package hjem

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// sqlRecorder keeps every statement the driver was handed. Counting GORM calls
// instead would not answer the question below: a batching API that quietly
// falls back to one write per row looks identical from the call site and
// differs only in what reaches the database.
type sqlRecorder struct{ stmts *[]string }

func (r sqlRecorder) LogMode(logger.LogLevel) logger.Interface      { return r }
func (r sqlRecorder) Info(context.Context, string, ...interface{})  {}
func (r sqlRecorder) Warn(context.Context, string, ...interface{})  {}
func (r sqlRecorder) Error(context.Context, string, ...interface{}) {}

func (r sqlRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	*r.stmts = append(*r.stmts, sql)
}

// addrWrites counts the statements that wrote to the address table, which is
// the unit the deployment pays for: one network round-trip each against
// Postgres.
func addrWrites(stmts []string) int {
	var n int
	for _, s := range stmts {
		if !strings.Contains(s, "addresses") {
			continue
		}
		if strings.HasPrefix(s, "INSERT") || strings.HasPrefix(s, "UPDATE") {
			n++
		}
	}
	return n
}

// Nearly every address a radius search turns up has no recent sale — one
// production lookup matched 1113 sales across 9541 addresses — and all of them
// have to be stamped anyway, or they are re-fetched together with their whole
// street on every later search.
//
// So this pins both halves at once: every fetched address ends up stamped, and
// the stamping costs a number of statements set by the batch size rather than
// by the number of addresses.
func TestFetchedAddressesAreStampedInBatches(t *testing.T) {
	// Two streets, so the fetch is planned as more than one task, and enough
	// addresses that a per-row write is unmistakable against a batched one.
	const count = 120
	addrs := make([]*Address, count)
	for i := range addrs {
		street := fmt.Sprintf("Gade%d", i%2)
		addrs[i] = &Address{
			DawaUUID: fmt.Sprintf("uuid-%d", i), DawaID: fmt.Sprintf("%s %d", street, i),
			StreetName: street, StreetNumber: fmt.Sprintf("%d", i),
			PostalCode: "1666", MunicipalityCode: "101",
		}
	}

	bc := seedCacher(t, addrs)
	// Swapped in after seeding so the recorder holds the fetch's statements and
	// nothing else.
	var stmts []string
	bc.db = bc.db.Session(&gorm.Session{Logger: sqlRecorder{&stmts}})

	// Two of the hundred and twenty sold. The rest are the case that matters:
	// addresses Boliga knows nothing about, which still have to be stamped.
	sold := func(a *Address) BoligaSaleItem {
		return BoligaSaleItem{
			Addr: a.Short(), SaleType: "Alm. Salg", AmountDKK: 3_000_000,
			SqMeters: 84, Rooms: 3, BuildYear: 1932, PropertyType: PropertyApartment,
			SoldDate: time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
		}
	}
	fetch := func([]BoligaPropertyRequest, *Progress, *HealthStats) ([]BoligaSaleItem, []BoligaStreetFailure, error) {
		return []BoligaSaleItem{sold(addrs[0]), sold(addrs[1])}, nil, nil
	}

	if _, _, err := bc.FetchSales(addrs, NewProgress(), NewHealthStats(), fetch); err != nil {
		t.Fatalf("FetchSales: %v", err)
	}
	writes := addrWrites(stmts)

	// Read back rather than trusting the in-memory addresses: the structs are
	// stamped before the write, so they would report success even if nothing
	// reached the database.
	var stored []Address
	if err := bc.db.Order("id").Find(&stored).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(stored) != count {
		t.Fatalf("stored %d addresses, want %d", len(stored), count)
	}

	var unstamped []string
	for _, a := range stored {
		if a.BoligaCollectedAt.IsZero() {
			unstamped = append(unstamped, a.DawaID)
		}
	}
	if len(unstamped) > 0 {
		t.Errorf("%d of %d addresses left unstamped (%s …) — each one re-fetches its whole street on the next search",
			len(unstamped), count, strings.Join(unstamped[:min(len(unstamped), 3)], ", "))
	}

	// One fetch, one timestamp: a sold address and an unsold one must not be
	// distinguishable by when they were checked.
	for _, a := range stored[1:] {
		if !a.BoligaCollectedAt.Equal(stored[0].BoligaCollectedAt) {
			t.Fatalf("%s stamped %v, %s stamped %v — one fetch must stamp one time",
				a.DawaID, a.BoligaCollectedAt, stored[0].DawaID, stored[0].BoligaCollectedAt)
		}
	}

	// The matched addresses also carry metadata, which is what forces the write
	// to be an upsert rather than one UPDATE over every id.
	for _, a := range stored[:2] {
		if a.BoligaBuildingSize != 84 || a.BoligaRooms != 3 || a.BoligaBuiltYear != 1932 ||
			a.BoligaPropertyKind != PropertyApartment {
			t.Errorf("%s kept %dm², %d rooms, built %d, kind %d — the sale's metadata was dropped",
				a.DawaID, a.BoligaBuildingSize, a.BoligaRooms, a.BoligaBuiltYear, a.BoligaPropertyKind)
		}
	}

	// 120 addresses in batches of 50 is three statements. The bound is loose so
	// that a change of batch size is not a failure, but tight enough that a
	// per-row write cannot slip under it.
	if limit := count / 10; writes > limit {
		t.Errorf("%d writes to the address table for %d addresses, want at most %d",
			writes, count, limit)
	}
}

// Asking for two ranges at once hands FetchSales the inner addresses twice, as
// two structs sharing a row: constructRanges searches DAR once per range and
// the circles nest. Batching the stamps made that a correctness problem rather
// than a wasted write — Postgres aborts an ON CONFLICT DO UPDATE that would
// touch one row twice in a statement, so the lookup would die against the
// deployment while passing here.
//
// Asserting on the statement rather than on the stored row, because SQLite
// accepts the duplicate: reading the address back afterwards looks identical
// either way, and the engine that refuses it is not the one under test.
func TestOverlappingRangesWriteEachAddressOnce(t *testing.T) {
	addrs := []*Address{
		{DawaUUID: "uuid-sold", DawaID: "Gade 1", StreetName: "Gade", StreetNumber: "1",
			PostalCode: "1666", MunicipalityCode: "101"},
		{DawaUUID: "uuid-quiet", DawaID: "Gade 2", StreetName: "Gade", StreetNumber: "2",
			PostalCode: "1666", MunicipalityCode: "101"},
	}
	bc := seedCacher(t, addrs)

	// What the second, wider range contributes: the same row, loaded again into
	// a struct of its own. The sale below can only match one of the two, since
	// the match map is keyed by address string, so the other reaches the write
	// through the stamping loop.
	alsoInWiderRange := *addrs[0]
	input := []*Address{addrs[0], addrs[1], &alsoInWiderRange}

	var stmts []string
	bc.db = bc.db.Session(&gorm.Session{Logger: sqlRecorder{&stmts}})

	fetch := func([]BoligaPropertyRequest, *Progress, *HealthStats) ([]BoligaSaleItem, []BoligaStreetFailure, error) {
		return []BoligaSaleItem{{
			Addr: addrs[0].Short(), SaleType: "Alm. Salg", AmountDKK: 3_000_000,
			SqMeters: 84, Rooms: 3, BuildYear: 1932, PropertyType: PropertyApartment,
			SoldDate: time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
		}}, nil, nil
	}

	if _, _, err := bc.FetchSales(input, NewProgress(), NewHealthStats(), fetch); err != nil {
		t.Fatalf("FetchSales: %v", err)
	}

	var insert string
	for _, s := range stmts {
		if strings.HasPrefix(s, "INSERT") && strings.Contains(s, "addresses") {
			insert = s
			break
		}
	}
	if insert == "" {
		t.Fatal("no insert into addresses was recorded")
	}
	if n := strings.Count(insert, "uuid-sold"); n != 1 {
		t.Errorf("the duplicated address appears %d times in one upsert, want 1 — Postgres aborts the lookup on the second", n)
	}

	// The copy that survived has to be the matched one. Dropping the duplicate
	// by keeping whichever arrived last would write the bare struct over the
	// sale's metadata and lose it.
	var stored Address
	if err := bc.db.Where("dawa_uuid = ?", "uuid-sold").First(&stored).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.BoligaBuildingSize != 84 || stored.BoligaRooms != 3 {
		t.Errorf("stored %dm² and %d rooms, want 84 and 3 — the unmatched copy won the upsert",
			stored.BoligaBuildingSize, stored.BoligaRooms)
	}
}
