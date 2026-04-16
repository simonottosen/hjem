package hjem

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
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
	FetchSales([]*Address, *Progress, *HealthStats) ([][]Sale, []string, error)
}

type boligaCacher struct {
	db *gorm.DB
}

func NewBoligaCacher(db *gorm.DB) *boligaCacher {
	db.AutoMigrate(&Sale{})
	return &boligaCacher{db}
}

const cacheExpiry time.Duration = time.Hour * 24 * 10 // 10 days

func (bc *boligaCacher) FetchSales(addrs []*Address, progress *Progress, stats *HealthStats) ([][]Sale, []string, error) {
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

		matched, fetchWarnings, err := BoligaSalesFromAddrs(addrsToFetch, progress, stats)
		warnings = fetchWarnings
		if err != nil {
			return nil, warnings, err
		}

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
	type K struct {
		Municipality string
		Street       string
		ZipCode      string
	}

	m := map[K]*BoligaPropertyRequest{}
	for _, addr := range addrs {
		k := K{
			Municipality: addr.MunicipalityCode,
			Street:       addr.StreetName,
			ZipCode:      addr.PostalCode,
		}

		if _, ok := m[k]; !ok {
			mun, _ := strconv.Atoi(addr.MunicipalityCode)
			zip, _ := strconv.Atoi(addr.PostalCode)
			m[k] = &BoligaPropertyRequest{
				StreetName:     addr.StreetName,
				ZipCode:        zip,
				MunicipalityID: mun,
			}
		}
	}

	totalReqs := len(m)
	var completedReqs, failedReqs int
	var totalSales []BoligaSaleItem
	var warnings []string

	log.Printf("Fetching sales for %d streets...", totalReqs)

	for _, req := range m {
		progress.Update(StageBoligaList, fmt.Sprintf("Henter salgsliste %d/%d...", completedReqs+failedReqs+1, totalReqs), completedReqs+failedReqs, totalReqs)
		s, err := req.Fetch()
		if err != nil {
			failedReqs++
			errType := "unknown"
			if strings.Contains(err.Error(), "429") {
				errType = "rate_limit"
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

	var exactMatches, normMatches, skippedSaleType int
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
	}
	totalMatched := exactMatches + normMatches
	log.Printf("Boliga matched %d exact + %d normalized = %d/%d sales (skipped %d non-alm. salg)",
		exactMatches, normMatches,
		totalMatched, len(totalSales), skippedSaleType)

	return result, warnings, nil
}

// classifyError returns a user-friendly Danish description of an error.
func classifyError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "429") {
		return "midlertidig blokering fra Boliga"
	}
	if strings.Contains(msg, "status 5") {
		return "serverfejl hos Boliga"
	}
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline") {
		return "timeout"
	}
	return "netværksfejl"
}

type BoligaSalesResponse struct {
	Meta  BoligaPageCrawl  `json:"meta"`
	Sales []BoligaSaleItem `json:"results"`
	Err   error
}

type BoligaPropertyRequest struct {
	StreetName     string
	ZipCode        int
	MunicipalityID int
}

func (r BoligaPropertyRequest) Fetch() ([]BoligaSaleItem, error) {
	endpoint := "https://api.boliga.dk/api/v2/sold/search/results"
	req, err := http.NewRequest("GET", endpoint, nil)
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
