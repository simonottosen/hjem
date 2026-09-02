package hjem

// datafordeler.go implements a radius (nearby) address search against the new
// Datafordeleren DAR GraphQL service. It is the planned replacement for DAWA's
// `cirkel` query (see DawaNearbySearch in dawa.go), which is being retired when
// DAWA closes on 17 August 2026.
//
// DAWA exposed a one-call REST circle search. Datafordeleren has no direct
// equivalent, so this implementation:
//   1. Projects the WGS84/ETRS89 centre point to EPSG:25832 (UTM zone 32N),
//      the CRS DAR stores all geometry in.
//   2. Builds a closed polygon approximating the search circle in metres.
//   3. Runs a three-step GraphQL chain (geometry lives only on
//      DAR_Adressepunkt, and DAR relationships are flat id strings rather than
//      nested objects): points within the polygon → the house numbers that
//      reference them → the full unit addresses, so the result granularity
//      matches DAWA's /adresser endpoint. See darQueryPoints/Husnumre/Adresser.
//
// The endpoint, the geometric `within` filter, the CRS, and the field names
// are taken from the official Datafordeler DAR documentation.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// EPSG:25832 (ETRS89 / UTM zone 32N) projection
//
// DAR geometry is stored in EPSG:25832, in metres. DAWA serves WGS84 lat/lon
// (degrees). ETRS89 and WGS84 agree to well under 1 m in Denmark, so we treat
// DAWA lat/lon as ETRS89 for projection purposes. The forward/inverse routines
// are the standard ellipsoidal Transverse Mercator series (Snyder, USGS PP
// 1395), accurate to sub-millimetre at UTM scales.
// ---------------------------------------------------------------------------

const (
	utmA       = 6378137.0           // GRS80 semi-major axis (m)
	utmF       = 1.0 / 298.257222101 // GRS80 flattening
	utmK0      = 0.9996              // UTM scale factor on central meridian
	utmFE      = 500000.0            // false easting
	utmLon0Deg = 9.0                 // central meridian of UTM zone 32
	// Northern hemisphere: false northing is 0.
)

// forwardUTM32 projects geographic coordinates (degrees) to EPSG:25832 (metres).
func forwardUTM32(latDeg, lonDeg float64) (easting, northing float64) {
	e2 := utmF * (2 - utmF) // first eccentricity squared
	ep2 := e2 / (1 - e2)    // second eccentricity squared (e'^2)

	lat := latDeg * math.Pi / 180
	lon := lonDeg * math.Pi / 180
	lon0 := utmLon0Deg * math.Pi / 180

	sinLat := math.Sin(lat)
	cosLat := math.Cos(lat)
	tanLat := math.Tan(lat)

	N := utmA / math.Sqrt(1-e2*sinLat*sinLat)
	T := tanLat * tanLat
	C := ep2 * cosLat * cosLat
	A := (lon - lon0) * cosLat

	M := utmA * ((1-e2/4-3*e2*e2/64-5*e2*e2*e2/256)*lat -
		(3*e2/8+3*e2*e2/32+45*e2*e2*e2/1024)*math.Sin(2*lat) +
		(15*e2*e2/256+45*e2*e2*e2/1024)*math.Sin(4*lat) -
		(35*e2*e2*e2/3072)*math.Sin(6*lat))

	easting = utmFE + utmK0*N*(A+(1-T+C)*A*A*A/6+
		(5-18*T+T*T+72*C-58*ep2)*math.Pow(A, 5)/120)
	northing = utmK0 * (M + N*tanLat*(A*A/2+
		(5-T+9*C+4*C*C)*math.Pow(A, 4)/24+
		(61-58*T+T*T+600*C-330*ep2)*math.Pow(A, 6)/720))
	return
}

// inverseUTM32 converts EPSG:25832 coordinates (metres) back to lat/lon (degrees).
func inverseUTM32(easting, northing float64) (latDeg, lonDeg float64) {
	e2 := utmF * (2 - utmF)
	ep2 := e2 / (1 - e2)
	lon0 := utmLon0Deg * math.Pi / 180

	M := northing / utmK0
	mu := M / (utmA * (1 - e2/4 - 3*e2*e2/64 - 5*e2*e2*e2/256))
	e1 := (1 - math.Sqrt(1-e2)) / (1 + math.Sqrt(1-e2))

	phi1 := mu +
		(3*e1/2-27*e1*e1*e1/32)*math.Sin(2*mu) +
		(21*e1*e1/16-55*e1*e1*e1*e1/32)*math.Sin(4*mu) +
		(151*e1*e1*e1/96)*math.Sin(6*mu) +
		(1097*e1*e1*e1*e1/512)*math.Sin(8*mu)

	sinPhi1 := math.Sin(phi1)
	cosPhi1 := math.Cos(phi1)
	tanPhi1 := math.Tan(phi1)
	C1 := ep2 * cosPhi1 * cosPhi1
	T1 := tanPhi1 * tanPhi1
	N1 := utmA / math.Sqrt(1-e2*sinPhi1*sinPhi1)
	R1 := utmA * (1 - e2) / math.Pow(1-e2*sinPhi1*sinPhi1, 1.5)
	D := (easting - utmFE) / (N1 * utmK0)

	lat := phi1 - (N1*tanPhi1/R1)*(D*D/2-
		(5+3*T1+10*C1-4*C1*C1-9*ep2)*math.Pow(D, 4)/24+
		(61+90*T1+298*C1+45*T1*T1-252*ep2-3*C1*C1)*math.Pow(D, 6)/720)
	lon := lon0 + (D-(1+2*T1+C1)*D*D*D/6+
		(5-2*C1+28*T1-3*C1*C1+8*ep2+24*T1*T1)*math.Pow(D, 5)/120)/cosPhi1

	return lat * 180 / math.Pi, lon * 180 / math.Pi
}

// circlePolygonWKT returns a closed WKT polygon in EPSG:25832 approximating a
// circle of radiusM metres around the given lat/lon. segments controls the
// vertex count (more = closer to a true circle).
func circlePolygonWKT(latDeg, lonDeg, radiusM float64, segments int) string {
	if segments < 8 {
		segments = 8
	}
	cE, cN := forwardUTM32(latDeg, lonDeg)

	var b strings.Builder
	b.WriteString("POLYGON ((")
	for i := 0; i <= segments; i++ {
		// i%segments closes the ring (last vertex == first).
		theta := 2 * math.Pi * float64(i%segments) / float64(segments)
		e := cE + radiusM*math.Cos(theta)
		n := cN + radiusM*math.Sin(theta)
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%.3f %.3f", e, n)
	}
	b.WriteString("))")
	return b.String()
}

// HaversineMeters returns the great-circle distance in metres between two
// lat/lon points. Exposed for the compare-radius tool.
func HaversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	return haversineKm(lat1, lon1, lat2, lon2) * 1000
}

// ---------------------------------------------------------------------------
// Datafordeleren DAR GraphQL radius search
// ---------------------------------------------------------------------------

const darGraphQLDefaultURL = "https://graphql.datafordeler.dk/DAR/v3"

// darStatusCurrent is the DAR lifecycle status code for a current ("gældende")
// object. DAWA returns only current addresses; DAR returns historic/invalid
// ones by default, so we filter client-side to match DAWA's behaviour.
const darStatusCurrent = "3"

// DAR's GraphQL is entity-based: relationships between entities are exposed as
// flat foreign-key id strings, NOT traversable nested objects, and geometry
// lives only on DAR_Adressepunkt. So a radius search is a three-step chain:
//
//  1. darQueryPoints   — DAR_Adressepunkt whose position lies `within` the
//     polygon → access-point ids + coordinates.
//  2. darQueryHusnumre — DAR_Husnummer whose `adgangspunkt` id is `in` that
//     set → house-number ids (linked back to their point).
//  3. darQueryAdresser — DAR_Adresse whose `husnummer` id is `in` that set →
//     the full unit addresses (matching DAWA /adresser).
//
// darMaxNodes is the largest page DAR will serve: `first: 1001` is rejected
// outright with "Invalid pagination input was supplied". It is a server limit,
// not a tuning knob, so a result set larger than this can only be read by
// following the cursor — see darPostAll. A 500 m search in central Copenhagen
// exceeds it comfortably.
const darMaxNodes = 1000

// darMaxPages bounds the cursor walk so a malformed cursor or a server that
// always reports hasNextPage cannot spin forever against a rate-limited API.
// At darMaxNodes per page this allows 200k nodes per query, far beyond any
// plausible radius search; exceeding it is a bug, so it is reported as one.
const darMaxPages = 200

// darInLimit is DAR's maximum number of elements allowed in a `where … in […]`
// filter. Larger id sets are queried in batches of this size.
const darInLimit = 100

// DAR is bitemporal: any query not filtering by id must pass registreringstid
// and virkningstid. We pass the same "now" snapshot (UTC, microsecond) to both
// so each query returns the currently-registered, currently-in-effect records.
// Format args per query: first (%d), registreringstid (%s), virkningstid (%s),
// and — for steps 2 and 3 — the inlined id list (%s). The id list is inlined
// rather than passed as a variable to avoid depending on the exact GraphQL
// list-variable type.

// darQueryPoints filters access points by geometry. The polygon (%s, the
// EPSG:25832 WKT from circlePolygonWKT) is inlined as a string literal rather
// than passed as a GraphQL variable: the geometry input is a custom scalar, and
// a String variable fed into it is silently coerced to null (matching nothing),
// whereas an inline literal is parsed correctly. This is the only geometric query.
//
// $after carries the cursor for pages after the first; it is passed as a
// GraphQL variable rather than inlined so the opaque base64 cursor needs no
// escaping. `after: null` is valid and means "start from the beginning".
const darQueryPoints = `query Points($after: String) {
  DAR_Adressepunkt(first: %d, after: $after, registreringstid: "%s", virkningstid: "%s", where: { position: { within: { crs: 25832, wkt: "%s" } } }) {
    pageInfo { hasNextPage endCursor }
    nodes { id_lokalId position { wkt } }
  }
}`

// darQueryHusnumre resolves access-point ids to house numbers.
const darQueryHusnumre = `query Husnumre($after: String) {
  DAR_Husnummer(first: %d, after: $after, registreringstid: "%s", virkningstid: "%s", where: { adgangspunkt: { in: [%s] } }) {
    pageInfo { hasNextPage endCursor }
    nodes { id_lokalId adgangspunkt }
  }
}`

// darQueryAdresser resolves house-number ids to full unit addresses.
const darQueryAdresser = `query Adresser($after: String) {
  DAR_Adresse(first: %d, after: $after, registreringstid: "%s", virkningstid: "%s", where: { husnummer: { in: [%s] } }) {
    pageInfo { hasNextPage endCursor }
    nodes { id_lokalId adressebetegnelse etagebetegnelse doerbetegnelse husnummer status }
  }
}`

// darEndpoint resolves the GraphQL URL (with the API key as a query parameter)
// from the environment. Addresses are public data, so an API key is sufficient;
// override the base URL with DATAFORDELER_GRAPHQL_URL if needed.
func darEndpoint() (string, error) {
	base := os.Getenv("DATAFORDELER_GRAPHQL_URL")
	if base == "" {
		base = darGraphQLDefaultURL
	}

	key := os.Getenv("DATAFORDELER_API_KEY")
	if key == "" {
		return "", errors.New("DATAFORDELER_API_KEY is not set")
	}

	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid DATAFORDELER_GRAPHQL_URL: %w", err)
	}
	q := u.Query()
	q.Set("apiKey", key)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// DARNearbySearch is the Datafordeleren equivalent of DawaNearbySearch. It
// implements the DawaRequest interface so it can be swapped into the existing
// caching layer (dawaCacher.Do).
type DARNearbySearch struct {
	Addr     Address
	Meters   int
	Segments int  // polygon vertex count; 0 → default (64)
	KeepAll  bool // include non-current (historic/invalid) addresses
}

func (d DARNearbySearch) segments() int {
	if d.Segments > 0 {
		return d.Segments
	}
	return 64
}

// addrLatLon extracts true (lat, lon) from an Address. NOTE: the Address struct
// stores DAWA's x in .Latitude (actually longitude) and y in .Longtitude
// (actually latitude) — a pre-existing field-naming quirk in dawa.go. We unpack
// it explicitly here so projection uses the correct values.
func addrLatLon(a Address) (lat, lon float64) {
	return a.Longtitude, a.Latitude
}

// Request returns a synthetic GET request whose URL uniquely encodes the search
// parameters. It is used ONLY as a cache key by dawaCacher; the real network
// call is a GraphQL POST built in Fetch. The distinct host keeps DAR cache
// entries separate from DAWA ones.
func (d DARNearbySearch) Request() *http.Request {
	req, _ := http.NewRequest("GET", darGraphQLDefaultURL, nil)
	lat, lon := addrLatLon(d.Addr)
	q := req.URL.Query()
	q.Add("src", "dar")
	q.Add("lat", strconv.FormatFloat(lat, 'f', 7, 64))
	q.Add("lon", strconv.FormatFloat(lon, 'f', 7, 64))
	q.Add("meters", strconv.Itoa(d.Meters))
	q.Add("segments", strconv.Itoa(d.segments()))
	req.URL.RawQuery = q.Encode()
	return req
}

func (d DARNearbySearch) MaxAge() time.Duration {
	return 365 * 24 * time.Hour
}

// Fetch runs the three-step DAR GraphQL radius search and returns the matching
// unit addresses (status-filtered to current, unless KeepAll is set).
func (d DARNearbySearch) Fetch() ([]*Address, error) {
	endpoint, err := darEndpoint()
	if err != nil {
		return nil, err
	}

	lat, lon := addrLatLon(d.Addr)
	wkt := circlePolygonWKT(lat, lon, float64(d.Meters), d.segments())
	// DAR is bitemporal: query the current registration/effect snapshot.
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000") + "Z"
	log.Printf("Fetching DAR GraphQL: radius %dm around %.6f,%.6f (%d-gon)", d.Meters, lat, lon, d.segments())
	log.Printf("DAR polygon (EPSG:25832): %s", truncate(wkt, 160))

	// Step 1: access points within the polygon. Collect ids regardless of
	// whether the position parses, so a coordinate-parsing problem doesn't look
	// like an empty geometry result (the two are logged separately).
	raw, err := darPostAll(endpoint,
		fmt.Sprintf(darQueryPoints, darMaxNodes, now, now, wkt),
		func(r *darPointResp) *darConnection[darPointNode] { return &r.Adressepunkt })
	if err != nil {
		return nil, err
	}
	pointPos := make(map[string][2]float64, len(raw))
	pointIDs := make([]string, 0, len(raw))
	for _, n := range raw {
		if n.IDLokalID == "" {
			continue
		}
		pointIDs = append(pointIDs, n.IDLokalID)
		if e, north, ok := parsePointWKT(n.Position.WKT); ok {
			pointPos[n.IDLokalID] = [2]float64{e, north}
		}
	}
	log.Printf("DAR step 1: %d points returned (%d with parseable position)", len(pointIDs), len(pointPos))
	if len(pointIDs) == 0 {
		return nil, nil
	}

	// Step 2: house numbers whose access point is in the set → husnummer→point.
	// DAR caps `in` lists at darInLimit, so the id list is queried in batches.
	husToPoint := make(map[string]string)
	for _, batch := range chunk(pointIDs, darInLimit) {
		nodes, err := darPostAll(endpoint,
			fmt.Sprintf(darQueryHusnumre, darMaxNodes, now, now, gqlIDList(batch)),
			func(r *darHusnummerResp) *darConnection[darHusnummerNode] { return &r.Husnummer })
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			husToPoint[n.IDLokalID] = n.Adgangspunkt
		}
	}
	log.Printf("DAR step 2: %d house numbers", len(husToPoint))
	if len(husToPoint) == 0 {
		return nil, nil
	}

	// Step 3: unit addresses whose husnummer is in the set (also batched).
	out := make([]*Address, 0, len(husToPoint))
	total := 0
	for _, batch := range chunk(keysOf(husToPoint), darInLimit) {
		nodes, err := darPostAll(endpoint,
			fmt.Sprintf(darQueryAdresser, darMaxNodes, now, now, gqlIDList(batch)),
			func(r *darAdresseResp) *darConnection[darAdresseNode] { return &r.Adresse })
		if err != nil {
			return nil, err
		}
		for i := range nodes {
			n := nodes[i]
			total++
			if !d.KeepAll && string(n.Status) != darStatusCurrent {
				continue
			}
			a := &Address{
				DawaUUID: n.IDLokalID,
				DawaID:   n.Betegnelse,
				Floor:    n.Etage,
				Door:     n.Doer,
			}
			// Coordinates: address → husnummer → access point → position.
			if pos, ok := pointPos[husToPoint[n.Husnummer]]; ok {
				lat, lon := inverseUTM32(pos[0], pos[1])
				a.Latitude = lon   // x — see addrLatLon note
				a.Longtitude = lat // y
			}
			out = append(out, a)
		}
	}
	log.Printf("DAR step 3: %d addresses (%d after status-filter)", total, len(out))

	// Step 4: street name, postal code and municipality code live on separate
	// DAR entities (DAR_NavngivenVej, DAR_Postnummer, DAR_NavngivenVejKommunedel)
	// reached through flat foreign keys, so DAR would need three further batched
	// round-trips per result set. Adressevælgeren returns the whole nested
	// husnummer record — vejnavn, husnummertekst, postnr and kommune — in one
	// call per 100 ids, so resolve them there instead. See avEnrichAddresses for
	// why these fields are load-bearing rather than cosmetic.
	if err := avEnrichAddresses(out); err != nil {
		return nil, err
	}

	return out, nil
}

// DARRawQuery runs an arbitrary GraphQL query against the DAR endpoint and
// returns the raw `data` JSON. Exposed for diagnostics (see cmd/dar-probe).
func DARRawQuery(query string, variables map[string]any) (json.RawMessage, error) {
	endpoint, err := darEndpoint()
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := darPost(endpoint, query, variables, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// darPostAll runs a paginated DAR query to exhaustion and returns every node.
//
// DAR caps a page at darMaxNodes and rejects any larger `first`, so a query
// whose result set exceeds that limit *silently* returns a partial answer
// unless the cursor is followed. For a radius search a partial answer is worse
// than an error: it yields plausible-looking but under-sampled comps. pick
// selects the connection from the decoded response, which is the only part
// that differs between the three queries.
func darPostAll[R any, N any](endpoint, query string, pick func(*R) *darConnection[N]) ([]N, error) {
	var all []N
	var after *string // nil on the first page → `after: null`

	for page := 1; ; page++ {
		var resp R
		if err := darPost(endpoint, query, map[string]any{"after": after}, &resp); err != nil {
			return nil, err
		}
		conn := pick(&resp)
		all = append(all, conn.Nodes...)

		// An empty cursor with more pages claimed would loop forever on the
		// same page, so treat it as the end.
		if !conn.PageInfo.HasNextPage || conn.PageInfo.EndCursor == "" {
			// Only worth logging when paging actually happened; single-page
			// queries are the common case and would drown out the signal.
			if page > 1 {
				log.Printf("DAR pagination: %d pages, %d nodes total", page, len(all))
			}
			return all, nil
		}
		if page >= darMaxPages {
			return nil, fmt.Errorf("DAR pagination exceeded %d pages (%d nodes so far); refusing to continue", darMaxPages, len(all))
		}
		cursor := conn.PageInfo.EndCursor
		after = &cursor
	}
}

// darPost sends a GraphQL POST and unmarshals the `data` field into out,
// returning any GraphQL-level errors verbatim.
func darPost(endpoint, query string, variables map[string]any, out any) error {
	payload := map[string]any{"query": query}
	if variables != nil {
		payload["variables"] = variables
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("DAR GraphQL returned status %d", resp.StatusCode)
	}

	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, len(env.Errors))
		for i, e := range env.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("DAR GraphQL errors: %s", strings.Join(msgs, "; "))
	}
	return json.Unmarshal(env.Data, out)
}

// gqlIDList renders ids as a comma-separated list of quoted GraphQL strings,
// for inlining into a `where: { field: { in: [...] } }` filter.
func gqlIDList(ids []string) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Quote(id)
	}
	return strings.Join(parts, ", ")
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// chunk splits s into consecutive slices of at most size elements.
func chunk(s []string, size int) [][]string {
	var out [][]string
	for i := 0; i < len(s); i += size {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		out = append(out, s[i:end])
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------------------------------------------------------------------------
// GraphQL response decoding (one struct per step)
// ---------------------------------------------------------------------------

// NOTE: darPost already strips the GraphQL `data` envelope, so these structs
// map the *contents* of `data` (the entity → nodes), with no `data` wrapper.

// darConnection is the envelope DAR wraps every result set in: one page of
// nodes plus the cursor needed to ask for the next.
type darConnection[N any] struct {
	PageInfo struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
	Nodes []N `json:"nodes"`
}

type darPointNode struct {
	IDLokalID string `json:"id_lokalId"`
	Position  struct {
		WKT string `json:"wkt"`
	} `json:"position"`
}

type darPointResp struct {
	Adressepunkt darConnection[darPointNode] `json:"DAR_Adressepunkt"`
}

type darHusnummerNode struct {
	IDLokalID    string `json:"id_lokalId"`
	Adgangspunkt string `json:"adgangspunkt"`
}

type darHusnummerResp struct {
	Husnummer darConnection[darHusnummerNode] `json:"DAR_Husnummer"`
}

type darAdresseNode struct {
	IDLokalID  string  `json:"id_lokalId"`
	Betegnelse string  `json:"adressebetegnelse"`
	Etage      *string `json:"etagebetegnelse"`
	Doer       *string `json:"doerbetegnelse"`
	Husnummer  string  `json:"husnummer"`
	Status     flexStr `json:"status"`
}

type darAdresseResp struct {
	Adresse darConnection[darAdresseNode] `json:"DAR_Adresse"`
}

// parsePointWKT parses "POINT (725243.12 6176052.34)" → easting, northing.
func parsePointWKT(wkt string) (easting, northing float64, ok bool) {
	s := strings.TrimSpace(wkt)
	if !strings.HasPrefix(strings.ToUpper(s), "POINT") {
		return 0, 0, false
	}
	open := strings.Index(s, "(")
	close := strings.LastIndex(s, ")")
	if open < 0 || close < 0 || close <= open {
		return 0, 0, false
	}
	fields := strings.Fields(s[open+1 : close])
	if len(fields) < 2 {
		return 0, 0, false
	}
	e, err1 := strconv.ParseFloat(fields[0], 64)
	n, err2 := strconv.ParseFloat(fields[1], 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return e, n, true
}

// flexStr decodes a JSON value that may be sent as either a string or a number
// (DAR is inconsistent across fields like postnr/kommunekode/status).
type flexStr string

func (f *flexStr) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "null" {
		*f = ""
		return nil
	}
	if len(s) > 0 && s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*f = flexStr(str)
		return nil
	}
	*f = flexStr(s)
	return nil
}
