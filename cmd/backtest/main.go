// Command backtest measures how wrong the valuation is, by re-running it
// against sales it was not allowed to see.
//
//	go run ./cmd/backtest -db hjem.db -radius 250
//
// Method: every cached sale is held out in turn. For the address that sold,
// the command rebuilds the neighbour set from the database and hands
// FormatLookupResponse only the sales that closed strictly before the hold-out
// date, then compares the estimate that comes back against the price the home
// actually fetched. Going through FormatLookupResponse rather than calling
// ComputeCompsEstimate directly is the point: whole-building filtering, IQR
// outlier removal and the yearly aggregation that comps market-adjusts against
// all live there, so a backtest that reimplemented the assembly would be
// scoring a model the server does not run. The square-metre estimate is scored
// on the same hold-outs, because "is the weighted one better than the average"
// is the first question anyone asks of these numbers.
//
// What it does not establish, in rough order of how much it should temper the
// result:
//
//   - Neighbours are whatever the database happens to hold, which is the union
//     of lookups someone already ran, not a radius search. A real lookup calls
//     DAR and can see addresses this never will, so every comp set here is a
//     subset of production's and the measured error is an upper bound on a
//     warm-cache lookup. Needing no API key is the trade, and it is what makes
//     the number reproducible by anyone with the database.
//
//   - Comp weights decay from time.Now(), not from the hold-out date, and
//     ComputeCompsEstimate takes no clock to override. Every weight in a
//     hold-out N years old is therefore scaled by exp(-timeLambda·N). That
//     factor is common to all comps, so the point estimate survives it — it is
//     a ratio of weighted sums — but totalWeight does not, and the >3.0/>1.5
//     confidence thresholds are absolute. Older hold-outs are pushed towards
//     "low" for reasons that have nothing to do with the data, which makes the
//     confidence table a floor on calibration rather than a reading of it. The
//     narrower -since is, the less this bites.
//
//   - Size, room count and build year are the values Boliga reports for an
//     address today, applied to a sale that may be much older. They are not
//     price information, so this is not leakage, but a home extended since the
//     sale is scored against the wrong size.
//
//   - Only sales Boliga calls "Alm. Salg" are stored at all. The error here is
//     the error on ordinary arm's-length transactions; it says nothing about
//     the homes that never change hands.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"sort"
	"time"

	hjem "github.com/tpanum/hjem"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func main() {
	dbFile := flag.String("db", "hjem.db", "SQLite database to read; point this at a copy, since opening it runs GORM's migrations")
	radius := flag.Int("radius", 250, "neighbour radius in metres")
	filter := flag.Int("filter", 1, "outlier filter strength, as the frontend's filter control sends it")
	since := flag.String("since", "2015-01-01", "hold out only sales on or after this date")
	flag.Parse()

	from, err := time.Parse("2006-01-02", *since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "-since %q is not a YYYY-MM-DD date: %v\n", *since, err)
		os.Exit(1)
	}

	db, err := gorm.Open(sqlite.Open(*dbFile), &gorm.Config{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open %s: %v\n", *dbFile, err)
		os.Exit(1)
	}
	if err := db.AutoMigrate(&hjem.Address{}, &hjem.Sale{}); err != nil {
		fmt.Fprintf(os.Stderr, "cannot migrate %s: %v\n", *dbFile, err)
		os.Exit(1)
	}

	var addrs []*hjem.Address
	var sales []hjem.Sale
	if err := db.Find(&addrs).Error; err != nil {
		fmt.Fprintf(os.Stderr, "cannot read addresses: %v\n", err)
		os.Exit(1)
	}
	if err := db.Find(&sales).Error; err != nil {
		fmt.Fprintf(os.Stderr, "cannot read sales: %v\n", err)
		os.Exit(1)
	}
	if len(sales) == 0 {
		fmt.Fprintf(os.Stderr, "%s holds no sales. A backtest reads an already-populated database; run lookups against this file first.\n", *dbFile)
		os.Exit(1)
	}

	byAddr := map[uint][]hjem.Sale{}
	for _, s := range sales {
		byAddr[s.AddrID] = append(byAddr[s.AddrID], s)
	}

	// FormatLookupResponse logs a line or two per call for an operator watching
	// one lookup. Several hundred of them would bury the report.
	log.SetOutput(io.Discard)

	var results []result
	for _, subject := range addrs {
		for _, held := range byAddr[subject.ID] {
			// Without a size there is nothing to multiply a square-metre price
			// by, so the server declines to value the home and so does this.
			if subject.BoligaBuildingSize == 0 || held.AmountDKK <= 0 || held.Date.Before(from) {
				continue
			}
			results = append(results, evaluate(subject, addrs, byAddr, held, float64(*radius)/1000, *filter))
		}
	}

	fmt.Printf("hold-outs: %d sales on or after %s, radius %dm, filter %d\n", len(results), *since, *radius, *filter)
	report(results)
}

type result struct {
	hasComps   bool
	compsErr   float64 // signed, percent
	inBand     bool    // actual price fell inside the published [low, high]
	confidence string
	hasSqm     bool
	sqmErr     float64 // signed, percent
}

// evaluate values one home as of the moment before it sold.
func evaluate(subject *hjem.Address, all []*hjem.Address, byAddr map[uint][]hjem.Sale, held hjem.Sale, radiusKm float64, filter int) result {
	addrs := []*hjem.Address{subject}
	sales := [][]hjem.Sale{known(byAddr[subject.ID], held.Date)}
	for _, a := range all {
		if a.ID == subject.ID || distanceKm(subject, a) > radiusKm {
			continue
		}
		addrs = append(addrs, a)
		sales = append(sales, known(byAddr[a.ID], held.Date))
	}

	addrs, sales = hjem.FilterAddressesByProperty(subject.BoligaPropertyKind, addrs, sales)
	resp, _ := hjem.FormatLookupResponse(addrs, nil, sales, filter)

	actual := float64(held.AmountDKK)
	var r result
	if c := resp.CompsEstimate; c != nil {
		r.hasComps = true
		r.compsErr = (float64(c.Value) - actual) / actual * 100
		r.inBand = actual >= float64(c.Low) && actual <= float64(c.High)
		r.confidence = c.Confidence
	}
	if mean := latestYearMean(resp.SquareMeters.Global); mean > 0 {
		r.hasSqm = true
		r.sqmErr = (mean*float64(subject.BoligaBuildingSize) - actual) / actual * 100
	}
	return r
}

// known returns the sales that had already closed when the held-out one did.
// A comparable that sells later is information the real lookup could not have
// had, and including it is the one mistake that would make every number in
// this report flattering and worthless.
func known(sales []hjem.Sale, cutoff time.Time) []hjem.Sale {
	var out []hjem.Sale
	for _, s := range sales {
		if s.Date.Before(cutoff) {
			out = append(out, s)
		}
	}
	return out
}

// latestYearMean is the baseline estimator: the area's newest square-metre
// price. It reads the same aggregation comps market-adjusts towards, so both
// estimators are anchored to one price level and the comparison between them
// is of the weighting alone.
func latestYearMean(agg map[time.Time]hjem.Aggregation) float64 {
	var latest time.Time
	var mean float64
	for t, a := range agg {
		if a.Mean > 0 && t.After(latest) {
			latest, mean = t, float64(a.Mean)
		}
	}
	return mean
}

// distanceKm stands in for DAR's radius filter, which is what narrows the
// neighbour set in production. The coordinate columns are transposed —
// Latitude holds the x/longitude DAR serves — so they are read back swapped
// here. comps.go hands the same two columns to its haversine in declared
// order, making its distance weight a different quantity; reproducing
// production means leaving that as it is. Over a few hundred metres the
// equirectangular form is accurate to millimetres.
func distanceKm(a, b *hjem.Address) float64 {
	const earthRadiusKm = 6371.0
	lat1, lat2 := a.Longtitude, b.Longtitude
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (b.Latitude - a.Latitude) * math.Pi / 180 * math.Cos((lat1+lat2)/2*math.Pi/180)
	return earthRadiusKm * math.Sqrt(dLat*dLat+dLon*dLon)
}

// dist carries the tails alongside the middle because a median on its own
// hides that a minority of homes come out valued at twice or half what they
// fetched, and that minority is the whole reason to care.
type dist struct {
	n                  int
	median, p75, p90   float64 // of the absolute error
	within10, within20 float64 // share of cases, percent
	medianSigned       float64 // positive means the estimator reads high
}

func describe(signed []float64) dist {
	d := dist{n: len(signed)}
	if d.n == 0 {
		return d
	}

	abs := make([]float64, d.n)
	var n10, n20 int
	for i, e := range signed {
		abs[i] = math.Abs(e)
		if abs[i] <= 10 {
			n10++
		}
		if abs[i] <= 20 {
			n20++
		}
	}
	d.within10 = float64(n10) / float64(d.n) * 100
	d.within20 = float64(n20) / float64(d.n) * 100

	sort.Float64s(abs)
	d.median, d.p75, d.p90 = percentile(abs, 0.5), percentile(abs, 0.75), percentile(abs, 0.9)

	sorted := append([]float64(nil), signed...)
	sort.Float64s(sorted)
	d.medianSigned = percentile(sorted, 0.5)
	return d
}

// percentile interpolates between the two bracketing observations. Nearest
// rank was the obvious choice and had to go: on an even sample it returns the
// lower of the two middle values, which reports an estimator that is wrong by
// ±30% as biased 30% low rather than unbiased.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := p * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return sorted[lo] + (pos-float64(lo))*(sorted[hi]-sorted[lo])
}

func report(results []result) {
	var compsErrs, sqmOnComps, sqmAll []float64
	var inBand int
	byConf := map[string][]float64{}
	bandByConf := map[string]int{}

	for _, r := range results {
		if r.hasSqm {
			sqmAll = append(sqmAll, r.sqmErr)
		}
		if !r.hasComps {
			continue
		}
		compsErrs = append(compsErrs, r.compsErr)
		byConf[r.confidence] = append(byConf[r.confidence], r.compsErr)
		if r.inBand {
			inBand++
			bandByConf[r.confidence]++
		}
		if r.hasSqm {
			sqmOnComps = append(sqmOnComps, r.sqmErr)
		}
	}

	fmt.Printf("  comps estimate produced: %d, declined: %d (fewer than minComps usable comparables, or no priced year to adjust against)\n",
		len(compsErrs), len(results)-len(compsErrs))
	fmt.Printf("  sqm baseline produced:   %d\n\n", len(sqmAll))

	fmt.Printf("%-22s %6s %8s %8s %8s %8s %8s %8s\n", "error vs actual price", "n", "median", "p75", "p90", "<=10%", "<=20%", "bias")
	row := func(label string, d dist) {
		fmt.Printf("%-22s %6d %7.1f%% %7.1f%% %7.1f%% %7.0f%% %7.0f%% %+7.1f%%\n",
			label, d.n, d.median, d.p75, d.p90, d.within10, d.within20, d.medianSigned)
	}
	row("comps", describe(compsErrs))
	row("sqm, same hold-outs", describe(sqmOnComps))
	row("sqm, all hold-outs", describe(sqmAll))

	if len(compsErrs) > 0 {
		fmt.Printf("\npublished [low, high] contained the actual price in %d of %d (%.0f%%)\n",
			inBand, len(compsErrs), float64(inBand)/float64(len(compsErrs))*100)
	}

	fmt.Printf("\n%-22s %6s %8s %8s %8s\n", "by stated confidence", "n", "median", "p90", "in band")
	for _, c := range []string{"high", "medium", "low"} {
		d := describe(byConf[c])
		if d.n == 0 {
			fmt.Printf("%-22s %6d\n", c, 0)
			continue
		}
		fmt.Printf("%-22s %6d %7.1f%% %7.1f%% %7.0f%%\n", c, d.n, d.median, d.p90, float64(bandByConf[c])/float64(d.n)*100)
	}
}
