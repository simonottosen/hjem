package hjem

import (
	"fmt"
	"testing"
	"time"
)

// Boliga is asked per street but the cache expires per address, so these tests
// all work on one street and watch how often it is asked for and what every
// address on it — in the lookup or only in the database — is left holding.

func onStreet(street, number string, collected time.Time) *Address {
	return &Address{
		DawaUUID: "uuid-" + street + number, DawaID: street + " " + number,
		StreetName: street, StreetNumber: number,
		PostalCode: "1666", MunicipalityCode: "101",
		BoligaCollectedAt: collected,
	}
}

func soldAt(a *Address, amount int) BoligaSaleItem {
	return BoligaSaleItem{
		Addr: a.Short(), SaleType: "Alm. Salg", AmountDKK: amount,
		SqMeters: 84, Rooms: 3, BuildYear: 1932, PropertyType: PropertyApartment,
		SoldDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
}

// countingFetch answers every street with the given sales and counts the
// streets it was asked for, which is what each fetch costs against Boliga.
func countingFetch(asked *int, items ...BoligaSaleItem) BoligaStreetFetcher {
	return func(tasks []BoligaPropertyRequest, _ *Progress, _ *HealthStats) ([]BoligaSaleItem, []BoligaStreetFailure, error) {
		*asked += len(tasks)
		return items, nil, nil
	}
}

// reload reads addresses back the way the next lookup gets them.
func reload(t *testing.T, bc *boligaCacher, addrs ...*Address) []*Address {
	t.Helper()
	out := make([]*Address, len(addrs))
	for i, a := range addrs {
		var stored Address
		if err := bc.db.First(&stored, a.ID).Error; err != nil {
			t.Fatalf("read back %s: %v", a.DawaID, err)
		}
		out[i] = &stored
	}
	return out
}

func storedSales(t *testing.T, bc *boligaCacher, a *Address) []Sale {
	t.Helper()
	var sales []Sale
	if err := bc.db.Where("addr_id = ?", a.ID).Find(&sales).Error; err != nil {
		t.Fatalf("read sales of %s: %v", a.DawaID, err)
	}
	return sales
}

func seedSale(t *testing.T, bc *boligaCacher, a *Address, amount int) {
	t.Helper()
	if err := bc.db.Create(&Sale{AddrID: a.ID, AmountDKK: amount}).Error; err != nil {
		t.Fatalf("seed sale for %s: %v", a.DawaID, err)
	}
}

// Two lookups from different starting points that share a street. The second
// reaches an address on it the first did not, but the first already had the
// whole street in hand, so the second must not ask Boliga for it again.
func TestOverlappingLookupsFetchStreetOnce(t *testing.T) {
	a1 := onStreet("Gade", "1", time.Time{})
	a2 := onStreet("Gade", "2", time.Time{})
	a3 := onStreet("Gade", "3", time.Time{})
	bc := seedCacher(t, []*Address{a1, a2, a3})

	var asked int
	fetch := countingFetch(&asked, soldAt(a3, 4_000_000))

	if _, _, err := bc.FetchSales([]*Address{a1, a2}, NewProgress(), NewHealthStats(), fetch); err != nil {
		t.Fatalf("first FetchSales: %v", err)
	}
	second := reload(t, bc, a2, a3)
	if _, _, err := bc.FetchSales(second, NewProgress(), NewHealthStats(), fetch); err != nil {
		t.Fatalf("second FetchSales: %v", err)
	}
	if asked != 1 {
		t.Errorf("one street was asked for %d times by two lookups within the TTL, want once", asked)
	}
}

// The other half of the same case: the address only the second lookup reaches
// has to have kept the sale the first lookup's fetch turned up, and be stamped
// with that fetch, or the sale is thrown away and the street re-fetched.
func TestStoredNeighbourGetsSaleFromStreetFetch(t *testing.T) {
	a1 := onStreet("Gade", "1", time.Time{})
	a2 := onStreet("Gade", "2", time.Time{})
	outside := onStreet("Gade", "3", time.Time{})
	bc := seedCacher(t, []*Address{a1, a2, outside})

	var asked int
	if _, _, err := bc.FetchSales([]*Address{a1, a2}, NewProgress(), NewHealthStats(),
		countingFetch(&asked, soldAt(outside, 4_000_000))); err != nil {
		t.Fatalf("FetchSales: %v", err)
	}

	got := reload(t, bc, a1, outside)
	if !got[1].BoligaCollectedAt.Equal(got[0].BoligaCollectedAt) {
		t.Errorf("%s stamped %v, %s stamped %v — one street fetch must stamp the whole street",
			got[1].DawaID, got[1].BoligaCollectedAt, got[0].DawaID, got[0].BoligaCollectedAt)
	}
	sales := storedSales(t, bc, outside)
	if len(sales) != 1 || sales[0].AmountDKK != 4_000_000 {
		t.Errorf("%s holds %v, want the 4,000,000 sale the street fetch found", outside.DawaID, sales)
	}
	if got[1].BoligaBuildingSize != 84 {
		t.Errorf("%s kept %dm², want 84 — the sale's metadata was dropped", outside.DawaID, got[1].BoligaBuildingSize)
	}

	// And through the cache, as the lookup that does reach it would see it.
	res, _, err := bc.FetchSales(reload(t, bc, outside), NewProgress(), NewHealthStats(), countingFetch(&asked))
	if err != nil {
		t.Fatalf("second FetchSales: %v", err)
	}
	if asked != 1 || len(res[0]) != 1 || res[0][0].AmountDKK != 4_000_000 {
		t.Errorf("second lookup asked %d streets in all and got %v, want 1 street and the cached sale", asked, res[0])
	}
}

// Once one address on a street expires the street is fetched once, and every
// address known on it leaves with that fetch's stamp and that fetch's sales —
// the lookup's still-cached one, and one only the database holds. Otherwise
// their older stamps each expire on their own day and fetch the street again.
func TestExpiredStreetRestampsEveryKnownAddress(t *testing.T) {
	ago := func(d time.Duration) time.Time { return time.Now().Add(-d).Truncate(time.Second) }
	expired := onStreet("Gade", "1", ago(2*cacheExpiry))
	cached := onStreet("Gade", "2", ago(cacheExpiry/3))
	stored := onStreet("Gade", "3", ago(cacheExpiry/2))
	bc := seedCacher(t, []*Address{expired, cached, stored})
	seedSale(t, bc, expired, 1_000_000)
	seedSale(t, bc, cached, 2_000_000)
	seedSale(t, bc, stored, 3_000_000)

	var asked int
	fetch := countingFetch(&asked, soldAt(cached, 2_500_000), soldAt(stored, 3_500_000))
	sales, _, err := bc.FetchSales([]*Address{expired, cached}, NewProgress(), NewHealthStats(), fetch)
	if err != nil {
		t.Fatalf("FetchSales: %v", err)
	}
	if asked != 1 {
		t.Fatalf("asked for %d streets, want 1", asked)
	}

	got := reload(t, bc, expired, cached, stored)
	for _, a := range got[1:] {
		if !a.BoligaCollectedAt.Equal(got[0].BoligaCollectedAt) {
			t.Errorf("%s stamped %v, %s stamped %v — the street was fetched once and must be stamped once",
				a.DawaID, a.BoligaCollectedAt, got[0].DawaID, got[0].BoligaCollectedAt)
		}
	}
	// The lookup's own struct too: the page's freshness note is read from it.
	if !cached.BoligaCollectedAt.Equal(got[0].BoligaCollectedAt) {
		t.Errorf("the lookup's copy of %s still says %v", cached.DawaID, cached.BoligaCollectedAt)
	}

	// Replaced, not added to: each address holds exactly what the fetch said.
	for _, c := range []struct {
		a    *Address
		want []int
	}{{expired, nil}, {cached, []int{2_500_000}}, {stored, []int{3_500_000}}} {
		var amounts []int
		for _, s := range storedSales(t, bc, c.a) {
			amounts = append(amounts, s.AmountDKK)
		}
		if fmt.Sprint(amounts) != fmt.Sprint(c.want) {
			t.Errorf("%s holds sales %v, want %v", c.a.DawaID, amounts, c.want)
		}
	}
	if len(sales[1]) != 1 || sales[1][0].AmountDKK != 2_500_000 {
		t.Errorf("the lookup got %v for %s, want the freshly fetched 2,500,000 sale", sales[1], cached.DawaID)
	}

	if _, _, err := bc.FetchSales(reload(t, bc, cached, stored), NewProgress(), NewHealthStats(), fetch); err != nil {
		t.Fatalf("second FetchSales: %v", err)
	}
	if asked != 1 {
		t.Errorf("the street was asked for again right after it was fetched")
	}
}

// A street that does not come back was not checked, so nothing on it may
// change: not the due address's sales, not its still-cached neighbour's, and
// not a stored neighbour's stamp.
func TestFailedStreetLeavesSalesAndStamps(t *testing.T) {
	ago := func(d time.Duration) time.Time { return time.Now().Add(-d).Truncate(time.Second) }
	expired := onStreet("Gade", "1", ago(2*cacheExpiry))
	cached := onStreet("Gade", "2", ago(cacheExpiry/3))
	stored := onStreet("Gade", "3", ago(cacheExpiry/2))
	other := onStreet("Vej", "1", time.Time{})
	bc := seedCacher(t, []*Address{expired, cached, stored, other})
	seedSale(t, bc, expired, 1_000_000)
	seedSale(t, bc, cached, 2_000_000)
	seedSale(t, bc, stored, 3_000_000)

	fetch := func(tasks []BoligaPropertyRequest, _ *Progress, _ *HealthStats) ([]BoligaSaleItem, []BoligaStreetFailure, error) {
		var failures []BoligaStreetFailure
		for _, tk := range tasks {
			if tk.StreetName == "Gade" {
				failures = append(failures, BoligaStreetFailure{Task: tk, Err: fmt.Errorf("status 429")})
			}
		}
		// A sale whose address string also fits the failed street must not
		// land there either.
		return []BoligaSaleItem{soldAt(stored, 9_000_000)}, failures, nil
	}
	sales, _, err := bc.FetchSales([]*Address{expired, cached, other}, NewProgress(), NewHealthStats(), fetch)
	if err != nil {
		t.Fatalf("FetchSales: %v", err)
	}

	before := []*Address{expired, cached, stored}
	seeded := []int{1_000_000, 2_000_000, 3_000_000}
	for i, a := range reload(t, bc, before...) {
		if !a.BoligaCollectedAt.Equal(before[i].BoligaCollectedAt) {
			t.Errorf("%s stamped %v after its street failed, want %v kept",
				a.DawaID, a.BoligaCollectedAt, before[i].BoligaCollectedAt)
		}
		s := storedSales(t, bc, a)
		if len(s) != 1 || s[0].AmountDKK != seeded[i] {
			t.Errorf("%s holds %v after its street failed, want its %d sale untouched", a.DawaID, s, seeded[i])
		}
	}
	if len(sales[1]) != 1 || sales[1][0].AmountDKK != 2_000_000 {
		t.Errorf("the lookup got %v for the cached %s, want its stored sale", sales[1], cached.DawaID)
	}
}

// A fetch that fails outright abandons the lookup. It must not take the
// expired addresses' sales with it: they are still the best there is, and the
// next search retries the street anyway.
func TestFetchErrorDeletesNoSales(t *testing.T) {
	expired := onStreet("Gade", "1", time.Now().Add(-2*cacheExpiry))
	bc := seedCacher(t, []*Address{expired})
	seedSale(t, bc, expired, 1_000_000)

	fetch := func(tasks []BoligaPropertyRequest, _ *Progress, _ *HealthStats) ([]BoligaSaleItem, []BoligaStreetFailure, error) {
		return nil, []BoligaStreetFailure{{Task: tasks[0], Err: fmt.Errorf("status 429")}}, fmt.Errorf("alle 1 gade-opslag fejlede")
	}
	if _, _, err := bc.FetchSales([]*Address{expired}, NewProgress(), NewHealthStats(), fetch); err == nil {
		t.Fatal("FetchSales succeeded although every street failed")
	}
	if s := storedSales(t, bc, expired); len(s) != 1 {
		t.Errorf("%s holds %d sales after a failed fetch, want its 1 sale kept", expired.DawaID, len(s))
	}
}

// Two streets of one name in different postcodes, both due. Their addresses
// share an address string, and matching has no postcode to tell them apart, so
// a stored Skolevej 3 in one village must not take the other village's sale
// and lose its own.
func TestStoredNeighbourSharingAddressStringIsLeftAlone(t *testing.T) {
	inVillage := func(zip, number string, collected time.Time) *Address {
		a := onStreet("Skolevej", number, collected)
		a.DawaUUID += "-" + zip
		a.DawaID += ", " + zip
		a.PostalCode = zip
		return a
	}
	stamped := time.Now().Add(-cacheExpiry / 2).Truncate(time.Second)
	due4000 := inVillage("4000", "1", time.Time{})
	due4100 := inVillage("4100", "3", time.Time{})
	stored := inVillage("4000", "3", stamped)
	bc := seedCacher(t, []*Address{due4000, due4100, stored})
	seedSale(t, bc, stored, 3_000_000)

	var asked int
	sales, _, err := bc.FetchSales([]*Address{due4000, due4100}, NewProgress(), NewHealthStats(),
		countingFetch(&asked, soldAt(due4100, 9_000_000)))
	if err != nil {
		t.Fatalf("FetchSales: %v", err)
	}
	if asked != 2 {
		t.Fatalf("asked for %d streets, want both Skolevej", asked)
	}

	got := reload(t, bc, stored)[0]
	if !got.BoligaCollectedAt.Equal(stamped) {
		t.Errorf("%s stamped %v, want %v kept", got.DawaID, got.BoligaCollectedAt, stamped)
	}
	if s := storedSales(t, bc, stored); len(s) != 1 || s[0].AmountDKK != 3_000_000 {
		t.Errorf("%s holds %v, want its own 3,000,000 sale", stored.DawaID, s)
	}
	if len(sales[1]) != 1 || sales[1][0].AmountDKK != 9_000_000 {
		t.Errorf("the lookup's %s got %v, want the 9,000,000 sale", due4100.DawaID, sales[1])
	}
}
