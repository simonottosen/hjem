package hjem

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func areaJSON(t *testing.T, pts []lonLat) string {
	t.Helper()
	b, err := json.Marshal(pts)
	if err != nil {
		t.Fatalf("marshal area: %v", err)
	}
	return string(b)
}

// An area is checked before the lookup is charged, as a radius is: an area
// the lookup would refuse must not spend the client's budget, and a request
// naming both a radius and an area is ambiguous, not a union of the two.
func TestHandleLookupArea(t *testing.T) {
	s := &server{sessions: newSessionStore(), dc: refusingCacher{}, stats: NewHealthStats()}

	post := func(body string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/lookup", strings.NewReader(body))
		req.RemoteAddr = "198.51.100.7:51000"
		s.handleLookup()(rec, req)
		return rec.Code
	}

	tri := areaJSON(t, testTriangle)
	invalid := map[string]string{
		"ranges and area": `{"q":"Flensborggade 40","ranges":[250],"area":` + tri + `}`,
		"neither":         `{"q":"Flensborggade 40"}`,
		"empty area":      `{"q":"Flensborggade 40","area":[]}`,
		"blank address":   `{"q":" ","area":` + tri + `}`,
		"oversized area":  `{"q":"Flensborggade 40","area":` + areaJSON(t, squareAround(testLat, testLon, 2000)) + `}`,
		"bow-tie area":    `{"q":"Flensborggade 40","area":` + areaJSON(t, testBowTie) + `}`,
		"outside Denmark": `{"q":"Flensborggade 40","area":[[13.40,52.52],[13.41,52.52],[13.405,52.525]]}`,
	}
	for name, body := range invalid {
		if code := post(body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, code)
		}
	}

	if n := len(s.sessions.sessions); n != 0 {
		t.Errorf("%d sessions created by rejected requests, want 0", n)
	}
	if len(invalid) <= s.sessions.maxClient {
		t.Fatalf("only %d invalid requests against a budget of %d; the check below proves nothing", len(invalid), s.sessions.maxClient)
	}
	if code := post(`{"q":"Flensborggade 40","area":` + tri + `}`); code != http.StatusAccepted {
		t.Errorf("valid area after rejected ones: status %d, want 202", code)
	}

	// Waited for so its summary line cannot land in a later test capturing
	// the log; runLookup cancels its context last.
	s.sessions.mu.Lock()
	var running []*lookupSession
	for _, sess := range s.sessions.sessions {
		running = append(running, sess)
	}
	s.sessions.mu.Unlock()
	for _, sess := range running {
		<-sess.ctx.Done()
	}
}

func locate(t *testing.T, s *server, q string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/locate?q="+q, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return rec, body
}

// The page centres its map on what this returns, so latitude and longitude
// have to come out as themselves — Address keeps them crossed over.
func TestLocateFindsTheAddress(t *testing.T) {
	avTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/adresser/soeg" {
			w.Write([]byte(avSearchFixture))
			return
		}
		w.Write([]byte(avAddressFixture))
	})
	s := &server{dc: NewDawaCacher(newTestDB(t)), sessions: newSessionStore()}

	rec, body := locate(t, s, "R%C3%A5dhuspladsen+1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %v", rec.Code, body)
	}
	if body["address"] != "Rådhuspladsen 1, 1550 København V" {
		t.Errorf("address = %v", body["address"])
	}
	lat, _ := body["lat"].(float64)
	lon, _ := body["lon"].(float64)
	if math.Abs(lat-55.675627) > 1e-4 || math.Abs(lon-12.569578) > 1e-4 {
		t.Errorf("lat, lon = %v, %v, want about 55.6756, 12.5696", lat, lon)
	}

	// Locating is free: it is one cached address query, not a lookup.
	if n := len(s.sessions.clientStarts); n != 0 {
		t.Errorf("locating charged %d client budgets, want none", n)
	}
}

func TestLocateNotFound(t *testing.T) {
	avTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","beskrivelse":"","fund":[]}`))
	})
	s := &server{dc: NewDawaCacher(newTestDB(t))}

	rec, body := locate(t, s, "ikke+en+adresse")
	if rec.Code != http.StatusNotFound || body["error"] == "" || body["error"] == nil {
		t.Errorf("status %d, body %v, want 404 with an error", rec.Code, body)
	}
}

func TestLocateBlankQuery(t *testing.T) {
	avTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Adressevælger was asked about a blank query: %s", r.URL)
	})
	s := &server{dc: NewDawaCacher(newTestDB(t))}

	for _, q := range []string{"", "+++"} {
		rec, body := locate(t, s, q)
		if rec.Code != http.StatusBadRequest || body["error"] == nil {
			t.Errorf("q=%q: status %d, body %v, want 400 with an error", q, rec.Code, body)
		}
	}
}

// fixedCacher answers every request with the same addresses.
type fixedCacher []*Address

func (c fixedCacher) Do(DawaRequest) ([]*Address, error) { return c, nil }

func TestLocateAmbiguous(t *testing.T) {
	s := &server{dc: fixedCacher{{DawaID: "a"}, {DawaID: "b"}}}
	if rec, body := locate(t, s, "Vestergade"); rec.Code != http.StatusConflict {
		t.Errorf("status %d, body %v, want 409", rec.Code, body)
	}
}

// A link to an area search has to bring the area back, so the page can draw
// it and search it again; a radius search's link stays as it was.
func TestStoredLookupRoundTripWithArea(t *testing.T) {
	s := &server{results: newLookupStore(newTestDB(t))}

	areaResp := &LookupResponse{}
	if err := s.results.Save(context.Background(), "Flensborggade 40", nil, testTriangle, 1, areaResp); err != nil {
		t.Fatalf("Save area: %v", err)
	}
	radiusResp := &LookupResponse{}
	if err := s.results.Save(context.Background(), "Flensborggade 40", []int{500}, nil, 1, radiusResp); err != nil {
		t.Fatalf("Save radius: %v", err)
	}

	rec := getResult(t, s, areaResp.ResultID)
	var got struct {
		Ranges    []int     `json:"ranges"`
		Area      []lonLat  `json:"area"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if len(got.Area) != len(testTriangle) {
		t.Fatalf("area = %v, want %v", got.Area, testTriangle)
	}
	for i := range testTriangle {
		if got.Area[i] != testTriangle[i] {
			t.Errorf("vertex %d = %v, want %v", i, got.Area[i], testTriangle[i])
		}
	}
	if len(got.Ranges) != 0 {
		t.Errorf("ranges = %v, want none for an area search", got.Ranges)
	}

	rec = getResult(t, s, radiusResp.ResultID)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["area"]; ok {
		t.Errorf("radius result carries an area: %s", rec.Body)
	}
}

// The log line has to say an area was searched and how much ground it
// covered, in place of the radius it does not have.
func TestAreaLookupIsLogged(t *testing.T) {
	logged := captureLog(t)

	s := &server{sessions: newSessionStore(), dc: refusingCacher{}, stats: NewHealthStats()}
	sess, err := s.sessions.Create("")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// One square kilometre exactly, measured where DAR measures it.
	s.runLookup(sess, "Flensborggade 40", nil, squareAround(testLat, testLon, 1000), 0)

	lines := lookupLines(logged.String())
	if len(lines) != 1 {
		t.Fatalf("got %d summary lines, want 1:\n%s", len(lines), logged)
	}
	requireAll(t, lines[0], "area=4pts/1.00km2")
	if strings.Contains(lines[0], "radius=") {
		t.Errorf("an area search reported a radius: %s", lines[0])
	}
}

// The loading map outlines the drawn area instead of a circle.
func TestMapPlanCarriesTheArea(t *testing.T) {
	primary := mapAddr("Flensborggade", "40", "1669", 55.6700, 12.5500)

	plan := buildMapPlan(primary, []*Address{primary}, nil, testTriangle)
	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		RadiusM int      `json:"radius_m"`
		Area    []lonLat `json:"area"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RadiusM != 0 || len(got.Area) != 3 || got.Area[0] != testTriangle[0] {
		t.Errorf("plan = %s, want radius 0 and the drawn area", b)
	}

	b, _ = json.Marshal(buildMapPlan(primary, []*Address{primary}, []int{500}, nil))
	if strings.Contains(string(b), `"area"`) {
		t.Errorf("radius plan carries an area: %s", b)
	}
}
