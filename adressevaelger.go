package hjem

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Adressevælgeren (adressevaelger.dk) is Klimadatastyrelsen's replacement for
// DAWA's autocomplete/fuzzy search. Datafordeleren's DAR GraphQL only supports
// exact matching (eq/in/startsWith), so free-text address entry cannot go there
// and uses this service instead. Radius search still goes to DAR — see
// datafordeler.go.
//
// A lookup takes two calls:
//
//  1. GET /adresser/soeg?tekst=... — phonetic, case-insensitive, implicit
//     wildcard. Returns ranked candidates carrying only id + titel.
//  2. GET /adresser/{id} — the full record. The husnummer payload is nested in
//     the response, so this single call yields coordinates, street name, postal
//     code and municipality code; no separate /husnumre/{id} call is needed.
//
// Step 1 has two blind spots, both of which return zero hits and are therefore
// indistinguishable from a genuinely nonexistent address. avResolveID works
// around them in order of how often they bite:
//
//   - Danish letters typed as ASCII ("Norrebrogade"). Retried against the same
//     search with ø/æ/å restored — see avStreetVariants.
//   - Historical designations ("Vestergade 1" after renumbering to "1A").
//     Retried against /vask/ (Adressevask), which is the only endpoint that
//     knows superseded betegnelser — see avVask.
//
// The two overlap, so the respelled variants are tried against /vask/ as well.
// Whatever the step, the resolved address is only returned if its DAR lifecycle
// status says it exists — see avStatusAccepted.
const (
	avDefaultBaseURL = "https://adressevaelger.dk"

	// avDefaultToken is the token published in the official documentation. The
	// service's FAQ states that user management ("brugerstyring") is not
	// implemented yet but that the parameter is mandatory. Override via
	// ADRESSEVAELGER_TOKEN once real credentials are issued.
	avDefaultToken = "adressevaelger123"

	// avMaxResults caps the candidate list. Only the top-ranked candidate is
	// used; a few extra make the responses easier to debug.
	avMaxResults = 10

	// avBatchSize is the number of ids per /adresser/?id_lokalids= call. The ids
	// travel in the query string, and the server rejects an over-long request
	// line with HTTP 431: 300 ids still succeed, 500 do not. 100 leaves a wide
	// margin and matches darInLimit.
	avBatchSize = 100

	// avMaxVariants caps how many transliterated spellings are tried after a
	// miss. Each costs one request, and they are only issued on a path that has
	// already failed, so the cap trades a slower miss for a better hit rate.
	// Streets needing more than a handful of substitutions are vanishingly rare.
	avMaxVariants = 8

	// Adressevask vaskestatus codes. Positive is a match, negative a failure;
	// the two differ in kind, so only the ones acted on are named here.
	//
	// 1000 and 900 are the only codes that identify one specific house number
	// (900 merely allowed a spelling variation in the street name). Notably 800
	// and 700 do not: the input was an interval such as "Christiansborg Slot
	// 1-5", and the service answers with the first house number in the interval,
	// or the last when the first does not exist.
	avVaskExact           = 1000
	avVaskSpellingVariant = 900

	// avVaskNoStreetInPost means the postnummer parsed but no street in that
	// district matched — the one failure a respelled street could fix.
	avVaskNoStreetInPost = -800
)

// avStatusAccepted is the set of DAR lifecycle statuses that denote an address
// worth returning. DAR exposes exactly four statuses for an Adresse — 2, 3, 4
// and 5 — so this doubles as a rejection of 4 and 5.
//
//   - 2 foreløbig: provisional but real, used for buildings under construction.
//     A user may legitimately look one up.
//   - 3 gældende: current.
//   - 4 nedlagt: was current, since decommissioned. Does not exist on the
//     ground, and /adresser/{id} answers 404 for it.
//   - 5 henlagt: was foreløbig and abandoned before ever becoming current, so
//     it never existed on the ground at all.
//
// Returning a 4 or a 5 would attribute nearby sales to a property that is not
// there — the same substitution this package avoids elsewhere.
var avStatusAccepted = map[string]string{
	"2": "foreløbig",
	"3": "gældende",
}

// avStatusNames labels the statuses that are rejected, for error messages.
var avStatusNames = map[string]string{
	"4": "nedlagt (no longer exists)",
	"5": "henlagt (abandoned before it was ever in use)",
}

// avCheckStatus returns an error if a DAR lifecycle status must not be
// returned to the caller. betegnelse is used only to describe the address.
func avCheckStatus(status flexStr, betegnelse string) error {
	s := string(status)
	if _, ok := avStatusAccepted[s]; ok {
		return nil
	}
	name, ok := avStatusNames[s]
	if !ok {
		name = fmt.Sprintf("status %q, which is not a usable address", s)
	}
	return fmt.Errorf("address %q is %s", betegnelse, name)
}

func avBaseURL() string {
	if u := os.Getenv("ADRESSEVAELGER_URL"); u != "" {
		return u
	}
	return avDefaultBaseURL
}

func avToken() string {
	if t := os.Getenv("ADRESSEVAELGER_TOKEN"); t != "" {
		return t
	}
	return avDefaultToken
}

// avSearchResponse is the /adresser/soeg payload.
type avSearchResponse struct {
	Status      string `json:"status"`
	Beskrivelse string `json:"beskrivelse"`
	Fund        []struct {
		Type        string `json:"type"`
		ID          string `json:"id"`
		Titel       string `json:"titel"`
		HusnummerID string `json:"husnummerId"`
	} `json:"fund"`
}

// avHusnummer is the access-address record nested inside an address lookup.
type avHusnummer struct {
	IDLokalID      string `json:"id_lokalid"`
	Husnummertekst string `json:"husnummertekst"`
	Vejnavn        string `json:"vejnavn"`

	Adgangspunkt struct {
		Koordinater struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		} `json:"koordinater"`
	} `json:"adgangspunkt"`

	Postnummer struct {
		Postnr string `json:"postnr"`
		Navn   string `json:"navn"`
	} `json:"postnummer"`

	NavngivenVejKommunedel struct {
		Kommune string `json:"kommune"`
		Vejkode string `json:"vejkode"`
	} `json:"navngivenvejkommunedel"`
}

// avAddress is a unit-address record. The same shape is returned singly by
// /adresser/{id} and as an array by /adresser/?id_lokalids=…
type avAddress struct {
	IDLokalID         string      `json:"id_lokalid"`
	Adressebetegnelse string      `json:"adressebetegnelse"`
	Etagebetegnelse   *string     `json:"etagebetegnelse"`
	Doerbetegnelse    *string     `json:"doerbetegnelse"`
	Status            flexStr     `json:"status"`
	Husnummer         avHusnummer `json:"husnummer"`
}

// avVaskResponse is the /vask/ (Adressevask) payload. Adressevask resolves a
// full address string — including historical designations — to a single current
// address, or to nothing. It is strictly a fallback, not a replacement for
// /adresser/soeg: it requires a postnummer (kode -300 without one) and returns
// no result at all when the input is ambiguous.
//
// Status is a JSON number here but a string on /adresser/{id}, hence flexStr.
type avVaskResponse struct {
	Vaskestatus struct {
		Kode  int    `json:"kode"`
		Tekst string `json:"tekst"`
	} `json:"vaskestatus"`

	Vaskeresultat struct {
		AdresseIDLokalID  string  `json:"adresse_id_lokalid"`
		Adressebetegnelse string  `json:"adressebetegnelse"`
		Status            flexStr `json:"status"`
	} `json:"vaskeresultat"`

	VaskeresultatHistorisk struct {
		Adressebetegnelse *string `json:"adressebetegnelse"`
	} `json:"vaskeresultat_historisk"`
}

// avAddressResponse is the /adresser/{id} payload.
type avAddressResponse struct {
	Status  string    `json:"status"`
	Adresse avAddress `json:"adresse"`
}

// avBatchResponse is the /adresser/?id_lokalids=… payload.
type avBatchResponse struct {
	Status   string      `json:"status"`
	Adresser []avAddress `json:"adresser"`
}

// avGet issues a GET against Adressevælgeren, injecting the mandatory token, and
// decodes the JSON body into out.
func avGet(path string, query url.Values, out any) error {
	u, err := url.Parse(avBaseURL() + path)
	if err != nil {
		return fmt.Errorf("invalid Adressevælger URL: %w", err)
	}
	if query == nil {
		query = url.Values{}
	}
	query.Set("token", avToken())
	u.RawQuery = query.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("Adressevælger request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Adressevælger returned status %d for %s", resp.StatusCode, path)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("Adressevælger decode failed for %s: %w", path, err)
	}

	return nil
}

// AVFuzzySearch is the Adressevælgeren replacement for DawaFuzzySearch. It
// implements the DawaRequest interface so it slots into the existing caching
// layer (dawaCacher.Do) unchanged.
type AVFuzzySearch struct {
	Query string
}

// Request returns the search URL. The token is deliberately omitted: this URL is
// also used as the cache key by dawaCacher, and baking a credential into the
// cache table would both leak it and invalidate every entry on rotation. avGet
// adds the token at call time.
func (a AVFuzzySearch) Request() *http.Request {
	u, err := url.Parse(avBaseURL() + "/adresser/soeg")
	if err != nil {
		return nil
	}

	q := u.Query()
	q.Set("tekst", a.Query)
	q.Set("maksimum", strconv.Itoa(avMaxResults))
	u.RawQuery = q.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil
	}

	return req
}

func (a AVFuzzySearch) MaxAge() time.Duration {
	return 365 * 24 * time.Hour
}

// avSoegID runs a phonetic search and returns the id of the top-ranked
// candidate, or "" if there were none. Phonetic search always returns a ranked
// list, so the top candidate ("bedste match") is taken; the remainder are
// near-miss variants such as floor/door permutations of the same building.
func avSoegID(query string) (id, titel string, err error) {
	var search avSearchResponse
	if err := avGet("/adresser/soeg", url.Values{
		"tekst":    {query},
		"maksimum": {strconv.Itoa(avMaxResults)},
	}, &search); err != nil {
		return "", "", err
	}

	if search.Status != "ok" {
		return "", "", fmt.Errorf("Adressevælger search failed: %s %s", search.Status, search.Beskrivelse)
	}

	if len(search.Fund) == 0 {
		return "", "", nil
	}

	return search.Fund[0].ID, search.Fund[0].Titel, nil
}

// avTranslitRules maps ASCII stand-ins to the Danish letters they represent.
// Digraphs come first so "oe" is preferred over bare "o" at the same position.
//
// "aa" -> "å" is deliberately absent: the search already understands it as a
// genuine Danish orthographic variant ("Aabenraa" resolves to "Åbenrå"), so
// including it would only widen the search space.
var avTranslitRules = []struct{ from, to string }{
	{"oe", "ø"},
	{"ae", "æ"},
	{"o", "ø"},
	{"a", "å"},
}

// avStreetVariants returns plausible re-spellings of a query typed without
// Danish characters, most-conservative first, capped at avMaxVariants.
//
// Only the street name is varied — everything from the first digit onwards is
// left untouched. The postnummer disambiguates the town on its own, so
// "Nørrebrogade 155, 2200 Kobenhavn N" already resolves while
// "Norrebrogade 155, 2200 København N" does not. Restricting substitution to the
// street keeps the combinatorial space small enough to brute-force.
//
// The original query is not included; the caller has already tried it.
func avStreetVariants(query string) []string {
	street, rest := avSplitStreet(query)
	if street == "" {
		return nil
	}

	var out []string
	seen := map[string]bool{strings.ToLower(street): true}

	// Breadth-first over substitutions: apply one rule to an already-generated
	// candidate and queue the result. This naturally orders single
	// substitutions before combinations, which is the likelier fix.
	queue := []string{street}
	for len(queue) > 0 && len(out) < avMaxVariants {
		cur := queue[0]
		queue = queue[1:]

		for _, rule := range avTranslitRules {
			for i := 0; i+len(rule.from) <= len(cur); i++ {
				if !strings.EqualFold(cur[i:i+len(rule.from)], rule.from) {
					continue
				}

				// Preserve the case of the text being replaced, so
				// "Osterbrogade" becomes "Østerbrogade" and not
				// "østerbrogade".
				to := rule.to
				if isUpperASCII(cur[i]) {
					to = strings.ToUpper(to)
				}

				cand := cur[:i] + to + cur[i+len(rule.from):]
				if seen[strings.ToLower(cand)] {
					continue
				}
				seen[strings.ToLower(cand)] = true

				out = append(out, cand+rest)
				queue = append(queue, cand)

				if len(out) >= avMaxVariants {
					return out
				}
			}
		}
	}

	return out
}

func isUpperASCII(b byte) bool { return b >= 'A' && b <= 'Z' }

// avSplitStreet splits a query into its street-name prefix and the remainder,
// cutting at the first digit — the house number. A query with no digit is all
// street name.
func avSplitStreet(query string) (street, rest string) {
	for i, r := range query {
		if r >= '0' && r <= '9' {
			return query[:i], query[i:]
		}
	}
	return query, ""
}

// avVask resolves a full address string through Adressevask, which is the only
// Adressevælger endpoint that knows historical designations. It returns "" and
// the vaskestatus code when there is no usable match, so the caller can tell
// which failures are worth retrying.
//
// A dead address is rejected here rather than downstream: /adresser/{id}
// answers 404 for a nedlagt address, so continuing would turn a meaningful
// "this address no longer exists" into an opaque lookup failure.
func avVask(query string) (string, int, error) {
	var vask avVaskResponse
	if err := avGet("/vask/", url.Values{"adresse": {query}}, &vask); err != nil {
		return "", 0, err
	}

	kode := vask.Vaskestatus.Kode
	id := vask.Vaskeresultat.AdresseIDLokalID
	if id == "" {
		// Ambiguous input also lands here: the service returns a null result
		// rather than a list when several addresses match.
		log.Printf("Adressevask found no match for %q (kode %d: %s)",
			query, kode, vask.Vaskestatus.Tekst)
		return "", kode, nil
	}

	// Only the two codes that identify one specific house number are accepted.
	// This is an allowlist rather than a rejection of the known interval codes:
	// an unrecognised match code means the service resolved the input by some
	// rule this code has never seen, and guessing that the rule preserves the
	// requested property is exactly the substitution being guarded against.
	if kode != avVaskExact && kode != avVaskSpellingVariant {
		return "", kode, fmt.Errorf(
			"Adressevask did not resolve %q to one specific address: it answered %q (kode %d: %s)",
			query, vask.Vaskeresultat.Adressebetegnelse, kode, vask.Vaskestatus.Tekst)
	}

	if err := avCheckStatus(vask.Vaskeresultat.Status, vask.Vaskeresultat.Adressebetegnelse); err != nil {
		return "", kode, err
	}

	if h := vask.VaskeresultatHistorisk.Adressebetegnelse; h != nil && *h != "" {
		log.Printf("Adressevask resolved historical %q -> %q", *h, vask.Vaskeresultat.Adressebetegnelse)
	}

	return id, kode, nil
}

// avResolveID turns free text into a single address id, widening the search only
// as far as needed. Returns "" when nothing matched, which api.go renders as
// "no found address".
func avResolveID(query string) (string, error) {
	id, titel, err := avSoegID(query)
	if err != nil {
		return "", err
	}
	if id != "" {
		log.Printf("Adressevælger matched %q -> %q (%s)", query, titel, id)
		return id, nil
	}

	variants := avStreetVariants(query)
	for _, variant := range variants {
		id, titel, err := avSoegID(variant)
		if err != nil {
			return "", err
		}
		if id != "" {
			log.Printf("Adressevælger matched %q via re-spelling %q -> %q (%s)", query, variant, titel, id)
			return id, nil
		}
	}

	id, kode, err := avVask(query)
	if err != nil {
		return "", err
	}
	if id != "" {
		log.Printf("Adressevask matched %q -> %s", query, id)
		return id, nil
	}

	// Adressevask has spelling tolerance of its own, but it is not the same as
	// the search's: it resolves "Hojgaardsvej" and not "Norrebrogade". So an
	// address that is both historical and typed in ASCII can miss every step so
	// far. Retry the same variants here, but only for the one failure a
	// respelled street could plausibly fix — the others (postnummer missing,
	// unknown or unparseable, house number not found) are not about the street,
	// and retrying them would just multiply requests on a hopeless query.
	if kode == avVaskNoStreetInPost {
		for _, variant := range variants {
			id, _, err := avVask(variant)
			if err != nil {
				return "", err
			}
			if id != "" {
				log.Printf("Adressevask matched %q via re-spelling %q -> %s", query, variant, id)
				return id, nil
			}
		}
	}

	log.Printf("Adressevælger found no addresses for %q", query)
	return "", nil
}

// Fetch resolves the free-text query to a single address.
func (a AVFuzzySearch) Fetch() ([]*Address, error) {
	log.Printf("Searching Adressevælger for %q", a.Query)

	id, err := avResolveID(a.Query)
	if err != nil {
		return nil, err
	}
	if id == "" {
		// Not an error: api.go turns an empty result into "no found address".
		return nil, nil
	}

	var lookup avAddressResponse
	if err := avGet("/adresser/"+id, nil, &lookup); err != nil {
		return nil, err
	}
	if lookup.Status != "ok" {
		return nil, fmt.Errorf("Adressevælger lookup failed for %s: %s", id, lookup.Status)
	}

	if err := avCheckStatus(lookup.Adresse.Status, lookup.Adresse.Adressebetegnelse); err != nil {
		return nil, err
	}

	addr, err := avToAddress(lookup.Adresse)
	if err != nil {
		return nil, err
	}

	return []*Address{addr}, nil
}

// avAddressesByIDs looks up full address records by DAR address id
// (DAR_Adresse.id_lokalId), batching to keep the query string short. Unknown ids
// are simply absent from the returned map.
func avAddressesByIDs(ids []string) (map[string]*Address, error) {
	out := make(map[string]*Address, len(ids))

	for _, batch := range chunk(ids, avBatchSize) {
		var resp avBatchResponse
		if err := avGet("/adresser/", url.Values{
			"id_lokalids": {strings.Join(batch, ",")},
		}, &resp); err != nil {
			return nil, err
		}
		if resp.Status != "ok" {
			return nil, fmt.Errorf("Adressevælger batch lookup failed: %s", resp.Status)
		}

		for i := range resp.Adresser {
			rec := resp.Adresser[i]
			addr := avMapFields(rec)
			avSetCoords(addr, rec) // best-effort; the caller may already have coords
			out[rec.IDLokalID] = addr
		}
	}

	return out, nil
}

// avEnrichAddresses fills in the fields DAR does not return inline — street
// name, house number, postal code and municipality code — by looking each
// address up in Adressevælgeren by id.
//
// These four are not cosmetic. BoligaSalesFromAddrs groups nearby addresses by
// {MunicipalityCode, StreetName, PostalCode} to build its per-street queries, so
// leaving them empty collapses every address into a single empty key and the
// sales lookup silently returns nothing.
//
// Coordinates already set by the caller are preserved; they are only taken from
// Adressevælgeren when the caller has none.
func avEnrichAddresses(addrs []*Address) error {
	ids := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a.DawaUUID != "" {
			ids = append(ids, a.DawaUUID)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	byID, err := avAddressesByIDs(ids)
	if err != nil {
		return fmt.Errorf("Adressevælger enrichment failed: %w", err)
	}

	var missing int
	for _, a := range addrs {
		src, ok := byID[a.DawaUUID]
		if !ok {
			missing++
			continue
		}

		a.StreetName = src.StreetName
		a.StreetNumber = src.StreetNumber
		a.PostalCode = src.PostalCode
		a.MunicipalityCode = src.MunicipalityCode

		if a.Latitude == 0 && a.Longtitude == 0 {
			a.Latitude, a.Longtitude = src.Latitude, src.Longtitude
		}
	}

	log.Printf("Adressevælger enriched %d/%d addresses", len(addrs)-missing, len(addrs))
	if missing > 0 {
		log.Printf("WARNING: %d addresses had no Adressevælger record; their Boliga sales will be skipped", missing)
	}

	return nil
}

// avMapFields maps the non-coordinate fields of an Adressevælger record onto the
// domain model. Split out from avToAddress because the enrichment path already
// has coordinates from DAR and must not reject a record that lacks an access
// point — the street and postal fields are still useful there.
func avMapFields(a avAddress) *Address {
	h := a.Husnummer

	return &Address{
		DawaUUID:         a.IDLokalID,
		DawaID:           a.Adressebetegnelse,
		StreetName:       h.Vejnavn,
		StreetNumber:     h.Husnummertekst,
		Floor:            a.Etagebetegnelse,
		Door:             a.Doerbetegnelse,
		PostalCode:       h.Postnummer.Postnr,
		MunicipalityCode: h.NavngivenVejKommunedel.Kommune,
	}
}

// avSetCoords projects the access point onto the Address, reporting whether the
// record had one.
//
// Coordinates are EPSG:25832 (ETRS89 / UTM 32N) easting/northing, not WGS84. A
// missing access point must not be projected: inverseUTM32(0, 0) yields a point
// off the coast of Africa, which would silently re-centre the radius search.
func avSetCoords(addr *Address, a avAddress) bool {
	k := a.Husnummer.Adgangspunkt.Koordinater
	if k.X == 0 && k.Y == 0 {
		return false
	}

	lat, lon := inverseUTM32(k.X, k.Y)

	// NOTE: Address stores x (longitude) in .Latitude and y (latitude) in
	// .Longtitude — a pre-existing field-naming quirk carried over from dawa.go.
	// datafordeler.go does the same; keep all three consistent.
	addr.Latitude = lon
	addr.Longtitude = lat
	return true
}

// avToAddress maps an Adressevælger address record onto the domain model. It
// requires an access point: this is the free-text path, and the resulting
// coordinates are what the radius search is centred on.
func avToAddress(a avAddress) (*Address, error) {
	addr := avMapFields(a)
	if !avSetCoords(addr, a) {
		return nil, fmt.Errorf("Adressevælger returned no access point for %s (%s)", a.Adressebetegnelse, a.IDLokalID)
	}
	return addr, nil
}
