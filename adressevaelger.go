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
)

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
	Husnummer         avHusnummer `json:"husnummer"`
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

// Fetch resolves the free-text query to a single address. Phonetic search always
// returns a ranked list, so the top candidate ("bedste match") is taken; the
// remainder are near-miss variants such as floor/door permutations of the same
// building.
func (a AVFuzzySearch) Fetch() ([]*Address, error) {
	log.Printf("Searching Adressevælger for %q", a.Query)

	var search avSearchResponse
	if err := avGet("/adresser/soeg", url.Values{
		"tekst":    {a.Query},
		"maksimum": {strconv.Itoa(avMaxResults)},
	}, &search); err != nil {
		return nil, err
	}

	if search.Status != "ok" {
		return nil, fmt.Errorf("Adressevælger search failed: %s %s", search.Status, search.Beskrivelse)
	}

	if len(search.Fund) == 0 {
		// Not an error: api.go turns an empty result into "no found address".
		log.Printf("Adressevælger found no addresses for %q", a.Query)
		return nil, nil
	}

	best := search.Fund[0]
	log.Printf("Adressevælger matched %q -> %q (%s)", a.Query, best.Titel, best.ID)

	var lookup avAddressResponse
	if err := avGet("/adresser/"+best.ID, nil, &lookup); err != nil {
		return nil, err
	}
	if lookup.Status != "ok" {
		return nil, fmt.Errorf("Adressevælger lookup failed for %s: %s", best.ID, lookup.Status)
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
