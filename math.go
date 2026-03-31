package hjem

import (
	"fmt"
	"math"
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

// SeperateOutliers splits sales into normal and outlier sets based on
// mean ± stdf*std of the per-sqm price.
func SeperateOutliers(sales []*JSONSale, prices []float64, aggr Aggregation, stdf int) ([]float64, []*JSONSale) {
	var normal []float64
	var outliers []*JSONSale

	lowb := float64(aggr.Mean) - float64(aggr.Std)*float64(stdf)
	upperb := float64(aggr.Mean) + float64(aggr.Std)*float64(stdf)
	for i := range sales {
		p := prices[i]
		s := sales[i]

		if p < lowb || p > upperb {
			outliers = append(outliers, s)
			continue
		}

		normal = append(normal, p)
	}

	return normal, outliers
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

		if stdf > 0 && len(g.P) >= 3 {
			// Two-pass outlier removal:
			// Pass 1: compute initial stats and identify outliers
			agg1 := AggregationFromPrices(g.P)
			cleanPrices, outlz := SeperateOutliers(g.S, g.P, agg1, stdf)

			for _, ol := range outlz {
				outliers[ol] = true
			}

			// Pass 2: recompute aggregation from cleaned data
			if len(cleanPrices) > 0 {
				out[year] = AggregationFromPrices(cleanPrices)
			} else {
				out[year] = agg1
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
