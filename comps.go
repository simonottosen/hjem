package hjem

import (
	"fmt"
	"math"
	"sort"
	"time"
)

const (
	// Time decay: half-life ~2.3 years
	timeLambda = 0.3

	// Size similarity: σ = 25% difference
	sizeSigma = 0.25

	// Room similarity: σ = 1.5 rooms
	roomSigma = 1.5

	// Build year similarity: σ = 20 years
	ageSigma = 20.0

	// Distance: half-weight at 200m
	distHalf = 0.2 // km

	// Maximum comparables to use
	maxComps = 30

	// Minimum comparables required
	minComps = 3
)

type CompsEstimate struct {
	Value      int    `json:"value"`       // Point estimate in DKK
	SqmPrice   int    `json:"sqm_price"`   // Estimated price per m²
	Low        int    `json:"low"`         // Lower bound
	High       int    `json:"high"`        // Upper bound
	Confidence string `json:"confidence"`  // "high", "medium", "low"
	NumComps   int    `json:"num_comps"`   // Number of comparable sales used
}

type compCandidate struct {
	adjustedSqmPrice float64
	weight           float64
}

// ComputeCompsEstimate runs a weighted comparable sales analysis.
// It market-adjusts historical prices using the area trend, then weights
// each sale by recency, size/room/age similarity, and geographic distance.
func ComputeCompsEstimate(
	primary *Address,
	addrs []*Address,
	sales []*JSONSale,
	globalAgg map[time.Time]Aggregation,
) *CompsEstimate {
	if primary == nil || primary.BoligaBuildingSize == 0 {
		return nil
	}

	// Find latest year's mean for market adjustment
	latestMean, _ := latestAggMean(globalAgg)
	if latestMean == 0 {
		return nil
	}

	now := time.Now()
	primarySize := float64(primary.BoligaBuildingSize)
	primaryRooms := float64(primary.BoligaRooms)
	primaryBuildYear := float64(primary.BoligaBuiltYear)
	primaryLat := primary.Latitude
	primaryLon := primary.Longtitude

	var candidates []compCandidate

	for _, s := range sales {
		// Skip primary address's own sales
		if s.AddrIndex == 0 {
			continue
		}

		// Need size for sqm price
		size := s.SqMeters
		if size == 0 {
			addr := addrs[s.AddrIndex]
			size = addr.BoligaBuildingSize
		}
		if size == 0 {
			continue
		}

		saleSqmPrice := float64(s.Amount) / float64(size)
		if saleSqmPrice <= 0 {
			continue
		}

		// Market-adjust to current level
		saleYear, _, _ := s.When.Date()
		saleYearTime, _ := time.Parse("2-1-2006", "1-1-"+itoa(saleYear))
		saleYearMean := float64(globalAgg[saleYearTime].Mean)
		if saleYearMean <= 0 {
			// Try closest year
			saleYearMean = closestYearMean(globalAgg, saleYear)
			if saleYearMean <= 0 {
				continue
			}
		}
		adjustedSqmPrice := saleSqmPrice * (latestMean / saleYearMean)

		// Compute weights
		yearsAgo := now.Sub(s.When).Hours() / (24 * 365.25)
		wTime := math.Exp(-timeLambda * yearsAgo)

		// Size similarity (% difference)
		saleSize := float64(size)
		sizeDiffPct := (saleSize - primarySize) / primarySize
		wSize := gaussian(sizeDiffPct, sizeSigma)

		// Room similarity
		saleRooms := float64(s.Rooms)
		if saleRooms == 0 {
			addr := addrs[s.AddrIndex]
			saleRooms = float64(addr.BoligaRooms)
		}
		wRooms := 1.0
		if primaryRooms > 0 && saleRooms > 0 {
			wRooms = gaussian(saleRooms-primaryRooms, roomSigma)
		}

		// Build year similarity
		saleBuildYear := float64(s.BuildYear)
		if saleBuildYear == 0 {
			addr := addrs[s.AddrIndex]
			saleBuildYear = float64(addr.BoligaBuiltYear)
		}
		wAge := 1.0
		if primaryBuildYear > 0 && saleBuildYear > 0 {
			wAge = gaussian(saleBuildYear-primaryBuildYear, ageSigma)
		}

		// Geographic distance
		addr := addrs[s.AddrIndex]
		dist := haversineKm(primaryLat, primaryLon, addr.Latitude, addr.Longtitude)
		wDist := 1.0 / (1.0 + dist/distHalf)

		weight := wTime * wSize * wRooms * wAge * wDist
		if weight < 1e-6 {
			continue
		}

		candidates = append(candidates, compCandidate{
			adjustedSqmPrice: adjustedSqmPrice,
			weight:           weight,
		})
	}

	if len(candidates) < minComps {
		return nil
	}

	// Select top N by weight
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].weight > candidates[j].weight
	})
	if len(candidates) > maxComps {
		candidates = candidates[:maxComps]
	}

	// Weighted mean
	var sumW, sumWP float64
	for _, c := range candidates {
		sumW += c.weight
		sumWP += c.weight * c.adjustedSqmPrice
	}
	if sumW == 0 {
		return nil
	}
	estSqmPrice := sumWP / sumW

	// Weighted standard deviation
	var sumWVar float64
	for _, c := range candidates {
		diff := c.adjustedSqmPrice - estSqmPrice
		sumWVar += c.weight * diff * diff
	}
	wStd := math.Sqrt(sumWVar / sumW)

	estValue := estSqmPrice * primarySize
	low := (estSqmPrice - wStd) * primarySize
	high := (estSqmPrice + wStd) * primarySize

	if low < 0 {
		low = 0
	}

	// Confidence assessment
	confidence := "low"
	totalWeight := sumW
	nComps := len(candidates)
	rangeWidth := high - low
	rangePct := rangeWidth / estValue

	if nComps >= 15 && totalWeight > 3.0 && rangePct < 0.3 {
		confidence = "high"
	} else if nComps >= 8 && totalWeight > 1.5 && rangePct < 0.5 {
		confidence = "medium"
	}

	return &CompsEstimate{
		Value:      int(math.Round(estValue)),
		SqmPrice:   int(math.Round(estSqmPrice)),
		Low:        int(math.Round(low)),
		High:       int(math.Round(high)),
		Confidence: confidence,
		NumComps:   nComps,
	}
}

// gaussian returns exp(-(x²)/(2σ²))
func gaussian(x, sigma float64) float64 {
	return math.Exp(-(x * x) / (2 * sigma * sigma))
}

// haversineKm returns distance in km between two lat/lng points.
func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0 // Earth radius in km
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	lat1r := lat1 * math.Pi / 180
	lat2r := lat2 * math.Pi / 180

	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1r)*math.Cos(lat2r)*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return R * c
}

// latestAggMean returns the mean and year of the most recent aggregation.
func latestAggMean(agg map[time.Time]Aggregation) (float64, time.Time) {
	var latest time.Time
	var mean float64
	for t, a := range agg {
		if t.After(latest) && a.Mean > 0 {
			latest = t
			mean = float64(a.Mean)
		}
	}
	return mean, latest
}

// closestYearMean finds the aggregation mean for the year closest to target.
func closestYearMean(agg map[time.Time]Aggregation, targetYear int) float64 {
	bestDist := math.MaxInt64
	bestMean := 0.0
	for t, a := range agg {
		y, _, _ := t.Date()
		d := y - targetYear
		if d < 0 {
			d = -d
		}
		if d < bestDist && a.Mean > 0 {
			bestDist = d
			bestMean = float64(a.Mean)
		}
	}
	return bestMean
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}
