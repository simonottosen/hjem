package hjem

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// The summary line rests on one distinction, so the fixture contains both
// kinds of miss. Firhøj 78 is on a street we queried but far up it, which is
// Boliga answering a street query with the whole street; Egedalsvej 1, 3. tv
// stands on a building we hold, and a radius query returns every unit at a
// point, so nothing but a defect explains it not matching. Sale amounts play
// no part in matching and are left at zero.
func TestMatchSeparatesOutOfRadiusFromMismatch(t *testing.T) {
	floor, door := "2", "th"
	addrs := []*Address{
		{StreetName: "Firhøj", StreetNumber: "4"},
		{StreetName: "Firhøj", StreetNumber: "6"},
		{StreetName: "Egedalsvej", StreetNumber: "1", Floor: &floor, Door: &door},
	}

	sales := []BoligaSaleItem{
		clientSale("Firhøj 4", 0),                 // exact
		clientSale("Egedalsvej 1, 2. th", 0),      // exact
		clientSale("Egedalsvej 1,2. th", 0),       // normalized
		clientSale("Firhøj 78", 0),                // queried street, outside the radius
		clientSale("Karlslunde Parkvej 12", 0),    // street we never held
		clientSale("Egedalsvej 1, 3. tv", 0),      // building we hold, unit we do not
		{Addr: "Firhøj 6", SaleType: "Fam. Salg"}, // skipped before any matching
	}

	var logged bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(prev)

	result := matchSalesToAddrs(addrs, sales)

	if got := len(result[0]); got != 1 {
		t.Errorf("Firhøj 4: got %d sales, want 1", got)
	}
	if got := len(result[1]); got != 0 {
		t.Errorf("Firhøj 6: got %d sales, want 0", got)
	}
	if got := len(result[2]); got != 2 {
		t.Errorf("Egedalsvej 1, 2. th: got %d sales, want 2", got)
	}

	want := "matched 2 exact + 1 normalized = 3/7 sales (skipped 1 non-alm. salg, 2 outside the radius as expected, 1 unmatched at buildings inside it)"
	if !strings.Contains(logged.String(), want) {
		t.Errorf("summary does not separate the two buckets:\ngot:  %s\nwant substring: %s", logged.String(), want)
	}
	if !strings.Contains(logged.String(), "Egedalsvej 1, 3. tv") {
		t.Errorf("mismatch example missing from log:\n%s", logged.String())
	}
	for _, expected := range []string{"Firhøj 78", "Karlslunde Parkvej 12"} {
		if strings.Contains(logged.String(), expected) {
			t.Errorf("%q is outside the radius and must not be reported as unmatched:\n%s", expected, logged.String())
		}
	}
}

func TestBuildingKeyIgnoresFloorAndDoor(t *testing.T) {
	tt := []struct {
		in  string
		out string
	}{
		{in: "Firhøj 4", out: "firhøj 4"},
		{in: "Firhøj 4, 2. th", out: "firhøj 4"},
		{in: "Firhøj 4 th", out: "firhøj 4"},
		{in: "Firhøj 4A, st. tv", out: "firhøj 4a"},
		{in: "Firhøj 78", out: "firhøj 78"},
	}

	for _, tc := range tt {
		if got := buildingKey(tc.in); got != tc.out {
			t.Errorf("buildingKey(%q) = %q, want %q", tc.in, got, tc.out)
		}
	}
}
