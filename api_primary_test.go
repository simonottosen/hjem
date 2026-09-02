package hjem

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// testAddr builds the minimum Address that FormatLookupResponse and its
// downstream statistics need: an identity (DawaID is the key the ranges map is
// rebuilt from) and a building size (a zero size makes SalesStatistics skip
// every sale).
func testAddr(dawaID, street, number string) *Address {
	return &Address{
		DawaID:             dawaID,
		StreetName:         street,
		StreetNumber:       number,
		PostalCode:         "1669",
		MunicipalityCode:   "0101",
		Latitude:           12.55,
		Longtitude:         55.66,
		BoligaBuildingSize: 60,
		BoligaRooms:        2,
		BoligaBuiltYear:    1900,
	}
}

func testSale(amount int, year int) Sale {
	return Sale{
		AmountDKK: amount,
		SqMeters:  60,
		Rooms:     2,
		BuildYear: 1900,
		Date:      time.Date(year, 6, 1, 0, 0, 0, 0, time.UTC),
	}
}

// The searched address is addrs[0] all the way into FormatLookupResponse, and
// primary_idx tells the frontend which entry of the response is that home. A
// home that has never been sold has no sales of its own, which must not cost it
// its place in the response: everything the frontend keys off primary_idx —
// the displayed subject property, the comps estimate, the sqm projections —
// would otherwise silently describe a neighbour's flat.
func TestFormatLookupResponseKeepsUnsoldPrimary(t *testing.T) {
	addrs := []*Address{
		testAddr("Flensborggade 40, st. tv, 1669 København V", "Flensborggade", "40"),
		testAddr("Flensborggade 34, 1. tv, 1669 København V", "Flensborggade", "34"),
		testAddr("Flensborggade 34, 2. th, 1669 København V", "Flensborggade", "34"),
	}
	// The primary has no sales; the two neighbours do.
	sales := [][]Sale{
		nil,
		{testSale(3_000_000, 2023)},
		{testSale(3_200_000, 2024)},
	}
	ranges := map[int][]*Address{100: {addrs[1], addrs[2]}}

	resp, err := FormatLookupResponse(addrs, ranges, sales, 0)
	if err != nil {
		t.Fatalf("FormatLookupResponse: %v", err)
	}

	if resp.PrimaryIndex < 0 || resp.PrimaryIndex >= len(resp.Addrs) {
		t.Fatalf("primary_idx %d is out of range for %d addresses", resp.PrimaryIndex, len(resp.Addrs))
	}
	if got := resp.Addrs[resp.PrimaryIndex]; got.DawaID != addrs[0].DawaID {
		t.Errorf("primary_idx points at %q, want the searched address %q", got.DawaID, addrs[0].DawaID)
	}

	// The neighbours must still be there — keeping the primary is not allowed
	// to come at the cost of dropping comps.
	if len(resp.Addrs) != 3 {
		t.Errorf("got %d addresses, want 3", len(resp.Addrs))
	}
}

// An unsold primary contributes no sales, so every sale in the response must
// still belong to whichever address it was fetched for. Keeping the primary in
// the slice shifts every other address's index, and addr_idx has to shift with
// it or each sale is attributed to the wrong home.
func TestFormatLookupResponseSaleIndicesFollowAddresses(t *testing.T) {
	addrs := []*Address{
		testAddr("primary", "Flensborggade", "40"),
		testAddr("neighbour-a", "Flensborggade", "34"),
		testAddr("neighbour-b", "Istedgade", "12"),
	}
	sales := [][]Sale{
		nil,
		{testSale(3_000_000, 2023)},
		{testSale(9_900_000, 2024)},
	}

	resp, err := FormatLookupResponse(addrs, map[int][]*Address{}, sales, 0)
	if err != nil {
		t.Fatalf("FormatLookupResponse: %v", err)
	}

	// Each amount identifies the address it came from.
	want := map[int]string{3_000_000: "neighbour-a", 9_900_000: "neighbour-b"}
	for _, s := range resp.Sales {
		if s.AddrIndex < 0 || s.AddrIndex >= len(resp.Addrs) {
			t.Fatalf("sale %d has addr_idx %d, out of range for %d addresses", s.Amount, s.AddrIndex, len(resp.Addrs))
		}
		if got := resp.Addrs[s.AddrIndex].DawaID; got != want[s.Amount] {
			t.Errorf("sale %d attributed to %q, want %q", s.Amount, got, want[s.Amount])
		}
	}
}

// The ranges map is rebuilt from DawaID, and it is what the frontend uses to
// decide which addresses fall inside the chosen radius. Reindexing must keep
// those references pointing at the same homes.
func TestFormatLookupResponseRangesFollowAddresses(t *testing.T) {
	addrs := []*Address{
		testAddr("primary", "Flensborggade", "40"),
		testAddr("neighbour-a", "Flensborggade", "34"),
		testAddr("neighbour-b", "Istedgade", "12"),
	}
	sales := [][]Sale{
		nil,
		{testSale(3_000_000, 2023)},
		{testSale(3_200_000, 2024)},
	}
	ranges := map[int][]*Address{100: {addrs[1]}, 250: {addrs[1], addrs[2]}}

	resp, err := FormatLookupResponse(addrs, ranges, sales, 0)
	if err != nil {
		t.Fatalf("FormatLookupResponse: %v", err)
	}

	got := map[int][]string{}
	for meters, idxs := range resp.Ranges {
		for _, idx := range idxs {
			if idx < 0 || idx >= len(resp.Addrs) {
				t.Fatalf("range %d references addr_idx %d, out of range for %d addresses", meters, idx, len(resp.Addrs))
			}
			got[meters] = append(got[meters], resp.Addrs[idx].DawaID)
		}
	}
	if len(got[100]) != 1 || got[100][0] != "neighbour-a" {
		t.Errorf("range 100 resolved to %v, want [neighbour-a]", got[100])
	}
	if len(got[250]) != 2 {
		t.Errorf("range 250 resolved to %v, want 2 addresses", got[250])
	}
}

// withNeighbours puts primary at index 0 and appends enough sold neighbours to
// clear minComps, so that a nil comps estimate in these tests means the subject
// property was the obstacle and not a shortage of comparable sales.
func withNeighbours(primary *Address, primarySales []Sale, n int) ([]*Address, [][]Sale) {
	addrs := []*Address{primary}
	sales := [][]Sale{primarySales}
	for i := 0; i < n; i++ {
		addrs = append(addrs, testAddr(fmt.Sprintf("neighbour-%d", i), "Flensborggade", "34"))
		sales = append(sales, []Sale{testSale(3_000_000+i*50_000, 2023)})
	}
	return addrs, sales
}

// A size only ever reaches us from a matched Boliga sale carrying square
// metres, so without one the home cannot be valued. Correcting primary_idx
// makes that visible for the first time — the estimate used to be computed
// against a sold neighbour instead — so the response has to explain the gap
// rather than just leaving the headline number missing.
func TestFormatLookupResponseWarnsWhenPrimarySizeUnknown(t *testing.T) {
	primary := testAddr("primary", "Flensborggade", "40")
	primary.BoligaBuildingSize = 0 // Boliga never gave us a usable size
	addrs, sales := withNeighbours(primary, nil, 4)

	resp, err := FormatLookupResponse(addrs, map[int][]*Address{}, sales, 0)
	if err != nil {
		t.Fatalf("FormatLookupResponse: %v", err)
	}

	if resp.CompsEstimate != nil {
		t.Errorf("got a comps estimate of %v for a home of unknown size, want none", resp.CompsEstimate.Value)
	}
	var explained bool
	for _, w := range resp.Warnings {
		if strings.Contains(w, "brugbar størrelse") {
			explained = true
		}
	}
	if !explained {
		t.Errorf("no warning explains the missing estimate; got %v", resp.Warnings)
	}
}

// The same response carries Boliga fetch failures, which the caller appends.
// Neither set of warnings may drop the other.
func TestFormatLookupResponseKeepsSizeWarningAlongsideFetchWarnings(t *testing.T) {
	primary := testAddr("primary", "Flensborggade", "40")
	primary.BoligaBuildingSize = 0
	addrs, sales := withNeighbours(primary, nil, 4)

	resp, err := FormatLookupResponse(addrs, map[int][]*Address{}, sales, 0)
	if err != nil {
		t.Fatalf("FormatLookupResponse: %v", err)
	}

	// Mirrors what runLookup does with the warnings FetchSales returns.
	resp.Warnings = append(resp.Warnings, "Kunne ikke hente salg for Enghavevej")

	if len(resp.Warnings) != 2 {
		t.Fatalf("got %d warnings, want the size warning and the fetch warning: %v", len(resp.Warnings), resp.Warnings)
	}
}

// A missing size is not the same thing as never having been sold. Boliga can
// return sales that carry no square metres, and it drops family sales
// altogether (boliga.go:313), so a home with a sales history of its own can
// still arrive without a size. The warning has to fire on the size it is
// actually about, and must not tell this user their home was never sold.
func TestFormatLookupResponseWarnsWhenSoldPrimaryHasNoSize(t *testing.T) {
	primary := testAddr("primary", "Flensborggade", "40")
	primary.BoligaBuildingSize = 0
	sizeless := testSale(2_900_000, 2022)
	sizeless.SqMeters = 0
	addrs, sales := withNeighbours(primary, []Sale{sizeless}, 4)

	resp, err := FormatLookupResponse(addrs, map[int][]*Address{}, sales, 0)
	if err != nil {
		t.Fatalf("FormatLookupResponse: %v", err)
	}

	var explained bool
	for _, w := range resp.Warnings {
		if strings.Contains(w, "brugbar størrelse") {
			explained = true
		}
		if strings.Contains(w, "ikke har været solgt") && !strings.Contains(w, "ofte fordi") {
			t.Errorf("warning states this home was never sold, but it has a sale: %q", w)
		}
	}
	if !explained {
		t.Errorf("no warning explains the missing size; got %v", resp.Warnings)
	}
}

// A home with a known size is valued as before: correcting primary_idx must
// not disturb the ordinary case, only the one it was getting wrong.
func TestFormatLookupResponseValuesSizedPrimary(t *testing.T) {
	primary := testAddr("primary", "Flensborggade", "40")
	addrs, sales := withNeighbours(primary, []Sale{testSale(2_900_000, 2022)}, 4)

	resp, err := FormatLookupResponse(addrs, map[int][]*Address{}, sales, 0)
	if err != nil {
		t.Fatalf("FormatLookupResponse: %v", err)
	}

	if resp.CompsEstimate == nil {
		t.Fatal("no comps estimate for a primary of known size")
	}
	for _, w := range resp.Warnings {
		if strings.Contains(w, "brugbar størrelse") {
			t.Errorf("warned about an unknown size for a primary of 60 m²: %q", w)
		}
	}
}

// Addresses that are neither the primary nor have any sales carry no
// information: they would render as empty rows and inflate the address list.
func TestFormatLookupResponseDropsUnsoldNeighbours(t *testing.T) {
	addrs := []*Address{
		testAddr("primary", "Flensborggade", "40"),
		testAddr("unsold-neighbour", "Flensborggade", "34"),
		testAddr("sold-neighbour", "Istedgade", "12"),
	}
	sales := [][]Sale{
		{testSale(2_800_000, 2022)},
		nil,
		{testSale(3_200_000, 2024)},
	}

	resp, err := FormatLookupResponse(addrs, map[int][]*Address{}, sales, 0)
	if err != nil {
		t.Fatalf("FormatLookupResponse: %v", err)
	}

	for _, a := range resp.Addrs {
		if a.DawaID == "unsold-neighbour" {
			t.Fatal("an unsold neighbour was kept in the response")
		}
	}
	if resp.Addrs[resp.PrimaryIndex].DawaID != "primary" {
		t.Errorf("primary_idx points at %q, want primary", resp.Addrs[resp.PrimaryIndex].DawaID)
	}
}
