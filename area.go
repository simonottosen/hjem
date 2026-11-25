package hjem

// area.go is the drawn-area alternative to a radius: a polygon the user traced
// on the map, searched in place of the circle circlePolygonWKT approximates.
// Everything after the DAR point query is indifferent to the shape, so all an
// area needs is to be checked, turned into WKT and keyed for the cache.

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// lonLat is one vertex as the page sends it: [lon, lat], GeoJSON order, which
// is also the order the map library hands its coordinates out in.
type lonLat [2]float64

func (p lonLat) lon() float64 { return p[0] }
func (p lonLat) lat() float64 { return p[1] }

// The bounds are the cost of a lookup, as maxLookupRadius is: the largest area
// is exactly the largest circle the page offers, so drawing buys a different
// shape and never a larger search. The vertex cap bounds the self-intersection
// check, which is quadratic, and the WKT that is inlined into every DAR query.
// The floor rejects what is in practice a mis-click: a few square metres hold
// no address, and nearly-collinear rings are where projected geometry gets
// numerically unreliable.
const (
	maxAreaVertices = 200
	minAreaM2       = 100.0
	maxAreaM2       = math.Pi * maxLookupRadius * maxLookupRadius
)

// Denmark's bounding box, generously: Bornholm reaches 15.2°E and Skagen
// 57.75°N. It is a sanity check rather than a border — it keeps the projection
// inside the zone it is accurate for and catches coordinates sent lat-first.
const (
	dkMinLon, dkMaxLon = 7.5, 15.5
	dkMinLat, dkMaxLat = 54.4, 58.0
)

// areaPrecision is the precision a drawn area is kept at, about a metre: the
// precision of the encoded polyline the page puts in its links (see
// decodePolyline). Rounding to it here means the lookup, its link and its CSV
// export all search the one ring, under the one cache key.
const areaPrecision = 1e5

// validateArea checks a drawn ring and returns it normalised: rounded to
// areaPrecision, and with a vertex repeating the one before it dropped — the
// closing vertex of a ring the client closed itself included — so every later
// step sees each vertex once. Rounding can merge points a freehand stroke drew
// side by side, and a repeat in sequence adds no edge. A vertex revisited
// later is another matter: that pinches the ring, and is refused below. The
// ring must be simple — DAR is not documented to define `within` for a polygon
// that crosses itself, and a bow-tie's shoelace area understates what it
// covers, which would let it past the size cap.
func validateArea(drawn []lonLat) ([]lonLat, error) {
	pts := make([]lonLat, 0, len(drawn))
	for _, p := range drawn {
		p = lonLat{math.Round(p[0]*areaPrecision) / areaPrecision, math.Round(p[1]*areaPrecision) / areaPrecision}
		if len(pts) > 0 && pts[len(pts)-1] == p {
			continue
		}
		pts = append(pts, p)
	}
	if len(pts) > 1 && pts[0] == pts[len(pts)-1] {
		pts = pts[:len(pts)-1]
	}
	if len(pts) < 3 || len(pts) > maxAreaVertices {
		return nil, fmt.Errorf("et område skal have mellem 3 og %d punkter", maxAreaVertices)
	}

	for _, p := range pts {
		if math.IsNaN(p.lon()) || math.IsNaN(p.lat()) || math.IsInf(p.lon(), 0) || math.IsInf(p.lat(), 0) {
			return nil, errors.New("området indeholder ugyldige koordinater")
		}
		if p.lon() < dkMinLon || p.lon() > dkMaxLon || p.lat() < dkMinLat || p.lat() > dkMaxLat {
			return nil, errors.New("området skal ligge i Danmark")
		}
	}

	// Judged in metres, in the CRS DAR evaluates the polygon in, so the
	// straight edges checked here are the edges DAR will see. A vertex visited
	// twice is caught here too: the edges either side of each visit touch.
	xy := projectArea(pts)
	n := len(xy)
	for i := 0; i < n; i++ {
		for j := i + 2; j < n; j++ {
			// The first and last edges share vertex 0 and are adjacent.
			if i == 0 && j == n-1 {
				continue
			}
			if segmentsTouch(xy[i], xy[(i+1)%n], xy[j], xy[(j+1)%n]) {
				return nil, errors.New("området må ikke krydse sig selv")
			}
		}
	}

	a := math.Abs(signedAreaM2(xy))
	if a < minAreaM2 {
		return nil, fmt.Errorf("området skal være mindst %.0f m²", minAreaM2)
	}
	if a > maxAreaM2 {
		return nil, fmt.Errorf("området er %.2f km², men må højst være %.2f km²", a/1e6, maxAreaM2/1e6)
	}

	return pts, nil
}

// projectArea maps each vertex to EPSG:25832 easting/northing.
func projectArea(pts []lonLat) [][2]float64 {
	xy := make([][2]float64, len(pts))
	for i, p := range pts {
		e, n := forwardUTM32(p.lat(), p.lon())
		xy[i] = [2]float64{e, n}
	}
	return xy
}

// signedAreaM2 is the shoelace area of an open ring: positive when the vertices
// run counter-clockwise.
func signedAreaM2(xy [][2]float64) float64 {
	var s float64
	for i := range xy {
		j := (i + 1) % len(xy)
		s += xy[i][0]*xy[j][1] - xy[j][0]*xy[i][1]
	}
	return s / 2
}

// areaKM2 is the area for the log line, which reports how much was searched.
func areaKM2(pts []lonLat) float64 {
	return math.Abs(signedAreaM2(projectArea(pts))) / 1e6
}

// segmentsTouch reports whether segments ab and cd share any point, crossings
// and touchings alike: for a ring, a vertex resting on a non-adjacent edge is
// as much a self-intersection as two edges crossing.
func segmentsTouch(a, b, c, d [2]float64) bool {
	o1, o2 := orient(a, b, c), orient(a, b, d)
	o3, o4 := orient(c, d, a), orient(c, d, b)
	if o1*o2 < 0 && o3*o4 < 0 {
		return true
	}
	return (o1 == 0 && onSegment(a, b, c)) || (o2 == 0 && onSegment(a, b, d)) ||
		(o3 == 0 && onSegment(c, d, a)) || (o4 == 0 && onSegment(c, d, b))
}

func orient(a, b, c [2]float64) float64 {
	v := (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0])
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

// onSegment reports whether p, already known to be collinear with ab, lies
// within its bounding box and so on the segment itself.
func onSegment(a, b, p [2]float64) bool {
	return math.Min(a[0], b[0]) <= p[0] && p[0] <= math.Max(a[0], b[0]) &&
		math.Min(a[1], b[1]) <= p[1] && p[1] <= math.Max(a[1], b[1])
}

// areaWKT renders a validated area as the same closed EPSG:25832 polygon
// circlePolygonWKT produces for a circle. The coordinates are formatted from
// the projected floats and never copied from the request: the WKT is inlined
// into the GraphQL query text (see darQueryPoints), so only digits may reach
// it. The ring is turned counter-clockwise if it was drawn the other way, the
// orientation the circle has always been sent in and so the one known to work.
func areaWKT(pts []lonLat) string {
	xy := projectArea(pts)
	if signedAreaM2(xy) < 0 {
		for i, j := 0, len(xy)-1; i < j; i, j = i+1, j-1 {
			xy[i], xy[j] = xy[j], xy[i]
		}
	}

	var b strings.Builder
	b.WriteString("POLYGON ((")
	for i := 0; i <= len(xy); i++ {
		p := xy[i%len(xy)]
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%.3f %.3f", p[0], p[1])
	}
	b.WriteString("))")
	return b.String()
}

// decodePolyline reads an area from Google's encoded polyline format at
// precision 5, which is how the page fits a drawn area into a link: two hundred
// vertices as JSON would make a URL several kilobytes long. The format stores
// latitude before longitude; the result is in the lonLat order used elsewhere.
func decodePolyline(s string) ([]lonLat, error) {
	var vals []int
	for i := 0; i < len(s); {
		var result, shift int
		for {
			if i >= len(s) {
				return nil, errors.New("ugyldigt område i linket")
			}
			b := int(s[i]) - 63
			i++
			if b < 0 || b > 63 || shift > 30 {
				return nil, errors.New("ugyldigt område i linket")
			}
			result |= (b & 0x1f) << shift
			shift += 5
			if b < 0x20 {
				break
			}
		}
		if result&1 != 0 {
			vals = append(vals, ^(result >> 1))
		} else {
			vals = append(vals, result>>1)
		}
	}
	if len(vals)%2 != 0 {
		return nil, errors.New("ugyldigt område i linket")
	}

	pts := make([]lonLat, 0, len(vals)/2)
	var lat, lon int
	for i := 0; i < len(vals); i += 2 {
		lat += vals[i]
		lon += vals[i+1]
		pts = append(pts, lonLat{float64(lon) / 1e5, float64(lat) / 1e5})
	}
	return pts, nil
}
