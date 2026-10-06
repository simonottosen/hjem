package hjem

import (
	"bytes"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"gorm.io/gorm"
)

const maxBytesLimit = 1024 * 1024 // 1mb

const (
	// maxBoligaIngestBytes caps the browser's upload. A Boliga sale is about
	// 500 bytes of JSON and the largest production lookup on record fetched
	// 9541 of them — roughly 4.8mb — so the ordinary 1mb limit would reject
	// every real ingest. This leaves room for a worse one.
	maxBoligaIngestBytes = 16 * 1024 * 1024

	// A byte limit does not bound what the bytes decode into. A BoligaSaleItem
	// is 184 bytes, while the shortest JSON that produces one is the three of
	// `{},` — so a 16mb body of empty objects decodes to 3.5gb of live heap,
	// measured. These cap the decoded side of that ratio.
	//
	// Both are far above any real lookup: the densest search observed asked for
	// 43 streets and returned 2229 sales, and the sales cache for a whole
	// neighbourhood holds about 10000.
	maxBoligaIngestSales   = 100_000
	maxBoligaIngestStreets = 2_000
)

// boligaClientWait is how long a lookup waits for the browser's results before
// fetching the streets itself. It has to cover a slow client working through
// dozens of streets, while still not stranding a lookup whose tab was closed
// the moment it started.
//
// Boliga throttles to about one request per second per IP, so a browser cannot
// go faster than roughly one street per second however it is written — a
// measured 500 m search is 24 streets. This must therefore exceed the client's
// own BUDGET_MS (frontend/src/lib/boliga.ts), or the server would start
// refetching streets the browser is still on and do the work twice.
//
// A var so tests can shorten it, like boligaSoldSearchURL.
var boligaClientWait = 60 * time.Second

type SalesObject struct {
	Meta  *Address `json:"meta"`
	Sales []Sale   `json:"sales"`
}

type Response struct {
	Address      string  `json:"address_name"`
	SquareMeters float64 `json:"sq_meters"`
	PropertyType `json:"property_type"`
}

func NewServer(db *gorm.DB) *server {
	dc := NewDawaCacher(db)
	bc := NewBoligaCacher(db)

	return &server{
		dc:       dc,
		bc:       bc,
		sessions: newSessionStore(),
		stats:    NewHealthStats(),
	}
}

type server struct {
	dc       DawaCacher
	bc       BoligaCacher
	stats    *HealthStats
	sessions *sessionStore
}

func (s *server) handleLookup() http.HandlerFunc {
	type Request struct {
		Query  string `json:"q"`
		Ranges []int  `json:"ranges"`
		Filter int    `json:"filter_below_std"`
		// PreviousID is the caller's own last lookup id, if it has one. It is
		// what makes "a new search replaces my old one" work without also
		// replacing other users' searches.
		PreviousID string `json:"previous_lookup_id"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		var req Request
		body := http.MaxBytesReader(w, r.Body, maxBytesLimit)
		defer body.Close()

		// Decoded before a session exists, so a malformed body is reported
		// through the status code alone — there is no id to poll for it.
		if err := json.NewDecoder(body).Decode(&req); err != nil {
			replyJSONErr(w, err, http.StatusBadRequest)
			return
		}

		sess, err := s.sessions.Create(req.PreviousID)
		if err != nil {
			replyJSONErr(w, err, http.StatusTooManyRequests)
			return
		}

		// Run the lookup in a background goroutine so the HTTP response
		// returns immediately. The frontend polls /api/progress for status
		// and the result. This avoids Cloudflare tunnel timeouts.
		go s.runLookup(sess, req.Query, req.Ranges, req.Filter)

		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{
			"status":    "accepted",
			"lookup_id": sess.ID,
		})
	}
}

func (s *server) runLookup(sess *lookupSession, query string, ranges []int, filter int) {
	s.stats.RecordLookup()

	// Releases the context once this lookup is done one way or another.
	// Nothing reads it after we return, so cancelling here is free.
	defer sess.cancel()

	p := sess.Progress

	// Helper: check if this lookup was cancelled (replaced by a newer search
	// from the same client, or evicted for running implausibly long)
	cancelled := func() bool { return sess.ctx.Err() != nil }

	p.Update(StageDawa, "Søger adresse...", 0, 0)
	addrs, err := s.dc.Do(AVFuzzySearch{
		Query: query,
	})
	if err != nil {
		p.Update(StageError, err.Error(), 0, 0)
		return
	}
	if cancelled() {
		log.Printf("Lookup cancelled after address search for %q", query)
		return
	}

	if len(addrs) > 1 {
		p.Update(StageError, "non-unique address, be more specific", 0, 0)
		return
	}

	if len(addrs) == 0 {
		p.Update(StageError, "no found address", 0, 0)
		return
	}
	addr := addrs[0]

	p.Update(StageDawa, "Henter nærliggende adresser...", 0, 0)
	rangeMap, err := s.constructRanges(addr, ranges)
	if err != nil {
		p.Update(StageError, err.Error(), 0, 0)
		return
	}
	if cancelled() {
		log.Printf("Lookup cancelled after range construction for %q", query)
		return
	}

	for _, addrsInRange := range rangeMap {
		addrs = append(addrs, addrsInRange...)
	}

	p.Update(StageBoligaList, "Henter salgslister fra Boliga...", 0, 0)
	sales, fetchWarnings, err := s.bc.FetchSales(addrs, p, s.stats, s.clientStreetFetcher(sess))
	if err != nil {
		p.Update(StageError, err.Error(), 0, 0)
		return
	}
	if cancelled() {
		log.Printf("Lookup cancelled after Boliga fetch for %q", query)
		return
	}

	// Add any partial-failure warnings
	for _, w := range fetchWarnings {
		p.AddWarning(w)
	}

	addrs, sales = FilterAddressesByProperty(addr.BoligaPropertyKind, addrs, sales)

	luResp, err := FormatLookupResponse(addrs, rangeMap, sales, filter)
	if err != nil {
		p.Update(StageError, err.Error(), 0, 0)
		return
	}

	// Attach warnings to the response so the frontend can display them.
	// Appended, not assigned: FormatLookupResponse adds its own.
	luResp.Warnings = append(luResp.Warnings, fetchWarnings...)

	// Final cancellation check before setting result — don't overwrite a newer lookup
	if cancelled() {
		log.Printf("Lookup cancelled before result delivery for %q", query)
		return
	}

	// Fetch Dingeo valuation via FlareSolverr if configured, otherwise direct (non-fatal)
	luResp.Valuation, _ = FetchDingeoValuation(addr.DawaUUID)

	// Store result and mark done
	p.SetResult(luResp)
	p.Update(StageDone, "Færdig!", 0, 0)
}

// clientStreetFetcher asks the browser to fetch the street list and falls back
// to fetching server-side. Boliga rate-limits by IP and dominates lookup
// latency — 94% of a 3m41s production lookup, almost all of it backoff — so
// spreading the requests across users' own addresses is the whole point.
//
// Every exit from here leaves the lookup able to continue: the browser may
// return everything, some of it, or nothing at all, and the remainder is
// always fetched locally.
func (s *server) clientStreetFetcher(sess *lookupSession) BoligaStreetFetcher {
	return func(tasks []BoligaPropertyRequest, progress *Progress, stats *HealthStats) ([]BoligaSaleItem, []string, error) {
		if len(tasks) == 0 {
			return nil, nil, nil
		}

		// Tasks before stage: a client polling between the two would act on a
		// stage with no list and report everything failed.
		progress.SetBoligaTasks(tasks)
		progress.Update(StageBoligaClient,
			fmt.Sprintf("Henter salgslister fra Boliga (%d gader)...", len(tasks)), 0, len(tasks))

		var clientSales []BoligaSaleItem
		remaining := tasks

		// Nothing reads the channel once this returns, so a post that lands
		// just as we give up would otherwise pin several megabytes of sales in
		// the buffer until the session is evicted a quarter of an hour later.
		defer sess.closeBoligaIngest()

		select {
		case ing := <-sess.boligaIngest:
			clientSales, remaining = acceptBoligaIngest(ing, tasks, stats)
		case <-time.After(boligaClientWait):
			log.Printf("Boliga client fetch: nothing posted within %s; fetching all %d streets server-side",
				boligaClientWait, len(tasks))
		case <-sess.ctx.Done():
			return nil, nil, sess.ctx.Err()
		}

		if len(remaining) == 0 {
			return clientSales, nil, nil
		}

		// Counted in streets, not sales: a street can legitimately have no sales
		// at all, and judging by the sale count would turn that successful fetch
		// into "the browser gave us nothing" — failing the whole lookup below
		// over a street that was simply empty.
		fetchedStreets := len(tasks) - len(remaining)

		serverSales, warnings, err := fetchStreetsLocally(remaining, progress, stats)
		if err != nil {
			// fetchStreetsLocally only errors when every street failed. That is
			// fatal when it is all we have, but not when the browser already
			// covered part of the list.
			if fetchedStreets == 0 {
				return nil, warnings, err
			}
			log.Printf("Boliga client fetch: server-side remainder failed (%v); continuing with %d streets (%d sales) from the browser",
				err, fetchedStreets, len(clientSales))
			return clientSales, warnings, nil
		}

		return append(clientSales, serverSales...), warnings, nil
	}
}

// acceptBoligaIngest takes the sales the browser fetched and reports which
// tasks still need fetching.
//
// Streets this lookup never asked for are discarded. Client sales are written
// to the shared cache untrusted by deliberate product decision, but that is an
// argument about the contents of an answer, not about answering a question
// nobody asked: without this check any caller holding a lookup id could inject
// sales for arbitrary addresses into every later user's results.
func acceptBoligaIngest(ing *BoligaIngest, tasks []BoligaPropertyRequest, stats *HealthStats) ([]BoligaSaleItem, []BoligaPropertyRequest) {
	pending := make(map[BoligaPropertyRequest]bool, len(tasks))
	for _, t := range tasks {
		pending[t] = true
	}

	var sales []BoligaSaleItem
	var unsolicited int
	for _, f := range ing.Fetched {
		if !pending[f.Task] {
			unsolicited++
			continue
		}
		delete(pending, f.Task)
		sales = append(sales, f.Sales...)
		if stats != nil {
			stats.RecordBoligaOK()
		}
	}

	// Rebuilt from tasks rather than from the map, to keep the server's retry
	// order the same as the order it originally handed out.
	remaining := make([]BoligaPropertyRequest, 0, len(pending))
	for _, t := range tasks {
		if pending[t] {
			remaining = append(remaining, t)
		}
	}

	// Failed is logged, never acted on: correctness comes from tasks minus
	// fetched, so a client that omits it still gets its streets fetched. What
	// it adds is the distinction between a street Boliga refused and one the
	// browser never reached, which the remaining count alone cannot show.
	log.Printf("Boliga client fetch: browser returned %d/%d streets (%d sales), reported %d refused; %d left for the server",
		len(tasks)-len(remaining), len(tasks), len(sales), len(ing.Failed), len(remaining))
	if unsolicited > 0 {
		log.Printf("Boliga client fetch: discarded %d street(s) this lookup did not ask for", unsolicited)
	}

	return sales, remaining
}

// boligaIngestEnvelope holds the upload's two arrays as raw bytes, which cost
// what they weigh. Expanding them is what costs hundreds of times more, so it
// waits until the lookup id has been recognised — otherwise any caller at all
// could spend the server's memory without holding one.
type boligaIngestEnvelope struct {
	LookupID string          `json:"lookup_id"`
	Fetched  json.RawMessage `json:"fetched"`
	Failed   json.RawMessage `json:"failed"`
}

// decode expands the arrays one element at a time, stopping at the caps rather
// than allocating first and measuring afterwards.
func (env *boligaIngestEnvelope) decode() (*BoligaIngest, error) {
	ing := BoligaIngest{LookupID: env.LookupID}

	sales := 0
	err := decodeArrayCapped(env.Fetched, maxBoligaIngestStreets, func(f BoligaFetchResult) error {
		if sales += len(f.Sales); sales > maxBoligaIngestSales {
			return fmt.Errorf("ingest holds more than %d sales", maxBoligaIngestSales)
		}
		ing.Fetched = append(ing.Fetched, f)
		return nil
	})
	if err != nil {
		return nil, err
	}

	err = decodeArrayCapped(env.Failed, maxBoligaIngestStreets, func(t BoligaPropertyRequest) error {
		ing.Failed = append(ing.Failed, t)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &ing, nil
}

// decodeArrayCapped decodes a JSON array element by element, handing each to
// emit and refusing to read past limit of them. Decoding the array whole would
// size the allocation on the sender's say-so.
//
// A missing or null array is not an error: the client reports streets it never
// reached by omitting them.
func decodeArrayCapped[T any](raw json.RawMessage, limit int, emit func(T) error) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil {
		return err
	} else if tok != json.Delim('[') {
		return fmt.Errorf("expected a JSON array, got %v", tok)
	}

	for n := 0; dec.More(); n++ {
		if n >= limit {
			return fmt.Errorf("ingest holds more than %d streets", limit)
		}
		var v T
		if err := dec.Decode(&v); err != nil {
			return err
		}
		if err := emit(v); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) handleBoligaIngest() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := http.MaxBytesReader(w, r.Body, maxBoligaIngestBytes)
		defer body.Close()

		var env boligaIngestEnvelope
		if err := json.NewDecoder(body).Decode(&env); err != nil {
			replyJSONErr(w, err, http.StatusBadRequest)
			return
		}

		sess, ok := s.sessions.Get(env.LookupID)
		if !ok {
			replyJSONErr(w, ErrUnknownSession, http.StatusNotFound)
			return
		}

		ing, err := env.decode()
		if err != nil {
			replyJSONErr(w, err, http.StatusBadRequest)
			return
		}

		if sess.offerBoligaIngest(ing) {
			replyJSON(w, map[string]string{"status": "accepted"}, http.StatusAccepted)
			return
		}
		// The lookup already stopped waiting, or this is a duplicate post.
		// Either way the streets are covered server-side, so this is not an
		// error the client can or should act on.
		replyJSON(w, map[string]string{"status": "ignored"}, http.StatusOK)
	}
}

func (s *server) handleCSVDownload() http.HandlerFunc {
	type Request struct {
		Query  string `json:"q"`
		Ranges []int  `json:"ranges"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		params, _ := url.ParseQuery(r.URL.RawQuery)
		queries, ok := params["q"]
		if !ok {
			// handle error
		}
		query := queries[0]
		reqranges, ok := params["range"]
		if !ok {
			// handle error
		}
		rang, err := strconv.Atoi(reqranges[0])
		if err != nil {
			// handle error
		}

		addrs, err := s.dc.Do(AVFuzzySearch{
			Query: query,
		})
		if err != nil {
			// handle error
		}

		if len(addrs) > 1 {
			// handle error
			return
		}
		addr := addrs[0]

		ranges, err := s.constructRanges(addr, []int{rang})
		if err != nil {
			// handle error
		}

		for _, addrsInRange := range ranges {
			addrs = append(addrs, addrsInRange...)
		}

		// Server-side: the CSV export has no browser waiting on a progress
		// stream to relay through, and its volume is low enough not to matter.
		sales, _, err := s.bc.FetchSales(addrs, nil, s.stats, fetchStreetsLocally)
		if err != nil {
			// handle error
		}

		addrs, sales = FilterAddressesByProperty(addr.BoligaPropertyKind, addrs, sales)

		info, err := FormatLookupResponse(addrs, ranges, sales, 0)
		if err != nil {
			replyJSONErr(w, err, http.StatusBadRequest)
			return
		}

		w.Header().Add("Content-Type", "text/csv")
		csvWriter := csv.NewWriter(w)
		for i, s := range info.Sales {
			a := info.Addrs[s.AddrIndex]
			if i == 0 {
				row := append(a.Headers(), s.Headers()...)
				if err := csvWriter.Write(row); err != nil {
					// handle error
				}
			}

			row := append(a.ToSlice(), s.ToSlice()...)
			if err := csvWriter.Write(row); err != nil {

			}
		}
	}
}

func (s *server) handleProgress() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")

		sess, ok := s.sessions.Get(r.URL.Query().Get("id"))
		if !ok {
			// 404 rather than an idle progress event: an unknown id will never
			// advance, so the client has to be told to stop polling it.
			replyJSONErr(w, ErrUnknownSession, http.StatusNotFound)
			return
		}

		w.Write(sess.Progress.SnapshotJSON())
	}
}

func (s *server) handleHealth() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		json.NewEncoder(w).Encode(s.stats.Snapshot())
	}
}

func (s *server) handleMetrics() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Write([]byte(s.stats.PrometheusMetrics() + s.sessions.PrometheusMetrics()))
	}
}

//go:embed frontend/dist/index.html
var indexBytes []byte

func (s *server) handleIndex() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexBytes)
	}
}

//go:embed frontend/dist/app.bundle.js
var bundleBytes []byte

func (s *server) handleBundle() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		w.Write(bundleBytes)
	}
}

func (s *server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex())
	mux.HandleFunc("/dist/app.bundle.js", s.handleBundle())
	mux.HandleFunc("/api/lookup", s.handleLookup())
	mux.HandleFunc("/api/progress", s.handleProgress())
	mux.HandleFunc("/api/boliga/ingest", s.handleBoligaIngest())
	mux.HandleFunc("/api/health", s.handleHealth())
	mux.HandleFunc("/metrics", s.handleMetrics())
	mux.HandleFunc("/download/csv", s.handleCSVDownload())

	return mux
}

func (s *server) constructRanges(addr *Address, nearby []int) (map[int][]*Address, error) {
	o := make(map[int][]*Address)
	for _, r := range nearby {
		addrs, err := s.dc.Do(DARNearbySearch{
			Addr:   *addr,
			Meters: r,
		})
		if err != nil {
			return nil, err
		}

		for i := 0; i < len(addrs); i++ {
			a := addrs[i]
			if a.ID == addr.ID {
				addrs = append(addrs[:i], addrs[i+1:]...)
				break
			}
		}

		o[r] = addrs
	}

	return o, nil
}

func replyJSONErr(w http.ResponseWriter, err error, sc int) {
	replyJSON(w, struct {
		Err string `json:"error"`
	}{err.Error()}, sc)
}

func replyJSON(w http.ResponseWriter, i interface{}, sc int) {
	w.WriteHeader(sc)
	w.Header().Add("Content-Type", "application/json")
	json.NewEncoder(w).Encode(i)
}

type SquareMeterPrices struct {
	Global      map[time.Time]Aggregation `json:"global"`
	Projections []map[time.Time]int       `json:"projections"`
}

type LookupResponse struct {
	PrimaryIndex  int               `json:"primary_idx"`
	Addrs         []*Address        `json:"addresses"`
	Sales         []*JSONSale       `json:"sales"`
	Ranges        map[int][]int     `json:"ranges,omitempty"`
	SquareMeters  SquareMeterPrices `json:"sqmeters"`
	Valuation     *DingeoValuation  `json:"valuation,omitempty"`
	CompsEstimate *CompsEstimate    `json:"comps_estimate,omitempty"`
	Warnings      []string          `json:"warnings,omitempty"`
}

type JSONSale struct {
	AddrIndex int       `json:"addr_idx"`
	Amount    int       `json:"amount"`
	SqMeters  int       `json:"sq_meters"`
	Rooms     int       `json:"rooms"`
	BuildYear int       `json:"build_year"`
	When      time.Time `json:"when"`
}

func (s JSONSale) ToSlice() []string {
	return []string{
		strconv.Itoa(s.Amount),
		s.When.Format(time.RFC3339),
	}
}

func (s JSONSale) Headers() []string {
	return []string{
		"amount_dkk",
		"sold_date",
	}
}

// filterBuildingSales removes whole-building transactions where the same total
// price appears on multiple apartments at the same street+number on the same date.
// These are bulk sales recorded per-apartment by Boliga, not real apartment prices.
func filterBuildingSales(addrs []*Address, sales []*JSONSale) []*JSONSale {
	// Group by (street+number, date, amount) → count of apartments
	type key struct {
		building string
		date     string
		amount   int
	}
	counts := map[key]int{}
	for _, s := range sales {
		if s.AddrIndex >= len(addrs) {
			continue
		}
		addr := addrs[s.AddrIndex]
		k := key{
			building: addr.StreetName + " " + addr.StreetNumber,
			date:     s.When.Format("2006-01-02"),
			amount:   s.Amount,
		}
		counts[k]++
	}

	// Any group with 3+ apartments at the same price on the same date is a building sale
	var filtered []*JSONSale
	var removed int
	for _, s := range sales {
		if s.AddrIndex >= len(addrs) {
			filtered = append(filtered, s)
			continue
		}
		addr := addrs[s.AddrIndex]
		k := key{
			building: addr.StreetName + " " + addr.StreetNumber,
			date:     s.When.Format("2006-01-02"),
			amount:   s.Amount,
		}
		if counts[k] >= 3 && s.AddrIndex != 0 {
			// Bulk building sale — skip (but keep primary address sales always)
			removed++
			continue
		}
		filtered = append(filtered, s)
	}

	if removed > 0 {
		log.Printf("Filtered %d whole-building sales from %d total", removed, len(sales))
	}

	return filtered
}

func FormatLookupResponse(addrs []*Address, ranges map[int][]*Address, sales [][]Sale, stdf int) (*LookupResponse, error) {
	m := map[string]int{}
	var resp LookupResponse

	var i int
	for j, s := range sales {
		// Addresses with no sales carry no information and are dropped, which
		// is why the output index i runs independently of j. The searched
		// address is the exception: it is what primary_idx refers to, and a
		// home that simply has never been sold must not lose its place in the
		// response — dropping it would silently shift primary_idx onto a
		// neighbour's flat, and with it the comps estimate, the subject
		// property shown, and the sqm projections.
		if j != 0 && len(s) == 0 {
			continue
		}

		a := addrs[j]
		if j == 0 {
			resp.PrimaryIndex = i
		}
		m[a.DawaID] = i
		resp.Addrs = append(resp.Addrs, a)

		tempsales := make([]*JSONSale, len(s))
		for k, sale := range s {
			tempsales[k] = &JSONSale{
				AddrIndex: i,
				Amount:    sale.AmountDKK,
				SqMeters:  sale.SqMeters,
				Rooms:     sale.Rooms,
				BuildYear: sale.BuildYear,
				When:      sale.Date,
			}
		}
		resp.Sales = append(resp.Sales, tempsales...)

		i += 1
	}

	r := map[int][]int{}
	for meters, nearby := range ranges {
		var ids []int
		for _, a := range nearby {
			idx, ok := m[a.DawaID]
			if !ok {
				continue
			}

			ids = append(ids, idx)
		}

		r[meters] = ids
	}
	resp.Ranges = r

	// Remove whole-building sales: when multiple apartments at the same
	// street+number are recorded with identical price on the same date,
	// it's a bulk/portfolio transaction where the total building price
	// was assigned to every apartment. These distort per-apartment statistics.
	resp.Sales = filterBuildingSales(resp.Addrs, resp.Sales)

	normalSales, global := SalesStatistics(resp.Addrs, resp.Sales, stdf)
	resp.Sales = normalSales

	resp.SquareMeters = SquareMeterPrices{
		Global: global,
	}

	var projections []map[time.Time]int
	// addrs and sales are the caller's parallel slices, in which the searched
	// address is index 0. resp.PrimaryIndex indexes resp.Addrs instead, so it
	// must not be used here — the two index spaces differ as soon as any
	// address is dropped.
	primaryBuildingSize := addrs[0].BoligaBuildingSize
	if primaryBuildingSize > 0 {
		for _, s := range sales[0] {
			m := map[time.Time]int{}
			sqMeterPrice := float64(s.AmountDKK) / float64(primaryBuildingSize)
			yearInt, _, _ := s.Date.Date()
			saleYear, _ := time.Parse("2-1-2006", fmt.Sprintf("1-1-%d", yearInt))

			globalMean := resp.SquareMeters.Global[saleYear].Mean
			if globalMean == 0 {
				continue
			}
			factor := sqMeterPrice / float64(globalMean)

			for t, agg := range resp.SquareMeters.Global {
				if agg.Mean == 0 {
					continue
				}
				if t == saleYear {
					m[t] = int(math.Round(sqMeterPrice))
				}
				if t.After(saleYear) {
					m[t] = int(math.Round(float64(agg.Mean) * factor))
				}
			}

			projections = append(projections, m)
		}
	}
	resp.SquareMeters.Projections = projections

	// Comparable sales estimate, weighted by similarity to the subject
	// property — so it has to be the searched home, not merely the first one
	// that happens to have sales.
	if len(resp.Addrs) > 0 {
		resp.CompsEstimate = ComputeCompsEstimate(resp.Addrs[resp.PrimaryIndex], resp.Addrs, resp.Sales, global)
	}

	// Every home-specific figure — the comps estimate and the sqm projections
	// — is scaled by the subject property's size, and a size only ever reaches
	// us from a matched Boliga sale that carried square metres
	// (boliga.go:130). Without one the home cannot be valued at all. Say so:
	// the alternative is a dashboard that silently omits the number the user
	// came for. Previously this case was hidden, because the estimate was
	// computed against whichever sold neighbour fell at index 0.
	//
	// Report the gap, not a cause. Never having been sold is the usual
	// reason, but a failed street request, an address that matched no sale,
	// a home sold only within a family (boliga.go:313 keeps "Alm. Salg" only)
	// or a sale record with no square metres all land here too, and we cannot
	// tell them apart from this side.
	if addrs[0].BoligaBuildingSize == 0 {
		resp.Warnings = append(resp.Warnings,
			"Boliga har ikke oplyst en brugbar størrelse for denne bolig — ofte fordi den ikke har været solgt — så der kan ikke beregnes en værdi for netop denne bolig. Områdets kvadratmeterpriser vises stadig.")
	}

	return &resp, nil
}
