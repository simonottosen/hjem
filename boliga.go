package hjem

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	boligaBaseUrl = "https://www.boliga.dk"
)

type PropertyType int

const (
	PropertyHouse PropertyType = iota + 1
	PropertySharedHouse
	PropertyApartment
	PropertyVacation
)

var (
	PropertyToName = map[PropertyType]string{
		PropertyHouse:       "house",
		PropertyApartment:   "apartment",
		PropertySharedHouse: "sharedhouse",
		PropertyVacation:    "vacation",
	}
)

type BoligaCacher interface {
	FetchSales([]*Address, *Progress, *HealthStats, BoligaStreetFetcher) ([][]Sale, []string, error)
}

// BoligaStreetFailure is one street query that did not come back. It is carried
// as the task plus the error rather than as a finished Danish sentence because
// the caller has to count the addresses sitting behind the failed street before
// it can say how much of the search went uncovered.
type BoligaStreetFailure struct {
	Task BoligaPropertyRequest
	Err  error
}

// addrStreetTask is the Boliga query that covers an address. Attributing a
// failed query back to the addresses it would have covered means grouping them
// the same way the task list was built, so both sides go through here.
func addrStreetTask(a *Address) BoligaPropertyRequest {
	mun, _ := strconv.Atoi(a.MunicipalityCode)
	zip, _ := strconv.Atoi(a.PostalCode)
	return BoligaPropertyRequest{StreetName: a.StreetName, ZipCode: zip, MunicipalityID: mun}
}

// BoligaStreetFetcher obtains the sales for a list of street queries. The
// server has two: fetchStreetsLocally, which issues the requests itself, and
// the session-aware one in api.go, which asks the browser to issue them and
// falls back to the local one for whatever the browser could not get.
//
// Only the fetching varies. Planning the street list and matching sales back
// to addresses are shared, so valuation behaviour cannot drift between the two
// paths.
type BoligaStreetFetcher func([]BoligaPropertyRequest, *Progress, *HealthStats) ([]BoligaSaleItem, []BoligaStreetFailure, error)

type boligaCacher struct {
	db *gorm.DB
}

func NewBoligaCacher(db *gorm.DB) *boligaCacher {
	db.AutoMigrate(&Sale{})
	return &boligaCacher{db}
}

const cacheExpiry time.Duration = time.Hour * 24 * 10 // 10 days

func (bc *boligaCacher) FetchSales(addrs []*Address, progress *Progress, stats *HealthStats, fetch BoligaStreetFetcher) ([][]Sale, []string, error) {
	cachedAddrs := map[int]*Address{}
	fetchAddrs := map[int]*Address{}
	var salesExpired []uint

	for i, addr := range addrs {
		if addr.BoligaCollectedAt.IsZero() {
			fetchAddrs[i] = addr
			continue
		}

		if time.Now().Sub(addr.BoligaCollectedAt) >= cacheExpiry {
			fetchAddrs[i] = addr
			salesExpired = append(salesExpired, addr.ID)
			continue
		}

		cachedAddrs[i] = addr
	}

	log.Printf("Boliga cache: %d cached, %d to fetch, %d expired (of %d total)",
		len(cachedAddrs), len(fetchAddrs), len(salesExpired), len(addrs))
	if stats != nil {
		stats.RecordCacheHit(len(cachedAddrs))
		stats.RecordCacheMiss(len(fetchAddrs))
	}

	if len(salesExpired) > 0 {
		bc.db.Where("addr_id IN ?", salesExpired).Delete(&Sale{})
	}

	sales := make([][]Sale, len(addrs))
	var warnings []string
	if len(fetchAddrs) > 0 {
		fetchTime := time.Now()
		addrsToFetch := make([]*Address, len(fetchAddrs))
		ids := make([]int, len(fetchAddrs))
		var i int
		for id, addr := range fetchAddrs {
			addrsToFetch[i] = addr
			ids[i] = id
			i += 1
		}

		// Only the uncached streets become fetch work. This is what keeps the
		// 10-day shared cache worth having once fetching moves to the browser:
		// a warm cache still hands the client an empty list.
		tasks := BoligaStreetTasks(addrsToFetch)
		totalSales, failures, err := fetch(tasks, progress, stats)
		if err != nil {
			// No coverage summary here: the lookup is being abandoned, so
			// "the other N addresses were covered" would describe a result
			// nobody is going to see.
			return nil, failureWarnings(failures), err
		}
		warnings = summarizeFetch(len(cachedAddrs), addrsToFetch, len(tasks), failures)
		matched := matchSalesToAddrs(addrsToFetch, totalSales)

		var salesToStore []Sale
		var addrsToStore []*Address
		for i, items := range matched {
			if len(items) == 0 {
				continue
			}

			origIdx := ids[i]
			addr := addrs[origIdx]

			psales := make([]Sale, len(items))
			for j, item := range items {
				psales[j] = Sale{
					AddrID:    addr.ID,
					AmountDKK: item.AmountDKK,
					SqMeters:  item.SqMeters,
					Rooms:     int(item.Rooms),
					BuildYear: item.BuildYear,
					Date:      item.SoldDate,
				}
			}

			sales[origIdx] = psales
			salesToStore = append(salesToStore, psales...)

			// Update address with best available Boliga metadata across all matched sales
			addr.BoligaCollectedAt = fetchTime
			for _, item := range items {
				if addr.BoligaBuildingSize == 0 && item.SqMeters > 0 {
					addr.BoligaBuildingSize = item.SqMeters
				}
				if addr.BoligaBuiltYear == 0 && item.BuildYear > 0 {
					addr.BoligaBuiltYear = item.BuildYear
				}
				if addr.BoligaRooms == 0 && item.Rooms > 0 {
					addr.BoligaRooms = int(item.Rooms)
				}
				if addr.BoligaPropertyKind == 0 && item.PropertyType > 0 {
					addr.BoligaPropertyKind = item.PropertyType
				}
			}

			addrsToStore = append(addrsToStore, addr)
		}

		if len(salesToStore) > 0 {
			if err := bc.db.CreateInBatches(&salesToStore, 50).Error; err != nil {
				return nil, warnings, err
			}
		}

		// Mark ALL fetched addresses as checked — even those without matches.
		// Without this, unmatched addresses (BoligaCollectedAt stays zero) trigger
		// a full re-fetch of their entire street on every subsequent search.
		for _, addr := range addrsToFetch {
			if addr.BoligaCollectedAt.IsZero() {
				addr.BoligaCollectedAt = fetchTime
				addrsToStore = append(addrsToStore, addr)
			}
		}

		// A lookup asking for more than one range holds every inner address
		// twice: constructRanges answers each range with its own DAR search and
		// its own freshly loaded structs, and the circles nest, so the same row
		// arrives under two pointers. Only one of them can match — the match
		// map is keyed by address string — leaving its twin to the stamping
		// loop above, and the two land in this slice as separate rows sharing
		// an id. Writing them one at a time tolerated that: the second Save
		// simply overwrote the first. One upsert cannot, because Postgres
		// rejects an ON CONFLICT DO UPDATE that would touch a row twice in the
		// same statement, and the whole lookup dies with it. SQLite accepts the
		// duplicate, so this would only ever have failed in the deployment.
		//
		// Keeping the first copy keeps the right one: the matched address is
		// appended by the loop above the stamping loop, so it is already here
		// by the time its bare twin arrives, and the metadata is preserved.
		seen := make(map[uint]bool, len(addrsToStore))
		uniqueAddrs := addrsToStore[:0]
		for _, addr := range addrsToStore {
			if seen[addr.ID] {
				continue
			}
			seen[addr.ID] = true
			uniqueAddrs = append(uniqueAddrs, addr)
		}
		addrsToStore = uniqueAddrs

		// A dense first lookup stamps some 2900 addresses, and a statement each
		// is merely slow against SQLite but a network round-trip each against
		// the Postgres deployment. These rows all exist already, yet each
		// carries its own metadata, so no single UPDATE ... WHERE id IN (...)
		// expresses them: an upsert is the only batched row-update either
		// engine offers.
		//
		// The conflict target is named rather than left implicit because
		// addresses carry unique indexes on dawa_id and dawa_uuid as well, and
		// only a conflict on the arbiter index resolves to DO UPDATE; every row
		// here is written back under the id it was read with. Batched at 50
		// like the sales above: an address is nineteen columns and a radius
		// search can carry ten thousand of them, which as one statement would
		// run past the 65535 bind parameters Postgres accepts.
		if len(addrsToStore) > 0 {
			if err := bc.db.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "id"}},
				UpdateAll: true,
			}).CreateInBatches(addrsToStore, 50).Error; err != nil {
				return nil, warnings, err
			}
		}
	}

	if len(cachedAddrs) > 0 {
		m := map[uint]int{}
		addrIds := make([]uint, len(cachedAddrs))
		var i int
		for id, addr := range cachedAddrs {
			m[addr.ID] = id
			addrIds[i] = addr.ID
			i += 1
		}

		var dbsales []Sale
		bc.db.Where("addr_id IN ?", addrIds).Find(&dbsales)

		for _, s := range dbsales {
			sid := m[s.AddrID]
			sales[sid] = append(sales[sid], s)
		}
	}

	return sales, warnings, nil
}

// maxStreetWarnings bounds the per-street detail. Boliga failing wholesale
// fails every street in the search at once — fifty-one of them in one
// production lookup — and that many near-identical lines in the banner buries
// the summary that says how much was actually lost.
const maxStreetWarnings = 3

func failureWarnings(failures []BoligaStreetFailure) []string {
	shown := min(len(failures), maxStreetWarnings)
	warnings := make([]string, 0, shown+1)
	for _, f := range failures[:shown] {
		warnings = append(warnings, fmt.Sprintf("Kunne ikke hente salg for %s (%s)",
			f.Task.StreetName, classifyError(f.Err)))
	}
	if rest := len(failures) - shown; rest > 0 {
		warnings = append(warnings, fmt.Sprintf("… og %d andre gader", rest))
	}
	return warnings
}

// summarizeFetch puts a count in front of the per-street failures. On its own
// "Kunne ikke hente salg for Matthæusgade" says nothing about how much of the
// search it cost: the same sentence appears whether one street out of fifty-one
// failed or forty-nine did.
//
// Coverage is counted in addresses because that is the unit the search was made
// in — a user asks for a radius, not for streets. It deliberately reports
// addresses *covered* rather than addresses *with sales*: most addresses have
// no recent sale at all (one production lookup matched 1113 sales across 9541
// addresses), so equating "we asked about it" with "we found something" would
// be wrong far more often than right.
func summarizeFetch(cached int, fetchAddrs []*Address, streetCount int, failures []BoligaStreetFailure) []string {
	if len(failures) == 0 {
		return nil
	}

	failed := make(map[BoligaPropertyRequest]bool, len(failures))
	for _, f := range failures {
		failed[f.Task] = true
	}

	var missing int
	for _, a := range fetchAddrs {
		if failed[addrStreetTask(a)] {
			missing++
		}
	}

	// Derived rather than passed in: every address is either cached or queued
	// for fetching, so a separate total could only ever disagree with the parts.
	total := cached + len(fetchAddrs)
	summary := fmt.Sprintf(
		"Ufuldstændigt datagrundlag: %d af %d adresser blev ikke dækket, fordi %d af %d gadeopslag fejlede. De øvrige %d blev dækket (%d fra cache, %d hentet fra Boliga nu).",
		missing, total, len(failures), streetCount,
		total-missing, cached, len(fetchAddrs)-missing)

	return append([]string{summary}, failureWarnings(failures)...)
}

type Sale struct {
	// Every query against this table filters on addr_id — reading a cached
	// address's sales, expiring them, and re-pointing them when duplicate
	// address rows are collapsed — and without an index each of those is a
	// full scan.
	AddrID    uint      `json:"-" gorm:"index"`
	AmountDKK int       `json:"amount"`
	SqMeters  int       `json:"sq_meters"`
	Rooms     int       `json:"rooms"`
	BuildYear int       `json:"build_year"`
	Date      time.Time `json:"time"`
}

type BoligaSaleItem struct {
	EstateId   int       `json:"estateId"`
	EstateCode int       `json:"estateCode"`
	SoldDate   time.Time `json:"soldDate"`

	Addr             string       `json:"address"`
	Guid             string       `json:"guid"`
	MunicipalityCode int          `json:"municipalityCode"`
	AmountDKK        int          `json:"price"`
	PropertyType     PropertyType `json:"propertyType"`
	SqMeters         int          `json:"size"`
	Rooms            float64      `json:"rooms"`
	BuildYear        int          `json:"buildYear"`
	Lattitude        float64      `json:"latitude"`
	Longtitude       float64      `json:"longitude"`
	ZipCode          int          `json:"zipCode"`
	City             string       `json:"city"`
	PriceChange      float64      `json:"change"`
	SaleType         string       `json:"saleType"`
}

type BoligaPageCrawl struct {
	Page        uint `gorm:"primaryKey"`
	CurrentPage int  `json:"pageIndex"`
	TotalPages  int  `json:"totalPages"`
	Error       string
	Runtime     time.Duration
	CreatedAt   time.Time
}

// BoligaSalesFromAddrs fetches Boliga sale listings for the given addresses
// and returns matched sales grouped by address index.
func BoligaSalesFromAddrs(addrs []*Address, progress *Progress, stats *HealthStats) ([][]BoligaSaleItem, []string, error) {
	tasks := BoligaStreetTasks(addrs)

	totalSales, failures, err := fetchStreetsLocally(tasks, progress, stats)
	warnings := failureWarnings(failures)
	if err != nil {
		return nil, warnings, err
	}

	return matchSalesToAddrs(addrs, totalSales), warnings, nil
}

// BoligaStreetTasks reduces addresses to the unique street queries Boliga
// needs. Boliga searches by street rather than by address, so a whole street of
// addresses collapses into one request — which is why a lookup issues 5-50
// requests rather than one per address.
//
// Order follows the address order rather than map iteration order, so the same
// addresses always produce the same task list. The client-side fetch path hands
// this list out and correlates results back against it, and a list that
// reshuffled per call would make that correlation non-reproducible.
func BoligaStreetTasks(addrs []*Address) []BoligaPropertyRequest {
	// Deduped on the task itself rather than on the address fields it came
	// from. Those are strings and the task holds ints, so "0101" and "101"
	// counted as two streets and then produced byte-identical requests — two
	// trips to a rate-limited Boliga for one street, and a pair acceptBoligaIngest
	// could not tell apart, since it keys pending work on the same struct.
	seen := map[BoligaPropertyRequest]bool{}
	var tasks []BoligaPropertyRequest
	for _, addr := range addrs {
		t := addrStreetTask(addr)
		if seen[t] {
			continue
		}
		seen[t] = true
		tasks = append(tasks, t)
	}

	return tasks
}

// fetchStreetsLocally runs the street queries from this process. A single
// street failing is not fatal — the remaining streets still produce a usable
// estimate — but every street failing is, since there is nothing left to value.
func fetchStreetsLocally(tasks []BoligaPropertyRequest, progress *Progress, stats *HealthStats) ([]BoligaSaleItem, []BoligaStreetFailure, error) {
	totalReqs := len(tasks)
	var completedReqs int
	var totalSales []BoligaSaleItem
	var failures []BoligaStreetFailure

	log.Printf("Fetching sales for %d streets...", totalReqs)

	for _, req := range tasks {
		progress.Update(StageBoligaList, fmt.Sprintf("Henter salgsliste %d/%d...", completedReqs+len(failures)+1, totalReqs), completedReqs+len(failures), totalReqs)
		s, err := req.Fetch()
		if err != nil {
			errType := "unknown"
			if strings.Contains(err.Error(), "status 429") {
				errType = "rate_limit"
			} else if strings.Contains(err.Error(), "status 403") {
				errType = "forbidden"
			} else if strings.Contains(err.Error(), "status 5") {
				errType = "server_error"
			}
			log.Printf("Failed to fetch %s %d: %v", req.StreetName, req.ZipCode, err)
			if stats != nil {
				stats.RecordBoligaFail(errType, fmt.Sprintf("%s %d: %v", req.StreetName, req.ZipCode, err))
			}
			failures = append(failures, BoligaStreetFailure{Task: req, Err: err})
			continue // Continue with remaining streets instead of aborting
		}
		completedReqs++
		if stats != nil {
			stats.RecordBoligaOK()
		}

		totalSales = append(totalSales, s...)
	}

	log.Printf("Completed %d/%d streets (%d sales, %d failed)", completedReqs, totalReqs, len(totalSales), len(failures))

	if completedReqs == 0 && len(failures) > 0 {
		return nil, failures, fmt.Errorf("alle %d gade-opslag fejlede", len(failures))
	}

	return totalSales, failures, nil
}

// matchSalesToAddrs attributes each sale to the address it belongs to. It is
// deliberately independent of how the sales were obtained: server-side and
// client-side fetching both land here, so valuation behaviour has one
// implementation rather than one per transport.
func matchSalesToAddrs(addrs []*Address, totalSales []BoligaSaleItem) [][]BoligaSaleItem {
	// Build lookup maps:
	// 1. Exact address string match
	// 2. Normalized string match (trim, lowercase, etc.) as fallback
	// No building-level fallback — attributing a sale to a random apartment
	// in the same building creates false duplicates.
	z := map[string]int{}
	zNorm := map[string]int{}
	// Not a third matching attempt — the no-building-fallback rule above still
	// holds. This only answers, for a sale that matched nothing, whether the
	// building it stands on was inside the radius at all.
	inRadius := map[string]bool{}
	for i, addr := range addrs {
		short := addr.Short()
		z[short] = i
		zNorm[normalizeAddr(short)] = i
		inRadius[buildingKey(short)] = true
	}

	// Two unrelated things, counted apart because only one of them is a
	// defect. Boliga is queried per street and answers with the whole street
	// while the radius covers only part of it, so a sale at a building we
	// never held is the system working: we asked for the street, we got the
	// street. A sale at a building we do hold is the opposite — something we
	// could have valued went unattributed. Summed, the first buries the second
	// and the line reads as wholesale data loss.
	var exactMatches, normMatches, skippedSaleType, outsideRadius, mismatched int
	mismatchStreets := map[string]string{}
	result := make([][]BoligaSaleItem, len(addrs))
	for i := range totalSales {
		s := totalSales[i]

		// Only include regular sales ("Alm. Salg"), skip family sales etc.
		if s.SaleType != "Alm. Salg" {
			skippedSaleType++
			continue
		}

		// Try exact match first
		if j, ok := z[s.Addr]; ok {
			exactMatches++
			result[j] = append(result[j], s)
			continue
		}

		// Fallback: normalized match (trim, collapse whitespace, strip trailing periods)
		if j, ok := zNorm[normalizeAddr(s.Addr)]; ok {
			normMatches++
			result[j] = append(result[j], s)
			continue
		}

		if !inRadius[buildingKey(s.Addr)] {
			outsideRadius++
			continue
		}

		// Dropped. Keep one example per street rather than the first few
		// overall: totalSales is assembled street by street, so a first-N
		// sample would come entirely from whichever street happened to be
		// fetched first and would say nothing about the rest.
		mismatched++
		street, _ := avSplitStreet(s.Addr)
		if _, seen := mismatchStreets[street]; !seen {
			mismatchStreets[street] = s.Addr
		}
	}
	totalMatched := exactMatches + normMatches
	log.Printf("Boliga matched %d exact + %d normalized = %d/%d sales (skipped %d non-alm. salg, %d outside the radius as expected, %d unmatched at buildings inside it)",
		exactMatches, normMatches,
		totalMatched, len(totalSales), skippedSaleType, outsideRadius, mismatched)
	if mismatched > 0 {
		examples := make([]string, 0, len(mismatchStreets))
		for _, addr := range mismatchStreets {
			examples = append(examples, addr)
		}
		sort.Strings(examples)
		log.Printf("Boliga could not match %d sales at buildings inside the radius, across %d streets, one example each: %s",
			mismatched, len(mismatchStreets), truncate(strings.Join(examples, ", "), 300))
	}

	return result
}

// classifyError returns a user-friendly Danish description of an error.
func classifyError(err error) string {
	msg := err.Error()
	// "status 429", not "429": Fetch formats failures as "<street> <zip>:
	// status <code>", so a bare "429" also matches postcodes 1429 and 4291 and
	// would report every failure on those streets as a rate limit.
	if strings.Contains(msg, "status 429") {
		return "midlertidig blokering fra Boliga"
	}
	// Deliberately not "midlertidig": a 403 is not a rate limit waiting to
	// expire, and telling the user to try again later would be wrong.
	if strings.Contains(msg, "status 403") {
		return "blokeret af Boliga"
	}
	if strings.Contains(msg, "status 5") {
		return "serverfejl hos Boliga"
	}
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline") {
		return "timeout"
	}
	return "netværksfejl"
}

// BoligaIngest is what the browser posts back after fetching. Sales are
// Boliga's own results array, verbatim — the client is a fetch relay that does
// no interpretation, so client-fetched and server-fetched sales reach the same
// matcher and cannot diverge.
type BoligaIngest struct {
	LookupID string `json:"lookup_id"`
	// Fetched holds the streets the browser got. Failed holds the ones it
	// tried and could not get; the server fetches those itself. A task in
	// neither list is treated as failed, so a client that simply stops
	// reporting still gets its streets fetched.
	Fetched []BoligaFetchResult     `json:"fetched"`
	Failed  []BoligaPropertyRequest `json:"failed"`
}

type BoligaFetchResult struct {
	Task  BoligaPropertyRequest `json:"task"`
	Sales []BoligaSaleItem      `json:"sales"`
}

type BoligaSalesResponse struct {
	Meta  BoligaPageCrawl  `json:"meta"`
	Sales []BoligaSaleItem `json:"results"`
	Err   error
}

// BoligaPropertyRequest is one street query. It doubles as the wire format for
// a client-side fetch task, hence the JSON tags.
type BoligaPropertyRequest struct {
	StreetName     string `json:"street"`
	ZipCode        int    `json:"zipcode"`
	MunicipalityID int    `json:"municipality"`
}

// A var, not a const, so tests can point the fetch at an httptest server —
// the same seam ADRESSEVAELGER_URL gives adressevaelger.go. Note this is a
// different host from boligaBaseUrl above.
var boligaSoldSearchURL = "https://api.boliga.dk/api/v2/sold/search/results"

// Boliga defaults to 50 sales per page and honours `pagesize` up to at least
// 2000. Since it answers per street rather than per address, a street query is
// the whole street: Nørrebrogade 2200 is 401 sales, nine requests at the
// default and one at this size. Requests — not bytes — are what the lookup
// pays for, because the host allows only about five of them every eleven
// seconds (hostRequestGap in http.go), so collapsing nine into one is worth
// far more than any pacing of the nine.
//
// 500 rather than the maximum: it clears all but the densest arterial streets
// in a single request, and the browser relay has to post every sale it
// receives back to the server, where the ingest is capped at 16 MB.
const boligaPageSize = 500

func (r BoligaPropertyRequest) Fetch() ([]BoligaSaleItem, error) {
	req, err := http.NewRequest("GET", boligaSoldSearchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	q := req.URL.Query()
	q.Add("searchTab", "1")
	q.Add("sort", "date-a")
	q.Add("pagesize", strconv.Itoa(boligaPageSize))

	if r.ZipCode > 0 {
		q.Add("zipcodeFrom", strconv.Itoa(r.ZipCode))
		q.Add("zipcodeTo", strconv.Itoa(r.ZipCode))
	}

	if r.StreetName != "" {
		q.Add("street", r.StreetName)
	}

	if r.MunicipalityID != 0 {
		q.Add("municipality", strconv.Itoa(r.MunicipalityID))
	}

	page := 1
	maxPages := 99999

	var sales []BoligaSaleItem
	for {
		q.Set("page", strconv.Itoa(page))
		req.URL.RawQuery = q.Encode()

		var sr BoligaSalesResponse
		resp, err := DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%s %d: %v", r.StreetName, r.ZipCode, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s %d: status %d", r.StreetName, r.ZipCode, resp.StatusCode)
		}

		if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
			return nil, fmt.Errorf("%s %d: decode error: %v", r.StreetName, r.ZipCode, err)
		}
		sales = append(sales, sr.Sales...)
		maxPages = sr.Meta.TotalPages
		if page >= maxPages {
			break
		}

		page += 1
	}

	return sales, nil
}

var (
	numbersOnlyRegexp = regexp.MustCompile(`^[0-9]+`)
)

func DirtyStringToInt(s string) (int, error) {
	s = strings.TrimSpace(s)
	s = strings.Replace(s, ".", "", -1)
	matches := numbersOnlyRegexp.FindAllString(s, 1)
	if len(matches) == 0 {
		return 0, &strconv.NumError{
			Func: "DirtyStringToInt",
			Num:  s,
			Err:  strconv.ErrSyntax,
		}
	}

	return strconv.Atoi(matches[0])
}

var (
	daToEn = map[string]string{
		"feb": "Feb",
		"mar": "Mar",
		"apr": "Apr",
		"maj": "May",
		"jun": "Jun",
		"jul": "Jul",
		"aug": "Aug",
		"sep": "Sep",
		"okt": "Oct",
		"nov": "Nov",
		"dec": "Dec",
	}
)

func DanishDateToTime(format string, s string) (time.Time, error) {
	clean := func(s string) string {
		s = strings.TrimSpace(s)
		s = strings.Replace(s, ".", "", -1)
		s = strings.Replace(s, "jan", "Jan", -1)
		return s
	}

	s = clean(s)
	format = clean(format)

	for from, to := range daToEn {
		if strings.Contains(s, from) {
			s = strings.Replace(s, from, to, -1)
			break
		}
	}

	return time.Parse(format, s)
}

var whitespaceRun = regexp.MustCompile(`\s+`)

// normalizeAddr collapses whitespace, trims, lowercases, and strips
// trailing periods to handle format variations between DAWA and Boliga.
func normalizeAddr(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	s = whitespaceRun.ReplaceAllString(s, " ")
	s = strings.TrimRight(s, ".")
	// Normalize ", " vs "," (Boliga sometimes omits the space)
	s = strings.ReplaceAll(s, ",", ", ")
	s = whitespaceRun.ReplaceAllString(s, " ")
	return s
}

// buildingKey reduces an address to the street and house number it stands on,
// dropping any floor and door. A radius query returns every unit at a point
// together, so a floor or a door can never be the reason an address lies
// outside the radius while a house number further up the street routinely is.
// Keying on the full address instead would make every unmatched flat look
// geographically excluded.
func buildingKey(addr string) string {
	street, rest := avSplitStreet(addr)
	number := rest
	if i := strings.IndexAny(rest, ", "); i >= 0 {
		number = rest[:i]
	}
	return normalizeAddr(street + " " + number)
}

func FilterAddressesByProperty(pt PropertyType, addrs []*Address, sales [][]Sale) ([]*Address, [][]Sale) {
	// If the primary address has no known property type, skip filtering entirely
	// — we can't meaningfully filter without knowing what type we're comparing against
	if pt == 0 {
		log.Printf("Property type filter: skipped (primary has unknown type)")
		return addrs, sales
	}

	var oAddrs []*Address
	var oSales [][]Sale
	var kept, filtered int

	for i := range addrs {
		a, s := addrs[i], sales[i]
		// Always keep the primary address (index 0) and addresses matching the type
		if i == 0 || a.BoligaPropertyKind == pt || a.BoligaPropertyKind == 0 {
			oAddrs = append(oAddrs, a)
			oSales = append(oSales, s)
			kept++
		} else {
			filtered++
		}
	}

	log.Printf("Property type filter: kept %d, filtered %d (type=%d)", kept, filtered, pt)
	return oAddrs, oSales
}
