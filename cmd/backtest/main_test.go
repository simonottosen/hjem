package main

import (
	"math"
	"testing"
	"time"

	hjem "github.com/tpanum/hjem"
)

func TestDescribeSeparatesSpreadFromBias(t *testing.T) {
	// Deliberately lopsided: every estimate reads high, and one reads very
	// high. A summary that reported only a mean absolute error would let the
	// 300% case set the headline, and one that reported only the median would
	// not show it at all.
	d := describe([]float64{5, 8, 12, 25, 300})

	if d.n != 5 {
		t.Fatalf("n = %d, want 5", d.n)
	}
	if d.median != 12 {
		t.Errorf("median = %v, want 12", d.median)
	}
	if d.p75 != 25 {
		t.Errorf("p75 = %v, want 25", d.p75)
	}
	if math.Abs(d.p90-190) > 1e-9 {
		t.Errorf("p90 = %v, want 190 (interpolated between 25 and 300)", d.p90)
	}
	if d.medianSigned != 12 {
		t.Errorf("medianSigned = %v, want 12", d.medianSigned)
	}
	if d.within10 != 40 {
		t.Errorf("within10 = %v%%, want 40", d.within10)
	}
	if d.within20 != 60 {
		t.Errorf("within20 = %v%%, want 60", d.within20)
	}
}

// An estimator that is wrong by the same amount in both directions has to
// score as unbiased but inaccurate. Collapsing the sign before taking the
// median would report it as perfect.
func TestDescribeUnbiasedButInaccurate(t *testing.T) {
	d := describe([]float64{-40, -30, 30, 40})

	if d.medianSigned != 0 {
		t.Errorf("medianSigned = %v, want 0 for a symmetric sample", d.medianSigned)
	}
	if d.median != 35 {
		t.Errorf("median absolute error = %v, want 35", d.median)
	}
	if d.within20 != 0 {
		t.Errorf("within20 = %v%%, want 0", d.within20)
	}
}

func TestDescribeEmpty(t *testing.T) {
	if d := describe(nil); d.n != 0 || d.median != 0 {
		t.Errorf("describe(nil) = %+v, want a zero dist", d)
	}
}

func TestPercentileEdges(t *testing.T) {
	s := []float64{1, 2, 3, 4}
	if got := percentile(s, 0); got != 1 {
		t.Errorf("p0 = %v, want 1", got)
	}
	if got := percentile(s, 1); got != 4 {
		t.Errorf("p100 = %v, want 4", got)
	}
	if got := percentile(s, 0.5); got != 2.5 {
		t.Errorf("p50 = %v, want 2.5", got)
	}
	if got := percentile([]float64{7}, 0.9); got != 7 {
		t.Errorf("p90 of a single observation = %v, want 7", got)
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("percentile(nil) = %v, want 0", got)
	}
}

func TestKnownExcludesTheHoldOutAndEverythingAfterIt(t *testing.T) {
	cutoff := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	sales := []hjem.Sale{
		{AmountDKK: 1, Date: cutoff.AddDate(0, -1, 0)},
		{AmountDKK: 2, Date: cutoff},
		{AmountDKK: 3, Date: cutoff.AddDate(0, 1, 0)},
	}

	got := known(sales, cutoff)
	if len(got) != 1 || got[0].AmountDKK != 1 {
		t.Fatalf("known() = %+v, want only the sale that closed before the cutoff", got)
	}
}

func TestDistanceKmReadsTheTransposedColumns(t *testing.T) {
	// Two points one degree of longitude apart at 55.7°N, which is where
	// Copenhagen is. If the columns were read in declared order the latitude
	// scaling would use 12.57° and the answer would come out near 111 km.
	a := &hjem.Address{Latitude: 12.0, Longtitude: 55.7}
	b := &hjem.Address{Latitude: 13.0, Longtitude: 55.7}

	want := 111.195 * math.Cos(55.7*math.Pi/180)
	if got := distanceKm(a, b); math.Abs(got-want) > 0.1 {
		t.Errorf("distanceKm = %.3f km, want %.3f km", got, want)
	}
	if got := distanceKm(a, a); got != 0 {
		t.Errorf("distanceKm to self = %v, want 0", got)
	}
}

func TestLatestYearMeanPicksTheNewestPricedYear(t *testing.T) {
	year := func(y int) time.Time { return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC) }
	agg := map[time.Time]hjem.Aggregation{
		year(2022): {Mean: 40000, N: 5},
		year(2024): {Mean: 50000, N: 3},
		// A year present but unpriced must not win: comps.go skips these when
		// it picks its market-adjustment anchor, so the baseline has to too.
		year(2025): {Mean: 0, N: 0},
	}

	if got := latestYearMean(agg); got != 50000 {
		t.Errorf("latestYearMean = %v, want 50000", got)
	}
	if got := latestYearMean(nil); got != 0 {
		t.Errorf("latestYearMean(nil) = %v, want 0", got)
	}
}
