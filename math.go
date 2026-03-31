package hjem

import (
	"fmt"
	"math"
	"sort"
	"time"
)

type Aggregation struct {
	Mean int `json:"mean"`
	Std  int `json:"std"`
	N    int `json:"n"`
}

func AggregationFromPrices(prices []float64) Aggregation {
	if len(prices) == 0 {
		return Aggregation{}
	}

	n := len(prices)
	var sum float64
	for _, p := range prices {
		sum += p
	}

	mean := sum / float64(n)

	var variance float64
	for _, p := range prices {
		variance += (p - mean) * (p - mean)
	}
	std := math.Sqrt(variance / float64(n))

	return Aggregation{
		Mean: int(math.Round(mean)),
		Std:  int(math.Round(std)),
		N:    n,
	}
}

// median returns the median of a sorted slice.
func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 0 {
		return (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return sorted[n/2]
}

// removeOutliersIQR uses the Interquartile Range method to identify outliers.
// This is robust against extreme values — unlike mean±std, the IQR is not
// distorted by a few 27M kr whole-building sales mixed in with 2M apartment sales.
// multiplier controls sensitivity: 1.5 is standard, lower is more aggressive.
func removeOutliersIQR(sales []*JSONSale, prices []float64, multiplier float64) ([]float64, []*JSONSale) {
	if len(prices) < 4 {
		return prices, nil
	}

	// Sort prices to compute quartiles (keep index mapping)
	type indexed struct {
		price float64
		idx   int
	}
	items := make([]indexed, len(prices))
	for i, p := range prices {
		items[i] = indexed{p, i}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].price < items[j].price })

	sortedPrices := make([]float64, len(items))
	for i, item := range items {
		sortedPrices[i] = item.price
	}

	mid := len(sortedPrices) / 2
	q1 := median(sortedPrices[:mid])
	var q3 float64
	if len(sortedPrices)%2 == 0 {
		q3 = median(sortedPrices[mid:])
	} else {
		q3 = median(sortedPrices[mid+1:])
	}
	iqr := q3 - q1

	lower := q1 - multiplier*iqr
	upper := q3 + multiplier*iqr

	var clean []float64
	var outliers []*JSONSale
	for i, p := range prices {
		if p < lower || p > upper {
			outliers = append(outliers, sales[i])
		} else {
			clean = append(clean, p)
		}
	}

	return clean, outliers
}

func SalesStatistics(addrs []*Address, sales []*JSONSale, stdf int) ([]*JSONSale, map[time.Time]Aggregation) {
	type G struct {
		S []*JSONSale
		P []float64
	}

	// Group sales by year, computing price-per-sqm with float precision
	temp := map[int]G{}
	for _, s := range sales {
		year, _, _ := s.When.Date()
		sqMeters := addrs[s.AddrIndex].BoligaBuildingSize
		if sqMeters == 0 {
			continue
		}

		pricePerSqm := float64(s.Amount) / float64(sqMeters)

		g := temp[year]
		g.S = append(g.S, s)
		g.P = append(g.P, pricePerSqm)

		temp[year] = g
	}

	out := map[time.Time]Aggregation{}
	outliers := map[*JSONSale]bool{}

	for Y, g := range temp {
		year, _ := time.Parse("2-1-2006", fmt.Sprintf("1-1-%d", Y))

		if stdf > 0 && len(g.P) >= 4 {
			// IQR-based outlier removal: robust against extreme values
			// Use stdf to control sensitivity: stdf=1 → multiplier=1.5 (strict),
			// stdf=2 → multiplier=2.0, stdf=3 → multiplier=2.5 (lenient)
			multiplier := 1.0 + 0.5*float64(stdf)
			cleanPrices, outlz := removeOutliersIQR(g.S, g.P, multiplier)

			for _, ol := range outlz {
				outliers[ol] = true
			}

			if len(cleanPrices) > 0 {
				out[year] = AggregationFromPrices(cleanPrices)
			} else {
				out[year] = AggregationFromPrices(g.P)
			}
		} else {
			out[year] = AggregationFromPrices(g.P)
		}
	}

	// Remove outlier sales (but never remove primary address sales)
	for i := 0; i < len(sales); i++ {
		s := sales[i]
		if outliers[s] && s.AddrIndex != 0 {
			sales = append(sales[:i], sales[i+1:]...)
			i--
		}
	}

	return sales, out
}
