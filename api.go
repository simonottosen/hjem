package hjem

import (
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"gorm.io/gorm"
)

const maxBytesLimit = 1024 * 1024 // 1mb

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
		progress: NewProgress(),
	}
}

type server struct {
	dc       DawaCacher
	bc       BoligaCacher
	progress *Progress
}

func (s *server) handleLookup() http.HandlerFunc {
	type Request struct {
		Query  string `json:"q"`
		Ranges []int  `json:"ranges"`
		Filter int    `json:"filter_below_std"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		s.progress.Reset()

		var req Request
		body := http.MaxBytesReader(w, r.Body, maxBytesLimit)
		defer body.Close()

		err := json.NewDecoder(body).Decode(&req)
		if err != nil {
			s.progress.Update(StageError, err.Error(), 0, 0)
			replyJSONErr(w, err, http.StatusBadRequest)
			return
		}

		s.progress.Update(StageDawa, "Søger adresse...", 0, 0)
		addrs, err := s.dc.Do(DawaFuzzySearch{
			Query: req.Query,
		})
		if err != nil {
			s.progress.Update(StageError, err.Error(), 0, 0)
			replyJSONErr(w, err, http.StatusBadRequest)
			return
		}

		if len(addrs) > 1 {
			s.progress.Update(StageError, "non-unique address", 0, 0)
			replyJSONErr(w, fmt.Errorf("non-unique address, be more specific"), http.StatusBadRequest)
			return
		}

		if len(addrs) == 0 {
			s.progress.Update(StageError, "no found address", 0, 0)
			replyJSONErr(w, fmt.Errorf("no found address"), http.StatusBadRequest)
			return
		}
		addr := addrs[0]

		s.progress.Update(StageDawa, "Henter nærliggende adresser...", 0, 0)
		ranges, err := s.constructRanges(addr, req.Ranges)
		if err != nil {
			s.progress.Update(StageError, err.Error(), 0, 0)
			replyJSONErr(w, err, http.StatusBadRequest)
			return
		}

		for _, addrsInRange := range ranges {
			addrs = append(addrs, addrsInRange...)
		}

		s.progress.Update(StageBoligaList, "Henter salgslister fra Boliga...", 0, 0)
		sales, err := s.bc.FetchSales(addrs, s.progress)
		if err != nil {
			s.progress.Update(StageError, err.Error(), 0, 0)
			replyJSONErr(w, err, http.StatusBadRequest)
			return
		}

		addrs, sales = FilterAddressesByProperty(addr.BoligaPropertyKind, addrs, sales)

		s.progress.Update(StageDone, "Færdig!", 0, 0)

		luResp, err := FormatLookupResponse(addrs, ranges, sales, req.Filter)
		if err != nil {
			s.progress.Update(StageError, err.Error(), 0, 0)
			replyJSONErr(w, err, http.StatusBadRequest)
			return
		}

		replyJSON(w, luResp, http.StatusOK)
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

		addrs, err := s.dc.Do(DawaFuzzySearch{
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

		sales, err := s.bc.FetchSales(addrs, nil)
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
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Send initial snapshot
		fmt.Fprintf(w, "data: %s\n\n", s.progress.SnapshotJSON())
		flusher.Flush()

		for {
			select {
			case <-s.progress.Wait():
				snap := s.progress.Snapshot()
				fmt.Fprintf(w, "data: %s\n\n", s.progress.SnapshotJSON())
				flusher.Flush()

				if snap.Stage == StageDone || snap.Stage == StageError {
					return
				}
			case <-r.Context().Done():
				return
			}
		}
	}
}

//go:embed frontend/dist/index.html
var indexBytes []byte

func (s *server) handleIndex() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Write(indexBytes)
	}
}

//go:embed frontend/dist/app.bundle.js
var bundleBytes []byte

func (s *server) handleBundle() http.HandlerFunc {

	return func(w http.ResponseWriter, r *http.Request) {
		w.Write(bundleBytes)
	}
}

func (s *server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex())
	mux.HandleFunc("/dist/app.bundle.js", s.handleBundle())
	mux.HandleFunc("/api/lookup", s.handleLookup())
	mux.HandleFunc("/api/progress", s.handleProgress())
	mux.HandleFunc("/download/csv", s.handleCSVDownload())

	return mux
}

func (s *server) constructRanges(addr *Address, nearby []int) (map[int][]*Address, error) {
	o := make(map[int][]*Address)
	for _, r := range nearby {
		addrs, err := s.dc.Do(DawaNearbySearch{
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
	PrimaryIndex int               `json:"primary_idx"`
	Addrs        []*Address        `json:"addresses"`
	Sales        []*JSONSale       `json:"sales"`
	Ranges       map[int][]int     `json:"ranges,omitempty"`
	SquareMeters SquareMeterPrices `json:"sqmeters"`
}

type JSONSale struct {
	AddrIndex int       `json:"addr_idx"`
	Amount    int       `json:"amount"`
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

func FormatLookupResponse(addrs []*Address, ranges map[int][]*Address, sales [][]Sale, stdf int) (*LookupResponse, error) {
	m := map[string]int{}
	var resp LookupResponse

	var i int
	for j, s := range sales {
		if len(s) > 0 {
			a := addrs[j]
			m[a.DawaID] = i
			resp.Addrs = append(resp.Addrs, a)

			tempsales := make([]*JSONSale, len(s))
			for k, sale := range s {
				tempsales[k] = &JSONSale{
					AddrIndex: i,
					Amount:    sale.AmountDKK,
					When:      sale.Date,
				}
			}
			resp.Sales = append(resp.Sales, tempsales...)

			i += 1
		}
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

	normalSales, global := SalesStatistics(resp.Addrs, resp.Sales, stdf)
	resp.Sales = normalSales

	resp.SquareMeters = SquareMeterPrices{
		Global: global,
	}

	var projections []map[time.Time]int
	primaryBuildingSize := addrs[0].BoligaBuildingSize
	if primaryBuildingSize > 0 {
		for _, s := range sales[resp.PrimaryIndex] {
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

	return &resp, nil
}
