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

// BoligaStreetFetcher obtains the sales for a list of street queries. The
// server has two: fetchStreetsLocally, which issues the requests itself, and
// the session-aware one in api.go, which asks the browser to issue them and
// falls back to the local one for whatever the browser could not get.
//
// Only the fetching varies. Planning the street list and matching sales back
// to addresses are shared, so valuation behaviour cannot drift between the two
// paths.
type BoligaStreetFetcher func([]BoligaPropertyRequest, *Progress, *HealthStats) ([]BoligaSaleItem, []string, error)

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
		totalSales, fetchWarnings, err := fetch(tasks, progress, stats)
		warnings = fetchWarnings
		if err != nil {
			return nil, warnings, err
		}
		matched := matchSalesToAddrs(addrsToFetch, totalSales)

		var salesToStore []Sale
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

			if err := bc.db.Save(&addr).Error; err != nil {
				return nil, warnings, err
			}
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
				bc.db.Save(addr)
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

type Sale struct {
	AddrID    uint      `json:"-"`
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

	totalSales, warnings, err := fetchStreetsLocally(tasks, progress, stats)
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
	type K struct {
		Municipality string
		Street       string
		ZipCode      string
	}

	seen := map[K]bool{}
	var tasks []BoligaPropertyRequest
	for _, addr := range addrs {
		k := K{
			Municipality: addr.MunicipalityCode,
			Street:       addr.StreetName,
			ZipCode:      addr.PostalCode,
		}
		if seen[k] {
			continue
		}
		seen[k] = true

		mun, _ := strconv.Atoi(addr.MunicipalityCode)
		zip, _ := strconv.Atoi(addr.PostalCode)
		tasks = append(tasks, BoligaPropertyRequest{
			StreetName:     addr.StreetName,
			ZipCode:        zip,
			MunicipalityID: mun,
		})
	}

	return tasks
}

// fetchStreetsLocally runs the street queries from this process. A single
// street failing is not fatal — the remaining streets still produce a usable
// estimate — but every street failing is, since there is nothing left to value.
func fetchStreetsLocally(tasks []BoligaPropertyRequest, progress *Progress, stats *HealthStats) ([]BoligaSaleItem, []string, error) {
	totalReqs := len(tasks)
	var completedReqs, failedReqs int
	var totalSales []BoligaSaleItem
	var warnings []string

	log.Printf("Fetching sales for %d streets...", totalReqs)

	for _, req := range tasks {
		progress.Update(StageBoligaList, fmt.Sprintf("Henter salgsliste %d/%d...", completedReqs+failedReqs+1, totalReqs), completedReqs+failedReqs, totalReqs)
		s, err := req.Fetch()
		if err != nil {
			failedReqs++
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
			warnings = append(warnings, fmt.Sprintf("Kunne ikke hente salg for %s (%s)", req.StreetName, classifyError(err)))
			continue // Continue with remaining streets instead of aborting
		}
		completedReqs++
		if stats != nil {
			stats.RecordBoligaOK()
		}

		totalSales = append(totalSales, s...)
	}

	log.Printf("Completed %d/%d streets (%d sales, %d failed)", completedReqs, totalReqs, len(totalSales), failedReqs)

	if completedReqs == 0 && failedReqs > 0 {
		return nil, warnings, fmt.Errorf("alle %d gade-opslag fejlede", failedReqs)
	}

	return totalSales, warnings, nil
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
	for i, addr := range addrs {
		short := addr.Short()
		z[short] = i
		zNorm[normalizeAddr(short)] = i
	}

	var exactMatches, normMatches, skippedSaleType, missed int
	missedStreets := map[string]string{}
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

		// Dropped. Keep one example per street rather than the first few
		// overall: totalSales is assembled street by street, so a first-N
		// sample would come entirely from whichever street happened to be
		// fetched first and would say nothing about the rest.
		missed++
		street, _ := avSplitStreet(s.Addr)
		if _, seen := missedStreets[street]; !seen {
			missedStreets[street] = s.Addr
		}
	}
	totalMatched := exactMatches + normMatches
	log.Printf("Boliga matched %d exact + %d normalized = %d/%d sales (skipped %d non-alm. salg, %d unmatched)",
		exactMatches, normMatches,
		totalMatched, len(totalSales), skippedSaleType, missed)
	if missed > 0 {
		examples := make([]string, 0, len(missedStreets))
		for _, addr := range missedStreets {
			examples = append(examples, addr)
		}
		sort.Strings(examples)
		log.Printf("Boliga unmatched %d sales across %d streets, one example each: %s",
			missed, len(missedStreets), truncate(strings.Join(examples, ", "), 300))
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

func (r BoligaPropertyRequest) Fetch() ([]BoligaSaleItem, error) {
	req, err := http.NewRequest("GET", boligaSoldSearchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	q := req.URL.Query()
	q.Add("searchTab", "1")
	q.Add("sort", "date-a")

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
