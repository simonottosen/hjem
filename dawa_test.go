package hjem

import (
	"errors"
	"net/http"
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
