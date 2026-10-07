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
	"strings"
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
		// A page size the two sides disagree on would not change which sales
		// exist, but it would change how many requests each spends reaching
		// them — and both budgets are counted in requests, not bytes.
		"pagesize": "500",
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("query %s=%q, want %q (frontend/src/lib/boliga.ts must match)", k, got.Get(k), v)
		}
	}
}

// deadBoliga stands in for a Boliga that refuses everything. 403 is the one
// failure the transport does not retry, so the server-side remainder fails
// immediately instead of walking the 2s..32s backoff ladder.
func deadBoliga(t *testing.T) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	orig := boligaSoldSearchURL
	boligaSoldSearchURL = srv.URL
	t.Cleanup(func() { boligaSoldSearchURL = orig })
}

// A street with no sales is a successful fetch, not a missing one. Judging the
// browser's contribution by the number of sales it returned rather than the
// number of streets it covered turns an empty street into "the browser gave us
// nothing", and fails the whole lookup when the server's remainder also fails.
func TestEmptyStreetCountsAsFetched(t *testing.T) {
	deadBoliga(t)
	s := testServer(t)
	sess, err := s.sessions.Create("")
	if err != nil {
		t.Fatal(err)
	}

	tasks := []BoligaPropertyRequest{task("Tomgade", 1456), task("Nørregade", 1165)}
	// The browser covered the first street and found it genuinely empty; the
	// second is left to the server, which cannot reach Boliga at all.
	sess.boligaIngest <- &BoligaIngest{
		LookupID: sess.ID,
		Fetched:  []BoligaFetchResult{{Task: tasks[0], Sales: nil}},
	}

	sales, _, err := s.clientStreetFetcher(sess)(tasks, NewProgress(), s.stats)
	if err != nil {
		t.Fatalf("lookup failed even though the browser covered a street: %v", err)
	}
	if len(sales) != 0 {
		t.Errorf("got %d sales, want 0", len(sales))
	}
}

// A session stays addressable for fifteen minutes after its lookup ends. An
// upload arriving in that window must be refused rather than buffered: nothing
// will ever read it, and it holds its decoded sales until eviction.
func TestIngestRefusedAfterRelayCloses(t *testing.T) {
	_, _ = fakeBoliga(t)
	orig := boligaClientWait
	boligaClientWait = 10 * time.Millisecond
	t.Cleanup(func() { boligaClientWait = orig })

	s := testServer(t)
	sess, err := s.sessions.Create("")
	if err != nil {
		t.Fatal(err)
	}

	// Let the relay time out and stop listening.
	s.clientStreetFetcher(sess)([]BoligaPropertyRequest{task("Vestergade", 1456)}, sess.Progress, s.stats)

	req := httptest.NewRequest(http.MethodPost, "/api/boliga/ingest",
		bytes.NewBufferString(fmt.Sprintf(`{"lookup_id":%q,"fetched":[]}`, sess.ID)))
	rec := httptest.NewRecorder()
	s.handleBoligaIngest()(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status %d, want 200 (ignored) — a closed relay must not answer 202", rec.Code)
	}
	if n := len(sess.boligaIngest); n != 0 {
		t.Errorf("%d ingest(s) retained after the relay closed, want 0", n)
	}
}

// bloatedIngest is the cheapest body that decodes into the most memory: a
// BoligaSaleItem is 184 bytes and `{},` is three.
func bloatedIngest(lookupID string, sales int) string {
	elems := strings.Repeat("{},", sales)
	return fmt.Sprintf(`{"lookup_id":%q,"fetched":[{"task":{},"sales":[%s]}]}`,
		lookupID, elems[:len(elems)-1])
}

// The byte limit bounds the request, not what it decodes into — measured at
// 16mb in, 3.5gb of live heap out. Both halves of the fix are checked here: the
// cap itself, and that an unrecognised lookup id is turned away before any of
// the payload is expanded.
func TestIngestBoundsDecodedSize(t *testing.T) {
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

	t.Run("over the sale cap", func(t *testing.T) {
		if rec := post(bloatedIngest(sess.ID, maxBoligaIngestSales+1)); rec.Code != http.StatusBadRequest {
			t.Errorf("status %d, want 400", rec.Code)
		}
	})

	t.Run("a real lookup still fits", func(t *testing.T) {
		// The densest search observed returned 2229 sales.
		if rec := post(bloatedIngest(sess.ID, 2229)); rec.Code != http.StatusAccepted {
			t.Errorf("status %d, want 202 — the cap must not reject real lookups", rec.Code)
		}
	})

	t.Run("unknown lookup id expands nothing", func(t *testing.T) {
		body := bloatedIngest("deadbeef", 500_000)

		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		rec := post(body)
		runtime.ReadMemStats(&after)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status %d, want 404", rec.Code)
		}
		// Decoding first would allocate 500k * 184 bytes = 92mb. Holding the
		// array as raw bytes costs what it weighs, about 1.5mb.
		grew := after.TotalAlloc - before.TotalAlloc
		if limit := uint64(20 << 20); grew > limit {
			t.Errorf("allocated %.1fmb rejecting an unknown lookup id, want under %.0fmb — "+
				"the payload is being decoded before the session is checked",
				float64(grew)/(1<<20), float64(limit)/(1<<20))
		}
	})
}

// seedCacher gives a test a cacher over its own database, already holding the
// addresses. NewBoligaCacher migrates Sale but not Address, and FetchSales
// writes addresses back, so the Address table has to be created separately.
func seedCacher(t *testing.T, addrs []*Address) *boligaCacher {
	t.Helper()

	db := newTestDB(t)
	if err := db.AutoMigrate(&Address{}); err != nil {
		t.Fatalf("migrate addresses: %v", err)
	}
	for _, a := range addrs {
		if err := db.Create(a).Error; err != nil {
			t.Fatalf("seed %s: %v", a.DawaID, err)
		}
	}
	return NewBoligaCacher(db)
}

// A failed street reads the same whether it cost the search one address or a
// thousand. One production lookup lost a single street out of fifty-one and
// told the user only "Kunne ikke hente salg for Matthæusgade" — no sense of
// how much of the radius that covered, and no sign that nothing came from
// cache either.
func TestPartialFetchWarningCountsCoverage(t *testing.T) {
	addr := func(street, number, zip string, collected time.Time) *Address {
		return &Address{
			DawaID: street + " " + number + ", " + zip, StreetName: street,
			StreetNumber: number, PostalCode: zip, MunicipalityCode: "101",
			BoligaCollectedAt: collected,
		}
	}

	// Three addresses on the street that fails, one on the street that works,
	// and one already cached — so every number in the summary is distinct and a
	// transposed pair cannot pass by coincidence.
	addrs := []*Address{
		addr("Matthæusgade", "1", "1666", time.Time{}),
		addr("Matthæusgade", "3", "1666", time.Time{}),
		addr("Matthæusgade", "5", "1666", time.Time{}),
		addr("Nørregade", "2", "1165", time.Time{}),
		addr("Enghavevej", "7", "1674", time.Now()),
	}

	bc := seedCacher(t, addrs)

	blocked := fmt.Errorf("Matthæusgade 1666: status 403")
	fetch := func(tasks []BoligaPropertyRequest, _ *Progress, _ *HealthStats) ([]BoligaSaleItem, []BoligaStreetFailure, error) {
		var failures []BoligaStreetFailure
		for _, tk := range tasks {
			if tk.StreetName == "Matthæusgade" {
				failures = append(failures, BoligaStreetFailure{Task: tk, Err: blocked})
			}
		}
		return nil, failures, nil
	}

	_, warnings, err := bc.FetchSales(addrs, NewProgress(), NewHealthStats(), fetch)
	if err != nil {
		t.Fatalf("FetchSales: %v", err)
	}
	if len(warnings) != 2 {
		t.Fatalf("got %d warnings, want a summary plus the failed street: %v", len(warnings), warnings)
	}

	// 5 addresses: 3 lost with Matthæusgade, 1 fetched on Nørregade, 1 cached.
	// 2 streets were asked for because the cached address needed no query.
	for _, want := range []string{
		"3 af 5 adresser blev ikke dækket",
		"1 af 2 gadeopslag fejlede",
		"De øvrige 2 blev dækket",
		"1 fra cache",
		"1 hentet fra Boliga nu",
	} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("summary is missing %q; got %q", want, warnings[0])
		}
	}

	// The per-street detail must survive the summary, not be replaced by it.
	if !strings.Contains(warnings[1], "Matthæusgade") || !strings.Contains(warnings[1], "blokeret af Boliga") {
		t.Errorf("warnings[1] = %q, want the Matthæusgade failure", warnings[1])
	}
}

// Boliga failing wholesale fails every street at once. Listing all of them
// pushes the one line carrying the counts off the top of the banner, so the
// detail is sampled and the rest is left as a number.
func TestManyFailedStreetsAreSummarizedNotListed(t *testing.T) {
	var addrs []*Address
	for i := range 10 {
		street := fmt.Sprintf("Gade%d", i)
		addrs = append(addrs, &Address{
			DawaID: street + " 1", StreetName: street, StreetNumber: "1",
			PostalCode: "1666", MunicipalityCode: "101",
		})
	}
	bc := seedCacher(t, addrs)

	fetch := func(tasks []BoligaPropertyRequest, _ *Progress, _ *HealthStats) ([]BoligaSaleItem, []BoligaStreetFailure, error) {
		failures := make([]BoligaStreetFailure, 0, len(tasks))
		for _, tk := range tasks {
			failures = append(failures, BoligaStreetFailure{Task: tk, Err: fmt.Errorf("status 429")})
		}
		// Not an error: every street failed, but FetchSales must still be able
		// to report partial coverage rather than abort, so the stub reports the
		// failures without claiming the whole fetch collapsed.
		return nil, failures, nil
	}

	_, warnings, err := bc.FetchSales(addrs, NewProgress(), NewHealthStats(), fetch)
	if err != nil {
		t.Fatalf("FetchSales: %v", err)
	}

	// Summary + maxStreetWarnings samples + the "and N others" line.
	if len(warnings) != maxStreetWarnings+2 {
		t.Fatalf("got %d warnings, want %d: %v", len(warnings), maxStreetWarnings+2, warnings)
	}
	if !strings.Contains(warnings[0], "10 af 10 adresser blev ikke dækket") {
		t.Errorf("summary = %q, want all 10 addresses reported uncovered", warnings[0])
	}
	if last := warnings[len(warnings)-1]; !strings.Contains(last, "og 7 andre gader") {
		t.Errorf("last warning = %q, want the remaining 7 streets collapsed into a count", last)
	}
}

// A fetch that loses nothing must stay silent. A summary on every lookup would
// train users to ignore the banner, which is where the real failures appear.
func TestCompleteFetchWarnsNothing(t *testing.T) {
	addrs := []*Address{{
		DawaID: "Nørregade 2, 1165", StreetName: "Nørregade",
		StreetNumber: "2", PostalCode: "1165", MunicipalityCode: "101",
	}}

	bc := seedCacher(t, addrs)

	fetch := func([]BoligaPropertyRequest, *Progress, *HealthStats) ([]BoligaSaleItem, []BoligaStreetFailure, error) {
		return []BoligaSaleItem{clientSale("Nørregade 2", 2_000_000)}, nil, nil
	}

	_, warnings, err := bc.FetchSales(addrs, NewProgress(), NewHealthStats(), fetch)
	if err != nil {
		t.Fatalf("FetchSales: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("got %v, want no warnings when every street came back", warnings)
	}
}
