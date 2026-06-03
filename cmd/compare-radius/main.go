// Command compare-radius checks that the new Datafordeleren DAR GraphQL radius
// search (hjem.DARNearbySearch) returns the same set of addresses as the
// existing DAWA cirkel search (hjem.DawaNearbySearch), across 10 reference
// addresses.
//
// For each address it:
//  1. Resolves the address to a centre point via DAWA fuzzy search.
//  2. Runs DAWA cirkel and DAR GraphQL radius searches at the same radius.
//  3. Compares the returned address sets (by address designation) and reports
//     matches, differences, and whether differences are merely boundary effects.
//
// Requirements:
//
//	DATAFORDELER_API_KEY   must be set (the DAR GraphQL service requires it).
//	DATAFORDELER_GRAPHQL_URL  optional override of the endpoint.
//
// Usage:
//
//	DATAFORDELER_API_KEY=xxxx go run ./cmd/compare-radius -radius 100
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	hjem "github.com/tpanum/hjem"
)

// referenceAddresses are 10 diverse Danish addresses (dense city, suburban,
// regional towns) used to exercise the radius search.
var referenceAddresses = []string{
	"Rådhuspladsen 1, 1550 København V",
	"Nørrebrogade 155, 2200 København N",
	"Store Torv 1, 8000 Aarhus C",
	"Vesterbro 23, 9000 Aalborg",
	"Vestergade 1, 5000 Odense C",
	"Kongensgade 1, 6700 Esbjerg",
	"Algade 1, 4000 Roskilde",
	"Perlegade 1, 6400 Sønderborg",
	"Østergade 1, 7400 Herning",
	"Hovedgaden 1, 3460 Birkerød",
}

func main() {
	radius := flag.Int("radius", 100, "search radius in metres")
	segments := flag.Int("segments", 64, "polygon vertex count for the DAR circle approximation")
	boundaryTol := flag.Float64("boundary", 2.0, "metres: a set difference within this of the radius edge is treated as a boundary effect, not a real mismatch")
	n := flag.Int("n", len(referenceAddresses), "number of reference addresses to test (1..10)")
	flag.Parse()

	addresses := referenceAddresses
	if *n > 0 && *n < len(addresses) {
		addresses = addresses[:*n]
	}

	if os.Getenv("DATAFORDELER_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "DATAFORDELER_API_KEY is not set.")
		fmt.Fprintln(os.Stderr, "Create an IT system + API key at https://selfservice.datafordeler.dk and export it:")
		fmt.Fprintln(os.Stderr, "  export DATAFORDELER_API_KEY=...")
		os.Exit(1)
	}

	fmt.Printf("Comparing DAWA cirkel vs DAR GraphQL — radius=%dm, polygon=%d-gon, boundary tol=%.1fm\n\n",
		*radius, *segments, *boundaryTol)

	var (
		compared     int
		exactMatches int
		withDiffs    int
		realMismatch int
		failed       int
	)

	for i, query := range addresses {
		fmt.Printf("[%d/%d] %s\n", i+1, len(addresses), query)

		center, err := resolveCenter(query)
		if err != nil {
			fmt.Printf("   ✗ could not resolve centre: %v\n\n", err)
			failed++
			continue
		}
		cLat, cLon := latLon(center)
		fmt.Printf("   centre: %s  (%.5f, %.5f)\n", center.DawaID, cLat, cLon)

		dawaAddrs, err := hjem.DawaNearbySearch{Addr: *center, Meters: *radius}.Fetch()
		if err != nil {
			fmt.Printf("   ✗ DAWA search failed: %v\n\n", err)
			failed++
			continue
		}

		darAddrs, err := hjem.DARNearbySearch{Addr: *center, Meters: *radius, Segments: *segments}.Fetch()
		if err != nil {
			fmt.Printf("   ✗ DAR search failed: %v\n\n", err)
			failed++
			continue
		}

		compared++

		dawaSet := indexByDesignation(dawaAddrs)
		darSet := indexByDesignation(darAddrs)

		common, onlyDAWA, onlyDAR := diffSets(dawaSet, darSet)

		fmt.Printf("   DAWA: %3d   DAR: %3d   common: %3d   only-DAWA: %d   only-DAR: %d\n",
			len(dawaAddrs), len(darAddrs), len(common), len(onlyDAWA), len(onlyDAR))

		// Live projection sanity check: DAR's point for the centre, decoded
		// through our inverse projection, should match DAWA's point for it.
		if darCenter, ok := darSet[normalize(center.DawaID)]; ok {
			dLat, dLon := latLon(darCenter)
			delta := hjem.HaversineMeters(cLat, cLon, dLat, dLon)
			fmt.Printf("   projection check (centre point, DAR vs DAWA): %.2f m\n", delta)
		}

		realInAddr := reportDiffs("only-DAWA", onlyDAWA, dawaSet, cLat, cLon, float64(*radius), *boundaryTol)
		realInAddr += reportDiffs("only-DAR", onlyDAR, darSet, cLat, cLon, float64(*radius), *boundaryTol)

		switch {
		case len(onlyDAWA) == 0 && len(onlyDAR) == 0:
			fmt.Printf("   ✓ MATCH\n")
			exactMatches++
		case realInAddr == 0:
			fmt.Printf("   ✓ MATCH (differences are all boundary effects)\n")
			exactMatches++
			withDiffs++
		default:
			fmt.Printf("   ⚠ %d real mismatch(es)\n", realInAddr)
			withDiffs++
			realMismatch += realInAddr
		}
		fmt.Println()
	}

	fmt.Println("=== Summary ===")
	fmt.Printf("addresses compared:  %d\n", compared)
	fmt.Printf("exact/boundary match: %d\n", exactMatches)
	fmt.Printf("with differences:    %d\n", withDiffs)
	fmt.Printf("real mismatches:     %d\n", realMismatch)
	if failed > 0 {
		fmt.Printf("failed to compare:   %d\n", failed)
	}

	if realMismatch > 0 || failed > 0 {
		os.Exit(1)
	}
}

// resolveCenter resolves a free-text address to a single centre Address via DAWA
// fuzzy search, taking the first (best) match.
func resolveCenter(query string) (*hjem.Address, error) {
	addrs, err := hjem.DawaFuzzySearch{Query: query}.Fetch()
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no address found")
	}
	return addrs[0], nil
}

// latLon returns true (lat, lon) from an Address. The struct stores DAWA's x in
// .Latitude (actually longitude) and y in .Longtitude (actually latitude); DAR
// results use the same convention, so this unpacking is correct for both.
func latLon(a *hjem.Address) (lat, lon float64) {
	return a.Longtitude, a.Latitude
}

// normalize produces a comparison key from an address designation.
func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

func indexByDesignation(addrs []*hjem.Address) map[string]*hjem.Address {
	m := make(map[string]*hjem.Address, len(addrs))
	for _, a := range addrs {
		m[normalize(a.DawaID)] = a
	}
	return m
}

func diffSets(dawa, dar map[string]*hjem.Address) (common, onlyDAWA, onlyDAR []string) {
	for k := range dawa {
		if _, ok := dar[k]; ok {
			common = append(common, k)
		} else {
			onlyDAWA = append(onlyDAWA, k)
		}
	}
	for k := range dar {
		if _, ok := dawa[k]; !ok {
			onlyDAR = append(onlyDAR, k)
		}
	}
	sort.Strings(common)
	sort.Strings(onlyDAWA)
	sort.Strings(onlyDAR)
	return
}

// maxListed caps how many differing addresses are printed per category, to keep
// output readable while debugging.
const maxListed = 8

// reportDiffs prints each differing address with its distance from the centre
// and classifies it as a boundary effect or a real mismatch. Returns the number
// of real mismatches.
func reportDiffs(label string, keys []string, set map[string]*hjem.Address, cLat, cLon, radius, tol float64) int {
	if len(keys) == 0 {
		return 0
	}
	real := 0
	for _, k := range keys {
		a := set[k]
		lat, lon := latLon(a)
		d := hjem.HaversineMeters(cLat, cLon, lat, lon)
		if d < radius-tol || d > radius+tol {
			real++
		}
	}
	fmt.Printf("   %s (%d, showing up to %d):\n", label, len(keys), maxListed)
	for i, k := range keys {
		if i >= maxListed {
			fmt.Printf("      … and %d more\n", len(keys)-maxListed)
			break
		}
		a := set[k]
		lat, lon := latLon(a)
		d := hjem.HaversineMeters(cLat, cLon, lat, lon)
		kind := "boundary"
		if d < radius-tol || d > radius+tol {
			kind = "REAL"
		}
		fmt.Printf("      - %-55s %7.1f m  [%s]\n", a.DawaID, d, kind)
	}
	return real
}
