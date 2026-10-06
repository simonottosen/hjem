package hjem

import "math"

// MapPlan is what the loading map draws: where to centre, how far the search
// reached, and which buildings belong to which Boliga street query.
//
// Grouping by street is what lets the reveal be driven by completed work
// rather than by a timer — a street is the unit Boliga is queried in. A street
// absent from boliga_tasks was served from cache and can be shown at once.
type MapPlan struct {
	Lat     float64     `json:"lat"`
	Lon     float64     `json:"lon"`
	RadiusM int         `json:"radius_m"`
	Streets []MapStreet `json:"streets"`
}

type MapStreet struct {
	Task BoligaPropertyRequest `json:"task"`
	// [lat, lon] pairs rather than objects: a dense 500 m lookup runs to a few
	// thousand buildings and this payload repeats on every poll.
	Points [][2]float64 `json:"points"`
}

// mapPointPrecision rounds coordinates to ~1 m, which is what collapses the
// dozens of flats sharing one staircase into a single building.
const mapPointPrecision = 1e5

// buildMapPlan takes the requested radii rather than one radius because a
// lookup can ask for several at once, and the map has to cover the widest.
func buildMapPlan(primary *Address, addrs []*Address, ranges []int) *MapPlan {
	// Via addrLatLon because Address stores x in .Latitude and y in .Longtitude
	// — reading the fields at face value puts Copenhagen in the Indian Ocean.
	round := func(a *Address) [2]float64 {
		lat, lon := addrLatLon(*a)
		return [2]float64{
			math.Round(lat*mapPointPrecision) / mapPointPrecision,
			math.Round(lon*mapPointPrecision) / mapPointPrecision,
		}
	}

	primaryLat, primaryLon := addrLatLon(*primary)
	plan := &MapPlan{Lat: primaryLat, Lon: primaryLon}
	for _, r := range ranges {
		plan.RadiusM = max(plan.RadiusM, r)
	}

	// The searched property is drawn as its own marker, so a neighbouring flat
	// at the same access point must not bury it under an ordinary dot.
	seen := map[[2]float64]bool{round(primary): true}
	idx := map[BoligaPropertyRequest]int{}

	for _, a := range addrs {
		if a.Latitude == 0 && a.Longtitude == 0 {
			continue
		}
		p := round(a)
		if seen[p] {
			continue
		}
		seen[p] = true

		t := addrStreetTask(a)
		i, ok := idx[t]
		if !ok {
			i = len(plan.Streets)
			idx[t] = i
			plan.Streets = append(plan.Streets, MapStreet{Task: t})
		}
		plan.Streets[i].Points = append(plan.Streets[i].Points, p)
	}

	return plan
}
