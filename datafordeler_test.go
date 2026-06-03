package hjem

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// On the central meridian (lon = 9°E) the easting is exactly the false easting,
// regardless of latitude. This is a defining property of UTM and validates the
// core Transverse Mercator handling.
func TestForwardUTM32CentralMeridian(t *testing.T) {
	for _, lat := range []float64{54.0, 55.5, 57.0, 57.75} {
		e, _ := forwardUTM32(lat, utmLon0Deg)
		if math.Abs(e-utmFE) > 1e-3 { // sub-mm
			t.Errorf("lat=%.2f lon=9: easting=%.4f, want %.1f", lat, e, utmFE)
		}
	}
}

// East of the central meridian easting must increase monotonically.
func TestForwardUTM32MonotonicEasting(t *testing.T) {
	const lat = 55.7
	prev, _ := forwardUTM32(lat, 9.0)
	for _, lon := range []float64{9.5, 10.0, 11.0, 12.0, 12.6} {
		e, _ := forwardUTM32(lat, lon)
		if e <= prev {
			t.Errorf("easting not increasing at lon=%.2f: %.2f <= %.2f", lon, e, prev)
		}
		prev = e
	}
}

// Forward→inverse must recover the original coordinate to sub-cm precision
// across the Danish bounding box.
func TestUTM32RoundTrip(t *testing.T) {
	points := [][2]float64{
		{55.6761, 12.5683}, // København
		{56.1629, 10.2039}, // Aarhus
		{57.0488, 9.9217},  // Aalborg
		{55.3960, 10.3870}, // Odense
		{55.4666, 8.4520},  // Esbjerg (near west edge)
		{54.9000, 12.4500}, // Bornholm-ish (south)
	}
	for _, p := range points {
		lat, lon := p[0], p[1]
		e, n := forwardUTM32(lat, lon)
		gotLat, gotLon := inverseUTM32(e, n)
		// 1e-7 deg latitude ≈ 1.1 cm.
		if math.Abs(gotLat-lat) > 1e-7 || math.Abs(gotLon-lon) > 1e-7 {
			t.Errorf("round trip %.5f,%.5f → %.5f,%.5f (Δlat=%.2e Δlon=%.2e)",
				lat, lon, gotLat, gotLon, gotLat-lat, gotLon-lon)
		}
	}
}

// Projected Danish coordinates must fall inside the official EPSG:25832 bounds
// for Denmark. This catches gross errors (wrong zone, swapped lat/lon, wrong
// hemisphere) without asserting digits we cannot independently verify here.
func TestForwardUTM32DenmarkBounds(t *testing.T) {
	// Generous national bounds for ETRS89/UTM32N over Denmark.
	const (
		minE, maxE = 400000.0, 900000.0
		minN, maxN = 6_040_000.0, 6_410_000.0
	)
	cph := struct{ lat, lon float64 }{55.6761, 12.5683}
	e, n := forwardUTM32(cph.lat, cph.lon)
	if e < minE || e > maxE || n < minN || n > maxN {
		t.Errorf("København projected outside Denmark bounds: E=%.1f N=%.1f", e, n)
	}
	// København is well east of the central meridian → easting clearly > 500 km.
	if e < 700000 || e > 740000 {
		t.Errorf("København easting %.1f outside expected ~725 km band", e)
	}
}

func TestCirclePolygonWKT(t *testing.T) {
	const (
		lat, lon = 55.6761, 12.5683
		radius   = 250.0
		segments = 48
	)
	wkt := circlePolygonWKT(lat, lon, radius, segments)

	if !strings.HasPrefix(wkt, "POLYGON ((") || !strings.HasSuffix(wkt, "))") {
		t.Fatalf("unexpected WKT shape: %s", wkt)
	}

	inner := wkt[len("POLYGON ((") : len(wkt)-len("))")]
	coords := strings.Split(inner, ", ")
	if len(coords) != segments+1 {
		t.Fatalf("got %d vertices, want %d (segments+1, ring closed)", len(coords), segments+1)
	}
	// Ring must be closed: first == last.
	if coords[0] != coords[len(coords)-1] {
		t.Errorf("ring not closed: first=%q last=%q", coords[0], coords[len(coords)-1])
	}

	// Every vertex must sit ~radius metres from the centre in projected space.
	cE, cN := forwardUTM32(lat, lon)
	for _, c := range coords {
		parts := strings.Fields(c)
		if len(parts) != 2 {
			t.Fatalf("bad vertex %q", c)
		}
		e, err1 := strconv.ParseFloat(parts[0], 64)
		n, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("unparseable vertex %q", c)
		}
		d := math.Hypot(e-cE, n-cN)
		if math.Abs(d-radius) > 1e-3 {
			t.Errorf("vertex %q is %.3f m from centre, want %.1f", c, d, radius)
		}
	}
}
