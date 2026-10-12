package hjem

import (
	"math"
	"testing"
	"time"
)

// Four comps at the same distance from the primary, two of them north and
// south and two east and west, priced differently along each axis. Distance is
// the only weight that can tell them apart — size, rooms, build year and sale
// date are identical — so a distance that is isotropic has to return the
// midpoint of the two prices.
//
// Reading the coordinate columns in declared order does not: it evaluates the
// cos(lat) term at Copenhagen's longitude, which leaves north-south about right
// and stretches east-west by 1.77x. The east and west comps then look 443 m
// away rather than 250 m, carry less weight for it, and the estimate lands at
// 38,167 kr/m² instead of 40,000.
func TestCompsDistanceIsIsotropic(t *testing.T) {
	const (
		originLat = 55.6761 // Copenhagen
		originLon = 12.5683
		metre     = 1.0 / 111195.0 // degrees of latitude
	)
	// A degree of longitude is shorter than a degree of latitude by cos(lat).
	eastMetre := metre / math.Cos(originLat*math.Pi/180)

	// Crossed over, like mapAddr in mapplan_test.go: Address stores x in
	// .Latitude and y in .Longtitude, so writing them straight through would
	// let the test agree with a reader that takes the fields at face value.
	at := func(lat, lon float64) *Address {
		return &Address{
			Latitude: lon, Longtitude: lat,
			BoligaBuildingSize: 100, BoligaRooms: 3, BoligaBuiltYear: 1932,
		}
	}
	primary := at(originLat, originLon)
	addrs := []*Address{
		primary,
		at(originLat+250*metre, originLon),
		at(originLat-250*metre, originLon),
		at(originLat, originLon+250*eastMetre),
		at(originLat, originLon-250*eastMetre),
	}

	when := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	sale := func(idx, amount int) *JSONSale {
		return &JSONSale{AddrIndex: idx, Amount: amount, SqMeters: 100,
			Rooms: 3, BuildYear: 1932, When: when}
	}
	sales := []*JSONSale{
		sale(1, 3_000_000), sale(2, 3_000_000), // 30,000 kr/m², north and south
		sale(3, 5_000_000), sale(4, 5_000_000), // 50,000 kr/m², east and west
	}

	// One priced year, equal to neither side's price, so the market adjustment
	// is the identity for every comp and cannot skew the average.
	globalAgg := map[time.Time]Aggregation{
		time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC): {Mean: 40000, N: 4},
	}

	est := ComputeCompsEstimate(primary, addrs, sales, globalAgg)
	if est == nil {
		t.Fatal("ComputeCompsEstimate returned nil, want an estimate from four comps")
	}
	if got, want := est.SqmPrice, 40000; math.Abs(float64(got-want)) > 400 {
		t.Errorf("%d kr/m², want %d ± 400 — the east-west comps were weighted as if they were further away than the north-south ones",
			got, want)
	}
}
