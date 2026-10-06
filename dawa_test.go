package hjem

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// stubRequest is a DawaRequest whose Fetch outcome the test controls.
type stubRequest struct {
	url   string
	addrs []*Address
	err   error
	calls *int
}

func (s stubRequest) Request() *http.Request {
	req, _ := http.NewRequest("GET", s.url, nil)
	return req
}

func (s stubRequest) MaxAge() time.Duration { return 365 * 24 * time.Hour }

func (s stubRequest) Fetch() ([]*Address, error) {
	if s.calls != nil {
		*s.calls++
	}
	return s.addrs, s.err
}

func newTestCacher(t *testing.T) *dawaCacher {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			sqlDB.Close()
		}
	})
	return NewDawaCacher(db)
}

// TestDawaCacherDoesNotCacheFailedFetch is a regression test. The cache TTL is
// 365 days, so writing an entry for a failed fetch turned any transient upstream
// outage into a permanent "no results" for that query.
func TestDawaCacherDoesNotCacheFailedFetch(t *testing.T) {
	c := newTestCacher(t)

	const url = "https://example.invalid/adresser/soeg?tekst=test"
	fetchErr := errors.New("upstream is down")

	if _, err := c.Do(stubRequest{url: url, err: fetchErr}); !errors.Is(err, fetchErr) {
		t.Fatalf("Do returned %v, want the fetch error to propagate", err)
	}

	var n int64
	if err := c.db.Model(&DawaQueryCache{}).Where("query = ?", url).Count(&n).Error; err != nil {
		t.Fatalf("count cache rows: %v", err)
	}
	if n != 0 {
		t.Fatalf("a failed fetch wrote %d cache row(s); it must write none", n)
	}

	// The same query must now be retried and succeed, rather than being served
	// an empty cached result.
	calls := 0
	addrs, err := c.Do(stubRequest{
		url:   url,
		calls: &calls,
		addrs: []*Address{{
			DawaUUID:         "70865c44-d570-44e7-a6f5-6f7c90add725",
			DawaID:           "Rådhuspladsen 1, 1550 København V",
			StreetName:       "Rådhuspladsen",
			StreetNumber:     "1",
			PostalCode:       "1550",
			MunicipalityCode: "0101",
		}},
	})
	if err != nil {
		t.Fatalf("retry after a failure: %v", err)
	}
	if calls != 1 {
		t.Errorf("Fetch called %d times, want 1 — the failure must not have been cached", calls)
	}
	if len(addrs) != 1 || addrs[0].StreetName != "Rådhuspladsen" {
		t.Fatalf("got %+v, want the freshly fetched address", addrs)
	}
}

// TestDawaCacherCachesSuccess pins the other half of the contract: a successful
// fetch is cached, and a repeat query is served from the DB without refetching.
func TestDawaCacherCachesSuccess(t *testing.T) {
	c := newTestCacher(t)

	const url = "https://example.invalid/adresser/soeg?tekst=cached"
	calls := 0
	req := stubRequest{
		url:   url,
		calls: &calls,
		addrs: []*Address{{
			DawaUUID:         "abc",
			DawaID:           "Testvej 2, 8000 Aarhus C",
			StreetName:       "Testvej",
			StreetNumber:     "2",
			PostalCode:       "8000",
			MunicipalityCode: "0751",
		}},
	}

	if _, err := c.Do(req); err != nil {
		t.Fatalf("first Do: %v", err)
	}

	addrs, err := c.Do(req)
	if err != nil {
		t.Fatalf("second Do: %v", err)
	}
	if calls != 1 {
		t.Errorf("Fetch called %d times, want 1 — the second call should hit the cache", calls)
	}
	if len(addrs) != 1 || addrs[0].DawaID != "Testvej 2, 8000 Aarhus C" {
		t.Fatalf("cached read returned %+v", addrs)
	}
}

// TestDawaCacherDoesNotCacheEmptyResult covers #28. A resolution that finds
// nothing is a *successful* fetch, so it used to be cached for the full 365-day
// MaxAge — pinning "this address does not exist" for a year. New construction
// and improvements to the fallback chain both make that answer go stale.
func TestDawaCacherDoesNotCacheEmptyResult(t *testing.T) {
	c := newTestCacher(t)

	const url = "https://example.invalid/adresser/soeg?tekst=nyt-byggeri"

	addrs, err := c.Do(stubRequest{url: url, addrs: nil})
	if err != nil {
		t.Fatalf("Do with an empty result: %v", err)
	}
	if len(addrs) != 0 {
		t.Fatalf("got %d addresses, want 0", len(addrs))
	}

	var n int64
	if err := c.db.Model(&DawaQueryCache{}).Where("query = ?", url).Count(&n).Error; err != nil {
		t.Fatalf("count cache rows: %v", err)
	}
	if n != 0 {
		t.Fatalf("an empty result wrote %d cache row(s); it must write none", n)
	}

	// Once the address is registered upstream, the same query must resolve it
	// rather than being served the year-old "not found".
	calls := 0
	addrs, err = c.Do(stubRequest{
		url:   url,
		calls: &calls,
		addrs: []*Address{{
			DawaUUID:         "new-uuid",
			DawaID:           "Nybyggetvej 1, 2300 København S",
			StreetName:       "Nybyggetvej",
			StreetNumber:     "1",
			PostalCode:       "2300",
			MunicipalityCode: "0101",
		}},
	})
	if err != nil {
		t.Fatalf("retry after an empty result: %v", err)
	}
	if calls != 1 {
		t.Errorf("Fetch called %d times, want 1 — the empty result must not have been cached", calls)
	}
	if len(addrs) != 1 || addrs[0].StreetName != "Nybyggetvej" {
		t.Fatalf("got %+v, want the freshly resolved address", addrs)
	}
}

// TestDawaCacherHealsPreexistingEmptyEntry covers the migration half of #28.
// Deployments already hold empty entries written before empty results stopped
// being cached — notably queries cached as "not found" before the fallback
// chain (#18, #19) could resolve them. Those must be re-fetched, not served.
func TestDawaCacherHealsPreexistingEmptyEntry(t *testing.T) {
	c := newTestCacher(t)

	const url = "https://example.invalid/adresser/soeg?tekst=poisoned"

	// Simulate the old behaviour: a fresh, empty, non-expired entry.
	if err := c.db.Create(&DawaQueryCache{
		Query:     url,
		IDs:       "",
		CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed empty cache entry: %v", err)
	}

	calls := 0
	addrs, err := c.Do(stubRequest{
		url:   url,
		calls: &calls,
		addrs: []*Address{{
			DawaUUID:         "healed-uuid",
			DawaID:           "Højgaardsvej 3B, 4760 Vordingborg",
			StreetName:       "Højgaardsvej",
			StreetNumber:     "3B",
			PostalCode:       "4760",
			MunicipalityCode: "0390",
		}},
	})
	if err != nil {
		t.Fatalf("Do over a poisoned entry: %v", err)
	}
	if calls != 1 {
		t.Fatalf("Fetch called %d times, want 1 — the stale empty entry was served instead of refetched", calls)
	}
	if len(addrs) != 1 || addrs[0].StreetName != "Højgaardsvej" {
		t.Fatalf("got %+v, want the re-resolved address", addrs)
	}
}

const addrUniqueIndex = "idx_addresses_dawa_uuid"

// newTestDB gives a test its own database. The shared in-memory DSN used by
// newTestCacher is process-wide, which the tests below cannot tolerate: they
// seed conflicting rows and re-run the migration.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "hjem.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			sqlDB.Close()
		}
	})
	return db
}

func countAddrs(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&Address{}).Count(&n).Error; err != nil {
		t.Fatalf("count addresses: %v", err)
	}
	return n
}

// TestSafeCreateOrGetAddrsDedupesOnUUID covers #21. DawaID holds a formatted
// address string, and DAWA's betegnelse and DAR's adressebetegnelse need not
// agree on a comma or a floor abbreviation. Keying the dedupe on it turned one
// physical address into two rows, silently, with no constraint to catch it.
func TestSafeCreateOrGetAddrsDedupesOnUUID(t *testing.T) {
	c := NewDawaCacher(newTestDB(t))

	const uuid = "70865c44-d570-44e7-a6f5-6f7c90add725"
	dawaSpelling := []*Address{{
		DawaUUID:         uuid,
		DawaID:           "Testvej 1, 1. tv, 8000 Aarhus C",
		StreetName:       "Testvej",
		StreetNumber:     "1",
		PostalCode:       "8000",
		MunicipalityCode: "0751",
	}}
	if err := c.safeCreateOrGetAddrs(dawaSpelling); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	darSpelling := []*Address{{
		DawaUUID:         uuid,
		DawaID:           "Testvej 1, 1 tv, 8000 Aarhus C",
		StreetName:       "Testvej",
		StreetNumber:     "1",
		PostalCode:       "8000",
		MunicipalityCode: "0751",
	}}
	if err := c.safeCreateOrGetAddrs(darSpelling); err != nil {
		t.Fatalf("second insert: %v", err)
	}

	if n := countAddrs(t, c.db); n != 1 {
		t.Fatalf("%d address rows, want 1 — the two spellings are the same address", n)
	}
	if darSpelling[0].ID != dawaSpelling[0].ID {
		t.Errorf("resolved to row %d, want the existing row %d", darSpelling[0].ID, dawaSpelling[0].ID)
	}
}

// TestSafeCreateOrGetAddrsBackfillsPreUUIDRow covers the rows written before
// DawaUUID was populated. They can only be matched on DawaID, so that match has
// to stay available for them — but per row, never through a single blank-UUID
// key, which would fold every one of them together.
func TestSafeCreateOrGetAddrsBackfillsPreUUIDRow(t *testing.T) {
	c := NewDawaCacher(newTestDB(t))

	legacy := []*Address{
		{DawaID: "Gammelvej 2, 2. th, 5000 Odense C", StreetName: "Gammelvej", StreetNumber: "2", PostalCode: "5000", MunicipalityCode: "0461"},
		{DawaID: "Gammelvej 4, 5000 Odense C", StreetName: "Gammelvej", StreetNumber: "4", PostalCode: "5000", MunicipalityCode: "0461"},
	}
	if err := c.db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed pre-UUID rows: %v", err)
	}

	fresh := []*Address{
		{DawaUUID: "uuid-2", DawaID: "Gammelvej 2, 2. th, 5000 Odense C", StreetName: "Gammelvej", StreetNumber: "2", PostalCode: "5000", MunicipalityCode: "0461"},
		{DawaUUID: "uuid-4", DawaID: "Gammelvej 4, 5000 Odense C", StreetName: "Gammelvej", StreetNumber: "4", PostalCode: "5000", MunicipalityCode: "0461"},
	}
	if err := c.safeCreateOrGetAddrs(fresh); err != nil {
		t.Fatalf("resolve against pre-UUID rows: %v", err)
	}

	if n := countAddrs(t, c.db); n != 2 {
		t.Fatalf("%d address rows, want 2 — the pre-UUID rows must be reused, not duplicated", n)
	}
	for i := range fresh {
		if fresh[i].ID != legacy[i].ID {
			t.Errorf("address %d resolved to row %d, want %d", i, fresh[i].ID, legacy[i].ID)
		}
	}

	var backfilled []*Address
	if err := c.db.Order("id").Find(&backfilled).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	for i, want := range []string{"uuid-2", "uuid-4"} {
		if backfilled[i].DawaUUID != want {
			t.Errorf("row %d has DawaUUID %q, want %q", backfilled[i].ID, backfilled[i].DawaUUID, want)
		}
	}

	// With the UUID backfilled, a drifted spelling now resolves to the same row.
	drifted := []*Address{{DawaUUID: "uuid-2", DawaID: "Gammelvej 2, 2 th, 5000 Odense C", StreetName: "Gammelvej", StreetNumber: "2", PostalCode: "5000", MunicipalityCode: "0461"}}
	if err := c.safeCreateOrGetAddrs(drifted); err != nil {
		t.Fatalf("resolve drifted spelling: %v", err)
	}
	if n := countAddrs(t, c.db); n != 2 {
		t.Fatalf("%d address rows after the drifted spelling, want 2", n)
	}
	if drifted[0].ID != legacy[0].ID {
		t.Errorf("drifted spelling resolved to row %d, want %d", drifted[0].ID, legacy[0].ID)
	}
}

// TestAddressMigrationToleratesPreUUIDRows pins why the unique index is partial.
// Every deployment that predates DawaUUID holds rows with an empty one, and a
// plain unique index would reject the lot on the first boot after this change.
func TestAddressMigrationToleratesPreUUIDRows(t *testing.T) {
	db := newTestDB(t)
	NewDawaCacher(db)
	if err := db.Migrator().DropIndex(&Address{}, addrUniqueIndex); err != nil {
		t.Fatalf("drop index to simulate the old schema: %v", err)
	}

	preUUID := []*Address{
		{DawaID: "Gammelvej 2, 5000 Odense C", StreetName: "Gammelvej", StreetNumber: "2", PostalCode: "5000", MunicipalityCode: "0461"},
		{DawaID: "Gammelvej 4, 5000 Odense C", StreetName: "Gammelvej", StreetNumber: "4", PostalCode: "5000", MunicipalityCode: "0461"},
	}
	if err := db.Create(&preUUID).Error; err != nil {
		t.Fatalf("seed pre-UUID rows: %v", err)
	}

	if err := db.AutoMigrate(&Address{}); err != nil {
		t.Fatalf("migration rejected rows with an empty DawaUUID: %v", err)
	}
	if !db.Migrator().HasIndex(&Address{}, addrUniqueIndex) {
		t.Fatal("unique index was not created")
	}
}

const dupUUID = "70865c44-d570-44e7-a6f5-6f7c90add725"

// duplicatedDB returns a database in the state production was left in: the
// unique index absent, and several address rows for one DAR address because the
// dedupe used to key on the formatted DawaID. The sales table is migrated here
// rather than by the caller because on a real upgrade it was created by an
// earlier deployment, long before this boot.
//
// Three copies rather than two, because nothing restricted the old keying to
// producing pairs — one per spelling the sources ever disagreed on.
func duplicatedDB(t *testing.T) (*gorm.DB, []*Address) {
	t.Helper()

	db := newTestDB(t)
	NewDawaCacher(db)
	NewBoligaCacher(db)
	if err := db.Migrator().DropIndex(&Address{}, addrUniqueIndex); err != nil {
		t.Fatalf("drop index to simulate the old schema: %v", err)
	}

	dupes := []*Address{
		{DawaUUID: dupUUID, DawaID: "Testvej 1, 1. tv, 8000 Aarhus C", StreetName: "Testvej", StreetNumber: "1", PostalCode: "8000", MunicipalityCode: "0751"},
		{DawaUUID: dupUUID, DawaID: "Testvej 1, 1 tv, 8000 Aarhus C", StreetName: "Testvej", StreetNumber: "1", PostalCode: "8000", MunicipalityCode: "0751"},
		{DawaUUID: dupUUID, DawaID: "Testvej 1, 1.tv, 8000 Aarhus C", StreetName: "Testvej", StreetNumber: "1", PostalCode: "8000", MunicipalityCode: "0751"},
	}
	if err := db.Create(&dupes).Error; err != nil {
		t.Fatalf("seed duplicates: %v", err)
	}

	return db, dupes
}

func countSales(t *testing.T, db *gorm.DB, addrID uint) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&Sale{}).Where("addr_id = ?", addrID).Count(&n).Error; err != nil {
		t.Fatalf("count sales: %v", err)
	}
	return n
}

// TestNewDawaCacherCollapsesDuplicateDawaUUID covers the databases that already
// carry the duplicates #21 stopped being created. Until they are merged the
// unique index cannot exist, so nothing stops the next one; and the copy no
// lookup resolves to keeps paying for a Boliga fetch nobody reads.
func TestNewDawaCacherCollapsesDuplicateDawaUUID(t *testing.T) {
	db, dupes := duplicatedDB(t)
	survivor, donor := dupes[0], dupes[1]

	// Only a discarded copy has a warm Boliga cache, which is the case worth
	// protecting: dropping it would make the survivor refetch a street that
	// was already paid for.
	collected := time.Now().Add(-time.Hour)
	donor.BoligaCollectedAt = collected
	donor.BoligaBuiltYear = 1932
	donor.BoligaRooms = 4
	if err := db.Save(donor).Error; err != nil {
		t.Fatalf("warm the duplicate: %v", err)
	}
	if err := db.Create(&[]Sale{
		{AddrID: donor.ID, AmountDKK: 4_200_000, SqMeters: 90, Rooms: 4, BuildYear: 1932, Date: time.Now().Add(-30 * 24 * time.Hour)},
		{AddrID: donor.ID, AmountDKK: 3_100_000, SqMeters: 90, Rooms: 4, BuildYear: 1932, Date: time.Now().Add(-900 * 24 * time.Hour)},
	}).Error; err != nil {
		t.Fatalf("seed sales: %v", err)
	}

	// A cached radius result naming every copy, plus an unrelated address.
	const query = "https://example.invalid/dar/naboer?x=1&y=2"
	const untouched = "https://example.invalid/dar/naboer?x=9&y=9"
	other := &Address{DawaUUID: "other-uuid", DawaID: "Andenvej 5, 8000 Aarhus C", StreetName: "Andenvej", StreetNumber: "5", PostalCode: "8000", MunicipalityCode: "0751"}
	if err := db.Create(other).Error; err != nil {
		t.Fatalf("seed unrelated address: %v", err)
	}
	cachedIDs := fmt.Sprintf("%d,%d,%d,%d", survivor.ID, donor.ID, dupes[2].ID, other.ID)
	otherIDs := fmt.Sprintf("%d", other.ID)
	if err := db.Create(&[]DawaQueryCache{
		{Query: query, IDs: cachedIDs, CreatedAt: time.Now()},
		{Query: untouched, IDs: otherIDs, CreatedAt: time.Now()},
	}).Error; err != nil {
		t.Fatalf("seed query cache: %v", err)
	}

	c := NewDawaCacher(db)

	if !db.Migrator().HasIndex(&Address{}, addrUniqueIndex) {
		t.Error("unique index still missing after the duplicates were collapsed")
	}
	if n := countAddrs(t, db); n != 2 {
		t.Fatalf("%d address rows, want 2 — the three copies collapsed to one, plus the unrelated address", n)
	}

	var kept Address
	if err := db.First(&kept, survivor.ID).Error; err != nil {
		t.Fatalf("the oldest copy was not the one kept: %v", err)
	}
	if !kept.BoligaCollectedAt.Equal(collected) || kept.BoligaBuiltYear != 1932 || kept.BoligaRooms != 4 {
		t.Errorf("surviving row did not adopt the duplicate's Boliga data: %+v", kept)
	}
	if n := countSales(t, db, survivor.ID); n != 2 {
		t.Errorf("%d sales on the surviving row, want the duplicate's 2", n)
	}
	if n := countSales(t, db, donor.ID); n != 0 {
		t.Errorf("%d sales still point at the removed row %d", n, donor.ID)
	}

	// The cache holds row ids for a year, and a read loads them with Find,
	// which drops ids that no longer exist without saying so. Left alone, this
	// entry would have returned two addresses instead of four.
	var cache DawaQueryCache
	if err := db.First(&cache, "query = ?", query).Error; err != nil {
		t.Fatalf("reload query cache: %v", err)
	}
	if want := fmt.Sprintf("%d,%d", survivor.ID, other.ID); cache.IDs != want {
		t.Errorf("cached ids are %q, want %q", cache.IDs, want)
	}

	// An entry naming none of the removed rows is left exactly as it was.
	var unaffected DawaQueryCache
	if err := db.First(&unaffected, "query = ?", untouched).Error; err != nil {
		t.Fatalf("reload the unaffected query cache: %v", err)
	}
	if unaffected.IDs != otherIDs {
		t.Errorf("unaffected entry was rewritten to %q, want %q", unaffected.IDs, otherIDs)
	}

	// And the drifted spelling that created the duplicate now resolves to the
	// row that survived, rather than inserting a third.
	got := []*Address{{DawaUUID: dupUUID, DawaID: "Testvej 1, 1.tv, 8000 Aarhus C", StreetName: "Testvej", StreetNumber: "1", PostalCode: "8000", MunicipalityCode: "0751"}}
	if err := c.safeCreateOrGetAddrs(got); err != nil {
		t.Fatalf("resolve after the collapse: %v", err)
	}
	if got[0].ID != survivor.ID {
		t.Errorf("resolved to row %d, want the surviving row %d", got[0].ID, survivor.ID)
	}
}

// TestCollapseDoesNotMergeTwoWarmCaches pins the limit on how much cache the
// collapse may keep. The copies hold the same street's sales, so re-pointing a
// discarded one's would list every sale under the address twice and weight it
// double in the comps.
func TestCollapseDoesNotMergeTwoWarmCaches(t *testing.T) {
	db, dupes := duplicatedDB(t)
	survivor := dupes[0]

	sale := Sale{AmountDKK: 4_200_000, SqMeters: 90, Rooms: 4, BuildYear: 1932, Date: time.Now().Add(-30 * 24 * time.Hour)}
	var sales []Sale
	for _, a := range dupes {
		a.BoligaCollectedAt = time.Now().Add(-time.Hour)
		if err := db.Save(a).Error; err != nil {
			t.Fatalf("warm %d: %v", a.ID, err)
		}
		s := sale
		s.AddrID = a.ID
		sales = append(sales, s)
	}
	if err := db.Create(&sales).Error; err != nil {
		t.Fatalf("seed sales: %v", err)
	}

	if kept, removed, err := collapseDuplicateUUIDs(db); err != nil || kept != 1 || removed != 2 {
		t.Fatalf("collapse returned (%d, %d, %v), want (1, 2, nil)", kept, removed, err)
	}

	if n := countSales(t, db, survivor.ID); n != 1 {
		t.Errorf("%d sales on the surviving row, want 1 — the duplicate's copy must be dropped, not added", n)
	}
	var total int64
	if err := db.Model(&Sale{}).Count(&total).Error; err != nil {
		t.Fatalf("count sales: %v", err)
	}
	if total != 1 {
		t.Errorf("%d sales left in the database, want 1", total)
	}
}

// TestCollapseLeavesPreUUIDRowsAlone guards the blank key. Rows written before
// DawaUUID was populated all share the empty string, so a collapse that did not
// exclude them would fold every legacy address in the database into one.
func TestCollapseLeavesPreUUIDRowsAlone(t *testing.T) {
	db := newTestDB(t)
	NewDawaCacher(db)
	NewBoligaCacher(db)

	legacy := []*Address{
		{DawaID: "Gammelvej 2, 5000 Odense C", StreetName: "Gammelvej", StreetNumber: "2", PostalCode: "5000", MunicipalityCode: "0461"},
		{DawaID: "Gammelvej 4, 5000 Odense C", StreetName: "Gammelvej", StreetNumber: "4", PostalCode: "5000", MunicipalityCode: "0461"},
		{DawaID: "Gammelvej 6, 5000 Odense C", StreetName: "Gammelvej", StreetNumber: "6", PostalCode: "5000", MunicipalityCode: "0461"},
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed pre-UUID rows: %v", err)
	}

	kept, removed, err := collapseDuplicateUUIDs(db)
	if err != nil {
		t.Fatalf("collapse: %v", err)
	}
	if kept != 0 || removed != 0 {
		t.Fatalf("collapse touched %d group(s) and removed %d row(s), want none", kept, removed)
	}
	if n := countAddrs(t, db); n != 3 {
		t.Fatalf("%d address rows, want the 3 seeded", n)
	}
}
