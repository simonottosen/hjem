package hjem

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A radius result and a resolved address are cached by the same code and must
// not be cached for the same length of time: the first is a set that grows with
// every completed building, the second an identity that does not change. The
// free-text half is the assertion that matters here — it is what catches a
// shortened TTL applied to both. Both figures are spelled out rather than read
// back from the constants they came from, which would assert nothing.
func TestAddressAndRadiusMaxAgesDiffer(t *testing.T) {
	if got, want := (DARNearbySearch{Meters: 500}).MaxAge(), 28*24*time.Hour; got != want {
		t.Errorf("radius maximum age is %v, want %v", got, want)
	}
	if got, want := (AVFuzzySearch{Query: "Rådhuspladsen 1"}).MaxAge(), 365*24*time.Hour; got != want {
		t.Errorf("free-text maximum age is %v, want %v", got, want)
	}
}

// darEmptyServer stands in for the DAR GraphQL endpoint, answering every query
// with an empty connection — enough to drive a Fetch to completion without a
// fixture — and counting how many times it was asked.
func darEmptyServer(t *testing.T) *int {
	t.Helper()
	var calls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"data":{"DAR_Adressepunkt":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}`)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("DATAFORDELER_API_KEY", "test-key")
	t.Setenv("DATAFORDELER_GRAPHQL_URL", srv.URL)

	return &calls
}

// seedCacheEntry leaves behind what a warm cache entry of the given age looks
// like on disk: one address, and a query entry naming it. The entry is built
// through the same constructor a real write uses, so the key is the one the
// request will be looked up under rather than a second guess at it. The uuid is
// a parameter because the addresses table rejects two rows sharing one.
func seedCacheEntry(t *testing.T, c *dawaCacher, req DawaRequest, age time.Duration, uuid string) {
	t.Helper()

	addr := &Address{
		DawaUUID:         uuid,
		DawaID:           uuid + ", 1550 København V",
		StreetName:       "Rådhuspladsen",
		StreetNumber:     "1",
		PostalCode:       "1550",
		MunicipalityCode: "0101",
	}
	if err := c.db.Create(addr).Error; err != nil {
		t.Fatalf("seed address: %v", err)
	}

	entry := NewDawaQueryCacheFromAddrs(req, []*Address{addr})
	entry.CreatedAt = time.Now().Add(-age)
	if err := c.db.Create(&entry).Error; err != nil {
		t.Fatalf("seed cache entry: %v", err)
	}
}

// Sixty days is the age the two answers have to diverge at: well past the point
// where a neighbourhood may have gained a building the cached set never saw, and
// nowhere near long enough for the address someone typed to stop naming it.
func TestStaleRadiusRefetchesWhileResolvedAddressIsServed(t *testing.T) {
	const staleDays = 60

	c := NewDawaCacher(newTestDB(t))
	calls := darEmptyServer(t)
	avTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Adressevælger was queried for %s; a %d-day-old resolution is well inside its maximum age", r.URL.Path, staleDays)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	})

	// Latitude/Longtitude are swapped on Address — see addrLatLon.
	radius := DARNearbySearch{Addr: Address{Latitude: 12.5683, Longtitude: 55.6761}, Meters: 500}
	freeText := AVFuzzySearch{Query: "Rådhuspladsen 1, 1550 København V"}
	seedCacheEntry(t, c, radius, staleDays*24*time.Hour, "stale-radius")
	seedCacheEntry(t, c, freeText, staleDays*24*time.Hour, "resolved-address")

	if _, err := c.Do(radius); err != nil {
		t.Fatalf("radius search over a %d-day-old entry: %v", staleDays, err)
	}
	if *calls != 1 {
		t.Errorf("DAR was queried %d times, want 1 — the stale radius entry was served instead of refreshed", *calls)
	}

	addrs, err := c.Do(freeText)
	if err != nil {
		t.Fatalf("free-text search over a %d-day-old entry: %v", staleDays, err)
	}
	if len(addrs) != 1 || addrs[0].DawaUUID != "resolved-address" {
		t.Fatalf("got %+v, want the cached resolution", addrs)
	}

	// The other side of the figure: a radius result still inside its maximum age
	// has to be served, or the shorter TTL is just a cache that never hits.
	fresh := DARNearbySearch{Addr: Address{Latitude: 10.2039, Longtitude: 56.1629}, Meters: 500}
	seedCacheEntry(t, c, fresh, 7*24*time.Hour, "fresh-radius")

	addrs, err = c.Do(fresh)
	if err != nil {
		t.Fatalf("radius search over a 7-day-old entry: %v", err)
	}
	if *calls != 1 {
		t.Errorf("DAR was queried %d times, want 1 — a radius entry inside its maximum age was refetched", *calls)
	}
	if len(addrs) != 1 || addrs[0].DawaUUID != "fresh-radius" {
		t.Fatalf("got %+v, want the cached radius result", addrs)
	}
}
