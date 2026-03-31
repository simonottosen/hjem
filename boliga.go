package hjem

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
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

type BoligaCacherTask struct {
	ctx   context.Context
	index int
	item  BoligaSaleItem
	out   chan<- BoligaCacherResp
}
type BoligaCacherResp struct {
	index int
	prop  *BoligaProperty
	err   error
}

type BoligaCacher interface {
	io.Closer
	FetchSales([]*Address, *Progress) ([][]Sale, error)
}

type boligaCacher struct {
	db *gorm.DB
	in chan BoligaCacherTask
}

func NewBoligaCacher(db *gorm.DB, n int) *boligaCacher {
	in := make(chan BoligaCacherTask)

	for i := 0; i < n; i++ {
		go func() {
			for task := range in {
				prop, err := PropertyFromBoligaItem(task.item)
				task.out <- BoligaCacherResp{task.index, prop, err}
			}
		}()
	}

	db.AutoMigrate(&Sale{})

	return &boligaCacher{db, in}
}

func (bc *boligaCacher) Close() error {
	close(bc.in)
	return nil
}

const oneMonth time.Duration = time.Hour * 24 * 31

func (bc *boligaCacher) FetchSales(addrs []*Address, progress *Progress) ([][]Sale, error) {
	cachedAddrs := map[int]*Address{}
	fetchAddrs := map[int]*Address{}
	var salesExpired []uint

	for i, addr := range addrs {
		if addr.BoligaCollectedAt.IsZero() {
			fetchAddrs[i] = addr
			continue
		}

		if time.Now().Sub(addr.BoligaCollectedAt) >= oneMonth {
			fetchAddrs[i] = addr
			salesExpired = append(salesExpired, addr.ID)
			continue
		}

		cachedAddrs[i] = addr
	}

	if len(salesExpired) > 0 {
		bc.db.Delete(&Sale{}, salesExpired)
	}

	sales := make([][]Sale, len(addrs))
	if len(fetchAddrs) > 0 {
		fetchTime := time.Now()
		addrsToFetch := make([]*Address, len(fetchAddrs))
		addrIds := make([]uint, len(fetchAddrs))
		ids := make([]int, len(fetchAddrs))
		var i int
		for id, addr := range fetchAddrs {
			addrIds[i] = addr.ID
			addrsToFetch[i] = addr
			ids[i] = id
			i += 1
		}

		items, err := BoligaPropertiesFromAddrs(addrsToFetch, progress)
		if err != nil {
			return nil, err
		}

		var totalProps int
		for _, item := range items {
			if item != nil {
				totalProps++
			}
		}
		var completedProps int
		progress.Update(StageBoligaProp, fmt.Sprintf("Henter boligdetaljer: %d/%d", 0, totalProps), 0, totalProps)

		var wg sync.WaitGroup
		out := make(chan BoligaCacherResp)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go func() {
			defer close(out)

			for i, item := range items {
				if item == nil {
					continue
				}

				if ctx.Err() != nil {
					break
				}

				wg.Add(1)
				bc.in <- BoligaCacherTask{
					item:  *item,
					index: ids[i],
					out:   out,
				}
			}

			wg.Wait()
		}()

		var salesToStore []Sale
		var rerr error
		for resp := range out {
			wg.Done()
			completedProps++
			progress.Update(StageBoligaProp, fmt.Sprintf("Henter boligdetaljer: %d/%d", completedProps, totalProps), completedProps, totalProps)
			if err := resp.err; err != nil {
				rerr = err
				cancel()
				continue
			}

			psales := make([]Sale, len(resp.prop.Sales))
			addr := addrs[resp.index]
			for i, sale := range resp.prop.Sales {
				sale.AddrID = addr.ID
				psales[i] = sale
			}

			sales[resp.index] = psales
			salesToStore = append(salesToStore, psales...)

			addr.BoligaCollectedAt = fetchTime
			addr.BoligaBuiltYear = resp.prop.BuiltYear
			addr.BoligaBasementSize = resp.prop.BasementSize
			addr.BoligaBuildingSize = resp.prop.BuildingSize
			addr.BoligaRooms = resp.prop.Rooms
			addr.BoligaPropertySize = resp.prop.PropertySize
			addr.BoligaMonthlyOwnerExpense = resp.prop.MonthlyOwnerExpense
			addr.BoligaEnergyMarking = resp.prop.EnergyMarking
			addr.BoligaPropertyKind = resp.prop.Kind

			if err := bc.db.Save(&addr).Error; err != nil {
				return nil, err
			}

		}

		if rerr != nil {
			return nil, rerr
		}

		if err := bc.db.CreateInBatches(&salesToStore, 50).Error; err != nil {
			return nil, err
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

	return sales, nil
}

type Sale struct {
	AddrID    uint      `json:"-"`
	AmountDKK int       `json:"amount"`
	Date      time.Time `json:"time"`
}

type BoligaProperty struct {
	Kind                PropertyType
	BuildingSize        int
	PropertySize        int
	BasementSize        int
	Rooms               int
	BuiltYear           int
	MonthlyOwnerExpense int
	EnergyMarking       string
	Sales               []Sale
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

func BoligaPropertiesFromAddrs(addrs []*Address, progress *Progress) ([]*BoligaSaleItem, error) {
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
	var completedReqs int
	var totalSales []BoligaSaleItem
	for _, req := range m {
		progress.Update(StageBoligaList, fmt.Sprintf("Henter salgsliste %d/%d...", completedReqs+1, totalReqs), completedReqs, totalReqs)
		s, err := req.Fetch()
		if err != nil {
			return nil, err
		}
		completedReqs++

		totalSales = append(totalSales, s...)
	}
	log.Printf("Boliga total sales fetched: %d", len(totalSales))

	z := map[string]int{}
	for i, addr := range addrs {
		z[addr.Short()] = i
	}

	log.Printf("Debug: z map has %d entries, totalSales has %d entries", len(z), len(totalSales))

	// Find a Boliga address where DAWA also has the same street+number
	if len(totalSales) > 0 {
		for _, s := range totalSales {
			parts := strings.SplitN(s.Addr, ",", 2)
			if len(parts) == 0 {
				continue
			}
			streetNum := strings.TrimSpace(parts[0])
			for key := range z {
				if strings.HasPrefix(key, streetNum) {
					log.Printf("CLOSE PAIR found:")
					log.Printf("  Boliga: %q (bytes: %x)", s.Addr, []byte(s.Addr))
					log.Printf("  DAWA:   %q (bytes: %x)", key, []byte(key))
					log.Printf("  Equal: %v", key == s.Addr)
					goto debugDone
				}
			}
		}
		log.Printf("NO close pairs found at all! Dumping street+num combos:")
		dawaStreetNums := map[string]bool{}
		for key := range z {
			parts := strings.SplitN(key, ",", 2)
			if len(parts) > 0 {
				dawaStreetNums[strings.TrimSpace(parts[0])] = true
			}
		}
		boligaStreetNums := map[string]bool{}
		for _, s := range totalSales {
			parts := strings.SplitN(s.Addr, ",", 2)
			if len(parts) > 0 {
				boligaStreetNums[strings.TrimSpace(parts[0])] = true
			}
		}
		log.Printf("  Sample DAWA street+nums (first 10):")
		var dc int
		for sn := range dawaStreetNums {
			if dc >= 10 {
				break
			}
			log.Printf("    %q", sn)
			dc++
		}
		log.Printf("  Sample Boliga street+nums (first 10):")
		var bc int
		for sn := range boligaStreetNums {
			if bc >= 10 {
				break
			}
			log.Printf("    %q", sn)
			bc++
		}
		// Check overlap
		var overlap int
		for sn := range boligaStreetNums {
			if dawaStreetNums[sn] {
				overlap++
			}
		}
		log.Printf("  Street+num overlap: %d", overlap)
	}
debugDone:

	var matches int

	props := make([]*BoligaSaleItem, len(addrs))
	for i, _ := range totalSales {
		s := totalSales[i]
		j, ok := z[s.Addr]
		if ok {
			matches += 1
			props[j] = &s
		}
	}
	log.Printf("Boliga matched %d/%d sales to addresses", matches, len(totalSales))

	return props, nil
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

		log.Printf("Fetching Boliga sales: %s (page %d)", req.URL, page)
		var sr BoligaSalesResponse
		resp, err := DefaultClient.Do(req)
		if err != nil {
			log.Printf("Boliga request failed: %v", err)
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			log.Printf("Boliga returned status %d", resp.StatusCode)
			return nil, fmt.Errorf("boliga API returned status %d", resp.StatusCode)
		}

		if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
			log.Printf("Boliga decode failed: %v", err)
			return nil, err
		}

		log.Printf("Boliga page %d/%d: %d sales", page, sr.Meta.TotalPages, len(sr.Sales))
		sales = append(sales, sr.Sales...)
		maxPages = sr.Meta.TotalPages
		if page >= maxPages {
			break
		}

		page += 1
	}

	return sales, nil
}

func PropertyFromBoligaItem(si BoligaSaleItem) (*BoligaProperty, error) {
	query := fmt.Sprintf("https://www.boliga.dk/salg/info/%d/%d/%s",
		si.MunicipalityCode,
		si.EstateCode,
		si.Guid,
	)
	log.Printf("Fetching Boliga property: %s", query)
	resp, err := DefaultClient.Get(query)
	if err != nil {
		log.Printf("Boliga property request failed: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Boliga property page returned status %d", resp.StatusCode)
		return nil, fmt.Errorf("boliga property page returned status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	prop := BoligaProperty{
		Kind:         si.PropertyType,
		BuiltYear:    si.BuildYear,
		BuildingSize: si.SqMeters,
		Rooms:        int(si.Rooms),
	}
	path, ok := doc.Find(".sales-overview-table.h-100 .table-row").Find("a").Attr("href")
	if ok {
		query = "https://www.boliga.dk" + path
		resp, err = DefaultClient.Get(query)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("boliga listing page returned status %d", resp.StatusCode)
		}

		err := ReadListingToProperty(resp.Body, &prop)
		if err != nil {
			return nil, err
		}
	}

	uniqueSales := map[Sale]struct{}{}
	doc.Find(".sales-overview-table").First().Find("tbody tr").Each(func(i int, s *goquery.Selection) {
		cols := s.Find("td")
		kind := strings.TrimSpace(cols.Eq(3).Find("span").Eq(1).Text())
		if strings.Contains(kind, "frit salg") {
			amount, _ := DirtyStringToInt(cols.Eq(1).Find("span").Eq(1).Text())
			timestr := cols.Eq(2).Find("span").Eq(1).Text()

			saleDate, _ := DanishDateToTime("2. jan. 2006", timestr)
			uniqueSales[Sale{
				AmountDKK: amount,
				Date:      saleDate,
			}] = struct{}{}
		}
	})

	for sale, _ := range uniqueSales {
		prop.Sales = append(prop.Sales, sale)
	}

	return &prop, nil
}

func ReadListingToProperty(reader io.ReadCloser, prop *BoligaProperty) error {
	doc, err := goquery.NewDocumentFromReader(reader)
	if err != nil {
		return err
	}
	defer reader.Close()

	doc.Find("app-property-detail").Each(func(i int, s *goquery.Selection) {
		spans := s.Find("span")
		detail := strings.TrimSpace(spans.Eq(0).Text())
		detail = strings.ToLower(detail)
		detail = strings.TrimSuffix(detail, ":")
		value := strings.TrimSpace(spans.Eq(1).Text())

		switch detail {
		case "boligstørrelse":
			v, err := DirtyStringToInt(value)
			if err != nil {
				return
			}
			prop.BuildingSize = v
		case "byggeår":
			v, err := DirtyStringToInt(value)
			if err != nil {
				return
			}
			prop.BuiltYear = v
		case "værelser":
			v, err := DirtyStringToInt(value)
			if err != nil {
				return
			}
			prop.Rooms = v
		case "ejerudgift":
			v, err := DirtyStringToInt(value)
			if err != nil {
				return
			}
			prop.MonthlyOwnerExpense = v
		case "grundstørrelse":
			v, err := DirtyStringToInt(value)
			if err != nil {
				return
			}
			prop.PropertySize = v
		case "energimærke":
			if value == "0" {
				return
			}
			prop.EnergyMarking = strings.ToLower(value)
		case "kælderstørrelse":
			v, err := DirtyStringToInt(value)
			if err != nil {
				return
			}

			prop.BasementSize = v

		}
	})

	return nil
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

func FilterAddressesByProperty(pt PropertyType, addrs []*Address, sales [][]Sale) ([]*Address, [][]Sale) {
	var oAddrs []*Address
	var oSales [][]Sale

	for i, _ := range addrs {
		a, s := addrs[i], sales[i]
		if a.BoligaPropertyKind == pt {
			oAddrs = append(oAddrs, a)
			oSales = append(oSales, s)
		}
	}

	return oAddrs, oSales
}
