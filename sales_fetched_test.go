package hjem

import (
	"testing"
	"time"
)

// The page states how current its sales are, and that has to be its least
// recently checked address: one response mixes addresses fetched by different
// lookups days apart. That includes addresses with no sale — they are dropped
// from the response, but one checked days ago is exactly where a sale
// registered since then would be missing.
func TestSalesFetchedAtIsTheOldestCheck(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 12, 0, 0, 0, time.UTC) }

	primary := testAddr("primary", "Flensborggade", "40")
	primary.BoligaCollectedAt = day(3)
	sold := testAddr("neighbour-a", "Flensborggade", "34")
	sold.BoligaCollectedAt = day(2)
	unsold := testAddr("neighbour-b", "Flensborggade", "24")
	unsold.BoligaCollectedAt = day(1)

	sales := [][]Sale{
		{testSale(5_550_000, 2024)},
		{testSale(4_000_000, 2025)},
		nil,
	}

	resp, err := FormatLookupResponse([]*Address{primary, sold, unsold}, map[int][]*Address{}, sales, 0)
	if err != nil {
		t.Fatalf("FormatLookupResponse: %v", err)
	}
	if resp.SalesFetchedAt == nil {
		t.Fatal("no sales_fetched_at on a response whose addresses were all fetched")
	}
	if !resp.SalesFetchedAt.Equal(day(1)) {
		t.Errorf("sales_fetched_at %v, want %v — the oldest check, including addresses without sales",
			resp.SalesFetchedAt, day(1))
	}
}
