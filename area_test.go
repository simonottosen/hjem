package hjem

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Flensborggade 40, where the page's example search is.
const testLat, testLon = 55.6700, 12.5500

// ringAround builds an n-gon of radius r metres around a point, the way the
// circle is built, then back in lon/lat as a drawing arrives.
func ringAround(lat, lon, r float64, n int) []lonLat {
	cE, cN := forwardUTM32(lat, lon)
	pts := make([]lonLat, n)
	for i := range pts {
		theta := 2 * math.Pi * float64(i) / float64(n)
		la, lo := inverseUTM32(cE+r*math.Cos(theta), cN+r*math.Sin(theta))
		pts[i] = lonLat{lo, la}
	}
	return pts
}

// squareAround is an axis-aligned square of side metres in EPSG:25832, whose
// area is therefore known exactly.
func squareAround(lat, lon, side float64) []lonLat {
	cE, cN := forwardUTM32(lat, lon)
	h := side / 2
	var pts []lonLat
	for _, d := range [][2]float64{{-h, -h}, {h, -h}, {h, h}, {-h, h}} {
		la, lo := inverseUTM32(cE+d[0], cN+d[1])
		pts = append(pts, lonLat{lo, la})
	}
	return pts
}

var testTriangle = []lonLat{{12.550, 55.670}, {12.555, 55.670}, {12.5525, 55.673}}

// testBowTie crosses itself with lobes of unequal size, so its shoelace area
// (about 0.14 km²) is inside both limits and only the crossing can reject it.
// A symmetric bow-tie nets out to nothing and is caught by the floor instead.
var testBowTie = []lonLat{{12.550, 55.670}, {12.560, 55.676}, {12.560, 55.670}, {12.550, 55.672}}

func TestValidateAreaAcceptsASimpleRing(t *testing.T) {
	got, err := validateArea(testTriangle)
	if err != nil {
		t.Fatalf("triangle rejected: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d vertices, want 3", len(got))
	}

	// A client closing the ring GeoJSON-style sends the first vertex again.
	// It is the same triangle, not a fourth point.
	closed := append(append([]lonLat{}, testTriangle...), testTriangle[0])
	got, err = validateArea(closed)
	if err != nil {
		t.Fatalf("closed triangle rejected: %v", err)
	}
	if len(got) != 3 || got[0] != testTriangle[0] || got[2] != testTriangle[2] {
		t.Errorf("closed triangle normalised to %v, want the three distinct vertices", got)
	}

	// A point repeated in sequence — a double click, or two points a stroke
	// laid within a metre of each other — adds no edge and is dropped.
	stutter := []lonLat{testTriangle[0], testTriangle[1], {12.555000001, 55.670000001}, testTriangle[2]}
	got, err = validateArea(stutter)
	if err != nil {
		t.Fatalf("triangle with a repeated point rejected: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("repeated point normalised to %v, want the three distinct vertices", got)
	}

	// The cap is the largest radius the page offers, so the polygon that radius
	// is actually searched as has to fit under it — and so does the most
	// detailed drawing of nearly the same circle.
	if _, err := validateArea(ringAround(testLat, testLon, maxLookupRadius, 64)); err != nil {
		t.Errorf("the 1000 m 64-gon a radius search uses was rejected: %v", err)
	}
	if _, err := validateArea(ringAround(testLat, testLon, 995, maxAreaVertices)); err != nil {
		t.Errorf("a %d-point ring of 995 m was rejected: %v", maxAreaVertices, err)
	}
}

func TestValidateAreaRejects(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	cases := map[string][]lonLat{
		"no points":           nil,
		"two points":          {{12.55, 55.67}, {12.555, 55.67}},
		"two points closed":   {{12.55, 55.67}, {12.555, 55.67}, {12.55, 55.67}},
		"too many points":     ringAround(testLat, testLon, 500, maxAreaVertices+1),
		"bow-tie":             testBowTie,
		"repeated vertex":     {{12.550, 55.670}, {12.555, 55.670}, {12.555, 55.673}, {12.555, 55.670}, {12.550, 55.673}},
		"larger than 1000 m":  squareAround(testLat, testLon, 2000),
		"just over the cap":   ringAround(testLat, testLon, 1010, maxAreaVertices),
		"smaller than 100 m²": squareAround(testLat, testLon, 9),
		"collinear":           {{12.550, 55.670}, {12.552, 55.671}, {12.554, 55.672}},
		"NaN":                 {{12.550, nan}, {12.555, 55.670}, {12.5525, 55.673}},
		"infinite":            {{inf, 55.670}, {12.555, 55.670}, {12.5525, 55.673}},
		"outside Denmark":     {{13.40, 52.52}, {13.41, 52.52}, {13.405, 52.525}},
		"latitude sent first": {{55.670, 12.550}, {55.670, 12.555}, {55.673, 12.5525}},
	}
	for name, pts := range cases {
		if got, err := validateArea(pts); err == nil {
			t.Errorf("%s: accepted as %v, want an error", name, got)
		}
	}
}

// A vertex resting on an edge it is not joined to pinches the ring into two,
// which is as much a self-intersection as a crossing. Drawn in plain metres:
// in lon/lat the touch would not survive projection exactly.
func TestSegmentsTouch(t *testing.T) {
	cases := []struct {
		name       string
		a, b, c, d [2]float64
		want       bool
	}{
		{"crossing", [2]float64{0, 0}, [2]float64{10, 10}, [2]float64{0, 10}, [2]float64{10, 0}, true},
		{"endpoint on the other", [2]float64{0, 0}, [2]float64{10, 0}, [2]float64{5, 0}, [2]float64{5, 5}, true},
		{"collinear overlap", [2]float64{0, 0}, [2]float64{10, 0}, [2]float64{5, 0}, [2]float64{15, 0}, true},
		{"collinear apart", [2]float64{0, 0}, [2]float64{10, 0}, [2]float64{11, 0}, [2]float64{15, 0}, false},
		{"parallel", [2]float64{0, 0}, [2]float64{10, 0}, [2]float64{0, 1}, [2]float64{10, 1}, false},
		{"short of each other", [2]float64{0, 0}, [2]float64{10, 0}, [2]float64{5, 1}, [2]float64{5, 5}, false},
	}
	for _, c := range cases {
		if got := segmentsTouch(c.a, c.b, c.c, c.d); got != c.want {
			t.Errorf("%s: segmentsTouch = %v, want %v", c.name, got, c.want)
		}
	}
}

var wktNumeric = regexp.MustCompile(`^POLYGON \(\((-?[0-9]+\.[0-9]{3} -?[0-9]+\.[0-9]{3}, )+-?[0-9]+\.[0-9]{3} -?[0-9]+\.[0-9]{3}\)\)$`)

func parseWKTRing(t *testing.T, wkt string) [][2]float64 {
	t.Helper()
	body := strings.TrimSuffix(strings.TrimPrefix(wkt, "POLYGON (("), "))")
	var ring [][2]float64
	for _, pair := range strings.Split(body, ", ") {
		f := strings.Fields(pair)
		e, err1 := strconv.ParseFloat(f[0], 64)
		n, err2 := strconv.ParseFloat(f[1], 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("unparseable vertex %q", pair)
		}
		ring = append(ring, [2]float64{e, n})
	}
	return ring
}

// The WKT is inlined into the GraphQL query text, so it must be the polygon
// and nothing else: digits only, closed, and the vertices that were drawn.
func TestAreaWKT(t *testing.T) {
	// Counter-clockwise, so the vertices come out in the order they went in.
	ccw := []lonLat{{12.550, 55.670}, {12.555, 55.670}, {12.5525, 55.673}}
	wkt := areaWKT(ccw)

	if !wktNumeric.MatchString(wkt) {
		t.Fatalf("WKT is not a plain numeric polygon: %s", wkt)
	}
	ring := parseWKTRing(t, wkt)
	if len(ring) != len(ccw)+1 || ring[0] != ring[len(ring)-1] {
		t.Fatalf("ring %v is not closed over %d vertices", ring, len(ccw))
	}
	for i, p := range ccw {
		lat, lon := inverseUTM32(ring[i][0], ring[i][1])
		if math.Abs(lat-p.lat()) > 1e-6 || math.Abs(lon-p.lon()) > 1e-6 {
			t.Errorf("vertex %d round-trips to (%.7f, %.7f), want (%.7f, %.7f)", i, lon, lat, p.lon(), p.lat())
		}
	}

	// Drawn clockwise, the ring is sent counter-clockwise like the circle.
	cw := []lonLat{ccw[2], ccw[1], ccw[0]}
	ring = parseWKTRing(t, areaWKT(cw))
	if signedAreaM2(ring[:len(ring)-1]) <= 0 {
		t.Errorf("a clockwise drawing was sent clockwise: %s", areaWKT(cw))
	}
}

// The cache key of a radius search must not move, or every entry cached before
// areas existed would be a miss. Spelled out as the string the radius search
// has always produced, not rebuilt from the code under test.
func TestDARRadiusCacheKeyUnchanged(t *testing.T) {
	d := DARNearbySearch{Addr: Address{Latitude: 12.5683, Longtitude: 55.6761}, Meters: 500}
	const want = "https://graphql.datafordeler.dk/DAR/v3?lat=55.6761000&lon=12.5683000&meters=500&segments=64&src=dar"
	if got := d.Request().URL.String(); got != want {
		t.Errorf("radius key = %s\nwant          %s", got, want)
	}
}

// The key is the primary key of the query cache, so an area's must be as
// distinct as the area and as short as a radius's, however many vertices.
func TestDARAreaCacheKey(t *testing.T) {
	key := func(addr Address, area []lonLat) string {
		return DARNearbySearch{Addr: addr, Area: area}.Request().URL.String()
	}
	a := Address{Latitude: testLon, Longtitude: testLat}
	b := Address{Latitude: 12.5510, Longtitude: 55.6710}

	tri := key(a, testTriangle)
	if tri != key(a, append([]lonLat{}, testTriangle...)) {
		t.Error("the same area gave two keys")
	}
	if tri != key(b, testTriangle) {
		t.Error("the key depends on the searched address, which does not change what lies inside the area")
	}
	other := []lonLat{{12.550, 55.670}, {12.556, 55.670}, {12.5525, 55.673}}
	if tri == key(a, other) {
		t.Error("two different areas share a key")
	}
	if strings.Contains(tri, "meters=") || !strings.Contains(tri, "area=") {
		t.Errorf("area key %s is not an area key", tri)
	}

	big := key(a, ringAround(testLat, testLon, 900, maxAreaVertices))
	if len(big) != len(tri) {
		t.Errorf("key length %d for %d vertices, %d for 3; want one fixed length", len(big), maxAreaVertices, len(tri))
	}
	if len(big) > 200 {
		t.Errorf("area key is %d bytes long", len(big))
	}
}

// What DAR is asked for is the drawn polygon, not a circle around the address.
func TestDARNearbySearchQueriesTheArea(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode DAR request: %v", err)
		}
		queries = append(queries, body.Query)
		fmt.Fprint(w, `{"data":{"DAR_Adressepunkt":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DATAFORDELER_API_KEY", "test-key")
	t.Setenv("DATAFORDELER_GRAPHQL_URL", srv.URL)

	addr := Address{Latitude: testLon, Longtitude: testLat}
	if _, err := (DARNearbySearch{Addr: addr, Area: testTriangle}).Fetch(); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if len(queries) != 1 {
		t.Fatalf("DAR was asked %d queries, want the one point query", len(queries))
	}
	if !strings.Contains(queries[0], `wkt: "`+areaWKT(testTriangle)+`"`) {
		t.Errorf("point query does not carry the area's WKT:\n%s", queries[0])
	}
}

// encodePolyline is the page's side of the format, here to build links.
func encodePolyline(pts []lonLat) string {
	var b strings.Builder
	var prevLat, prevLon int
	enc := func(v int) {
		u := v << 1
		if v < 0 {
			u = ^u
		}
		for u >= 0x20 {
			b.WriteByte(byte((0x20 | (u & 0x1f)) + 63))
			u >>= 5
		}
		b.WriteByte(byte(u + 63))
	}
	for _, p := range pts {
		lat, lon := int(math.Round(p.lat()*1e5)), int(math.Round(p.lon()*1e5))
		enc(lat - prevLat)
		enc(lon - prevLon)
		prevLat, prevLon = lat, lon
	}
	return b.String()
}

func TestDecodePolyline(t *testing.T) {
	// The worked example from Google's documentation of the format.
	got, err := decodePolyline("_p~iF~ps|U_ulLnnqC_mqNvxq`@")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []lonLat{{-120.2, 38.5}, {-120.95, 40.7}, {-126.453, 43.252}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if math.Abs(got[i][0]-want[i][0]) > 1e-9 || math.Abs(got[i][1]-want[i][1]) > 1e-9 {
			t.Errorf("point %d = %v, want %v", i, got[i], want[i])
		}
	}

	round, err := decodePolyline(encodePolyline(testTriangle))
	if err != nil {
		t.Fatalf("decode round trip: %v", err)
	}
	for i := range testTriangle {
		if round[i] != testTriangle[i] {
			t.Errorf("round trip point %d = %v, want %v", i, round[i], testTriangle[i])
		}
	}

	for _, bad := range []string{
		"_p~iF~ps|U_ulLnnqC_mqNvxq`", // cut off inside a value
		"_p~iF",                      // a latitude with no longitude
		"_p~iF ~ps|U",                // a character outside the alphabet
		"~~~~~~~~~~~~~~~~~~~~~~~~~~", // a value too long for any coordinate
	} {
		if pts, err := decodePolyline(bad); err == nil {
			t.Errorf("%q decoded to %v, want an error", bad, pts)
		}
	}
}

// A link carries the area at polyline precision, so the area a lookup searched
// has to be exactly the one its link and CSV export bring back: the same
// vertices, and the same cache key, or the export would search a slightly
// different polygon from scratch.
func TestAreaSurvivesItsLink(t *testing.T) {
	drawn := []lonLat{{12.5500012345, 55.6700049999}, {12.5550098765, 55.6700000001}, {12.5525043210, 55.6730061234}}

	searched, err := validateArea(drawn)
	if err != nil {
		t.Fatalf("validate drawn: %v", err)
	}
	decoded, err := decodePolyline(encodePolyline(drawn))
	if err != nil {
		t.Fatalf("decode link: %v", err)
	}
	linked, err := validateArea(decoded)
	if err != nil {
		t.Fatalf("validate link: %v", err)
	}

	for i := range searched {
		if searched[i] != linked[i] {
			t.Errorf("vertex %d searched as %v, linked as %v", i, searched[i], linked[i])
		}
	}
	key := func(a []lonLat) string { return DARNearbySearch{Area: a}.Request().URL.String() }
	if key(searched) != key(linked) {
		t.Errorf("lookup and link have different cache keys")
	}
}

// The export takes an area as the page's link spells it, and refuses the same
// areas a lookup refuses before doing any work for them.
func TestCSVDownloadRejectsInvalidArea(t *testing.T) {
	s := &server{dc: refusingCacher{}, stats: NewHealthStats()}

	for name, a := range map[string]string{
		"malformed": "_p~iF",
		"too large": encodePolyline(squareAround(testLat, testLon, 2000)),
		"bow-tie":   encodePolyline(testBowTie),
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/download/csv?q=Flensborggade+40&a="+url.QueryEscape(a), nil)
		s.handleCSVDownload()(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
	}
}

// recordingCacher answers the subject's own address search and every nearby
// search with fixed addresses, and keeps the nearby searches it was asked.
type recordingCacher struct {
	nearby   []*Address
	searches []DARNearbySearch
}

func (c *recordingCacher) Do(req DawaRequest) ([]*Address, error) {
	d, ok := req.(DARNearbySearch)
	if !ok {
		return nil, fmt.Errorf("unexpected request %T", req)
	}
	c.searches = append(c.searches, d)
	return append([]*Address{}, c.nearby...), nil
}

// An area is one search, filed under 0, and the subject is taken out of it as
// it is out of a radius: it is already at index 0 of the lookup's addresses.
func TestConstructRangesSearchesTheArea(t *testing.T) {
	subject := &Address{ID: 1, DawaID: "Flensborggade 40"}
	neighbour := &Address{ID: 2, DawaID: "Flensborggade 42"}
	c := &recordingCacher{nearby: []*Address{neighbour, subject}}
	s := &server{dc: c}

	got, err := s.constructRanges(subject, nil, testTriangle)
	if err != nil {
		t.Fatalf("constructRanges: %v", err)
	}
	if len(c.searches) != 1 || len(c.searches[0].Area) != 3 || c.searches[0].Meters != 0 {
		t.Fatalf("searches = %+v, want the one area search", c.searches)
	}
	if len(got) != 1 || len(got[0]) != 1 || got[0][0] != neighbour {
		t.Errorf("ranges = %v, want {0: [neighbour]}", got)
	}
}
