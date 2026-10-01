package hjem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"slices"
	"testing"
	"time"
)

// fakeBoliga stands in for api.boliga.dk. It records the streets it was asked
// for, which is how these tests tell "the browser fetched this" apart from
// "the server fell back and fetched it itself" — the distinction the whole
// feature turns on.
func fakeBoliga(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()

	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		street := r.URL.Query().Get("street")
		asked = append(asked, street)
		json.NewEncoder(w).Encode(BoligaSalesResponse{
			Meta: BoligaPageCrawl{CurrentPage: 1, TotalPages: 1},
			Sales: []BoligaSaleItem{{
				Addr:      street + " 1",
				SaleType:  "Alm. Salg",
				AmountDKK: 2_000_000,
			}},
		})
	}))
	t.Cleanup(srv.Close)

	orig := boligaSoldSearchURL
	boligaSoldSearchURL = srv.URL
	t.Cleanup(func() { boligaSoldSearchURL = orig })

	return srv, func() []string { return asked }
}

func testServer(t *testing.T) *server {
	t.Helper()
	return &server{sessions: newSessionStore(), stats: NewHealthStats()}
}

func task(street string, zip int) BoligaPropertyRequest {
	return BoligaPropertyRequest{StreetName: street, ZipCode: zip, MunicipalityID: 101}
}

func clientSale(addr string, amount int) BoligaSaleItem {
	return BoligaSaleItem{Addr: addr, SaleType: "Alm. Salg", AmountDKK: amount}
}

// The point of the feature: when the browser supplies the sales, the server
// must not issue the requests itself. If it does, the Boliga rate limit — 94%
// of production lookup latency — has not moved anywhere.
func TestClientFetchSkipsServerRequests(t *testing.T) {
	_, asked := fakeBoliga(t)
	s := testServer(t)
	sess, err := s.sessions.Create("")
	if err != nil {
		t.Fatal(err)
	}

	tasks := []BoligaPropertyRequest{task("Vestergade", 1456), task("Nørregade", 1165)}
	sess.boligaIngest <- &BoligaIngest{
		LookupID: sess.ID,
		Fetched: []BoligaFetchResult{
			{Task: tasks[0], Sales: []BoligaSaleItem{clientSale("Vestergade 1", 3_000_000)}},
			{Task: tasks[1], Sales: []BoligaSaleItem{clientSale("Nørregade 2", 4_000_000)}},
		},
	}

	sales, _, err := s.clientStreetFetcher(sess)(tasks, NewProgress(), s.stats)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := asked(); len(got) != 0 {
		t.Errorf("server fetched %v; the browser already supplied every street", got)
	}
	if len(sales) != 2 {
		t.Fatalf("got %d sales, want the 2 the browser posted", len(sales))
	}
	if sales[0].AmountDKK != 3_000_000 || sales[1].AmountDKK != 4_000_000 {
		t.Errorf("got %+v, want the browser's own sale amounts", sales)
	}
}

// A closed tab must not strand the lookup. This is the acceptance criterion
// "server finishes or times out cleanly".
func TestClientFetchFallsBackWhenNothingPosted(t *testing.T) {
	_, asked := fakeBoliga(t)
	orig := boligaClientWait
	boligaClientWait = 20 * time.Millisecond
	defer func() { boligaClientWait = orig }()

	s := testServer(t)
	sess, err := s.sessions.Create("")
	if err != nil {
		t.Fatal(err)
	}

	tasks := []BoligaPropertyRequest{task("Vestergade", 1456), task("Nørregade", 1165)}

	sales, _, err := s.clientStreetFetcher(sess)(tasks, NewProgress(), s.stats)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := asked(); len(got) != 2 {
		t.Errorf("server fetched %v, want both streets after the client said nothing", got)
	}
	if len(sales) != 2 {
		t.Errorf("got %d sales, want 2 from the server fallback", len(sales))
	}
}

// Per-task failures (429, 5xx) must cost only the streets that failed. The
// systemic case — the client abandoning everything — is the same code path
// with every task in Failed, so both are covered by the boundary here.
func TestClientFetchRetriesOnlyFailedStreets(t *testing.T) {
	_, asked := fakeBoliga(t)
	s := testServer(t)
	sess, err := s.sessions.Create("")
	if err != nil {
		t.Fatal(err)
	}

	tasks := []BoligaPropertyRequest{task("Vestergade", 1456), task("Nørregade", 1165)}
	sess.boligaIngest <- &BoligaIngest{
		LookupID: sess.ID,
		Fetched:  []BoligaFetchResult{{Task: tasks[0], Sales: []BoligaSaleItem{clientSale("Vestergade 1", 3_000_000)}}},
		Failed:   []BoligaPropertyRequest{tasks[1]},
	}

	sales, _, err := s.clientStreetFetcher(sess)(tasks, NewProgress(), s.stats)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := asked()
	if len(got) != 1 || got[0] != "Nørregade" {
		t.Errorf("server fetched %v, want only the street the client failed on", got)
	}
	if len(sales) != 2 {
		t.Errorf("got %d sales, want 1 from the client plus 1 from the server", len(sales))
	}
}

// A task the lookup never handed out must not enter the shared cache. Client
// sales are stored untrusted by product decision, but that decision is about
// the contents of an answer to a question we asked — not about letting a
// caller volunteer sales for arbitrary addresses that later users will read.
func TestClientFetchDiscardsUnsolicitedStreets(t *testing.T) {
	_, asked := fakeBoliga(t)
	s := testServer(t)
	sess, err := s.sessions.Create("")
	if err != nil {
		t.Fatal(err)
	}

	tasks := []BoligaPropertyRequest{task("Vestergade", 1456)}
	sess.boligaIngest <- &BoligaIngest{
		LookupID: sess.ID,
		Fetched: []BoligaFetchResult{
			{Task: task("Injiceret", 9999), Sales: []BoligaSaleItem{clientSale("Injiceret 1", 99_000_000)}},
		},
	}

	sales, _, err := s.clientStreetFetcher(sess)(tasks, NewProgress(), s.stats)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, sale := range sales {
		if sale.AmountDKK == 99_000_000 {
			t.Fatalf("a street the lookup never asked for reached the pipeline: %+v", sale)
		}
	}
	// The real task went unanswered, so the server still owes it.
	if got := asked(); len(got) != 1 || got[0] != "Vestergade" {
		t.Errorf("server fetched %v, want the solicited street it never received", got)
	}
}

// The ordering hazard SetBoligaTasks documents: a client that polls between
// the stage change and the task list would act on a stage with nothing to do
// and report every street failed.
func TestProgressCarriesTasksAsSoonAsStageIsVisible(t *testing.T) {
	_, _ = fakeBoliga(t)
	orig := boligaClientWait
	boligaClientWait = 250 * time.Millisecond
	defer func() { boligaClientWait = orig }()

	s := testServer(t)
	sess, err := s.sessions.Create("")
	if err != nil {
		t.Fatal(err)
	}
	p := sess.Progress
	tasks := []BoligaPropertyRequest{task("Vestergade", 1456)}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.clientStreetFetcher(sess)(tasks, p, s.stats)
	}()

	deadline := time.After(2 * time.Second)
	for {
		snap := p.Snapshot()
		if snap.Stage == StageBoligaClient {
			if len(snap.BoligaTasks) != 1 || snap.BoligaTasks[0].StreetName != "Vestergade" {
				t.Fatalf("stage %q exposed tasks %+v, want the street list", snap.Stage, snap.BoligaTasks)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("never reached StageBoligaClient")
		default:
		}
	}
	<-done

	// And it stops being sent once the stage moves on, so later polls do not
	// re-download a list the client has already acted on.
	if snap := p.Snapshot(); snap.Stage != StageBoligaClient && len(snap.BoligaTasks) != 0 {
		t.Errorf("stage %q still carries %d tasks", snap.Stage, len(snap.BoligaTasks))
	}
}

func TestBoligaIngestHandler(t *testing.T) {
	s := testServer(t)
	sess, err := s.sessions.Create("")
	if err != nil {
		t.Fatal(err)
	}

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/boliga/ingest", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		s.handleBoligaIngest()(rec, req)
		return rec
	}

	t.Run("unknown lookup id", func(t *testing.T) {
		// A stale tab posting into a lookup that has aged out must be told so,
		// not silently accepted into nothing.
		if rec := post(`{"lookup_id":"deadbeef","fetched":[]}`); rec.Code != http.StatusNotFound {
			t.Errorf("status %d, want 404", rec.Code)
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		if rec := post(`{`); rec.Code != http.StatusBadRequest {
			t.Errorf("status %d, want 400", rec.Code)
		}
	})

	t.Run("accepted once then ignored", func(t *testing.T) {
		body := fmt.Sprintf(`{"lookup_id":%q,"fetched":[]}`, sess.ID)
		if rec := post(body); rec.Code != http.StatusAccepted {
			t.Fatalf("status %d, want 202", rec.Code)
		}
		// Nothing is reading the channel, so the buffer is still full. A
		// duplicate post must not block the handler or fail the client.
		if rec := post(body); rec.Code != http.StatusOK {
			t.Errorf("duplicate post got %d, want 200", rec.Code)
		}
	})
}

// The acceptance criterion "no leaked goroutine". A client that posts after
// the lookup stopped waiting leaves a value in a buffered channel nobody will
// read; that must not keep a goroutine alive.
func TestClientFetchLeavesNoGoroutineBehind(t *testing.T) {
	_, _ = fakeBoliga(t)
	orig := boligaClientWait
	boligaClientWait = 10 * time.Millisecond
	defer func() { boligaClientWait = orig }()

	s := testServer(t)
	before := runtime.NumGoroutine()

	for i := 0; i < 20; i++ {
		sess, err := s.sessions.Create("")
		if err != nil {
			t.Fatal(err)
		}
		tasks := []BoligaPropertyRequest{task("Vestergade", 1456)}
		// Time out waiting, then post anyway — the closed-tab race.
		s.clientStreetFetcher(sess)(tasks, sess.Progress, s.stats)
		sess.boligaIngest <- &BoligaIngest{LookupID: sess.ID}
		// Reaching a terminal stage is what frees the session slot, so drive
		// the lookup to one as runLookup would; otherwise this loop trips the
		// eight-active cap rather than measuring goroutines.
		sess.Progress.Update(StageDone, "", 0, 0)
		sess.cancel()
	}

	// The server-side fallback leaves pooled keep-alive connections behind,
	// and each idle connection holds a read and write goroutine. Those are
	// http.Transport's, not ours, and counting them would make this test
	// report a leak that does not exist.
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()

	// Goroutine teardown is not instantaneous, so allow it to settle rather
	// than asserting on an exact count.
	var after int
	for i := 0; i < 50; i++ {
		after = runtime.NumGoroutine()
		if after <= before+2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("goroutines went from %d to %d across 20 timed-out lookups", before, after)
}

// Guards the assumption the client relies on: Boliga is queried by street, and
// the task the server hands out carries everything needed to rebuild that
// query in the browser.
func TestBoligaStreetTasksAreStableAndDeduped(t *testing.T) {
	addrs := []*Address{
		{MunicipalityCode: "0101", StreetName: "Vestergade", PostalCode: "1456", StreetNumber: "1"},
		{MunicipalityCode: "0101", StreetName: "Vestergade", PostalCode: "1456", StreetNumber: "3"},
		{MunicipalityCode: "0101", StreetName: "Nørregade", PostalCode: "1165", StreetNumber: "2"},
	}

	first := BoligaStreetTasks(addrs)
	if len(first) != 2 {
		t.Fatalf("got %d tasks, want 2 — the two Vestergade addresses share a street query", len(first))
	}

	// Order has to be reproducible: the server correlates the client's reply
	// against this list, and map iteration order would make that a coin toss.
	for i := 0; i < 20; i++ {
		if got := BoligaStreetTasks(addrs); !slices.Equal(got, first) {
			t.Fatalf("task order changed between calls: %+v then %+v", first, got)
		}
	}

	if first[0].StreetName != "Vestergade" || first[0].ZipCode != 1456 || first[0].MunicipalityID != 101 {
		t.Errorf("task %+v does not carry the fields the browser needs to query Boliga", first[0])
	}
}

// The client builds its own Boliga URL, so the query it sends must match what
// the server would have sent. A drift here silently changes which sales a
// lookup sees depending on who fetched them.
func TestServerBoligaQueryShape(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		json.NewEncoder(w).Encode(BoligaSalesResponse{Meta: BoligaPageCrawl{TotalPages: 1}})
	}))
	defer srv.Close()

	orig := boligaSoldSearchURL
	boligaSoldSearchURL = srv.URL
	defer func() { boligaSoldSearchURL = orig }()

	if _, err := (BoligaPropertyRequest{StreetName: "Vestergade", ZipCode: 1456, MunicipalityID: 101}).Fetch(); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"searchTab":    "1",
		"sort":         "date-a",
		"zipcodeFrom":  "1456",
		"zipcodeTo":    "1456",
		"street":       "Vestergade",
		"municipality": "101",
		"page":         "1",
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("query %s=%q, want %q (frontend/src/lib/boliga.ts must match)", k, got.Get(k), v)
		}
	}
}
