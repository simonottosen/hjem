package hjem

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// darPageServer serves a fixed list of node ids as pages of size perPage,
// mimicking DAR's cursor paging. It records the `after` cursor of every request
// (empty string for the first page) so tests can assert the walk actually
// followed the cursor rather than re-reading page one.
func darPageServer(t *testing.T, ids []string, perPage int) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				After *string `json:"after"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// The cursor is just the index of the next node, as a string.
		start := 0
		cursor := ""
		if req.Variables.After != nil {
			cursor = *req.Variables.After
			if _, err := fmt.Sscanf(cursor, "%d", &start); err != nil {
				t.Errorf("unparseable cursor %q", cursor)
			}
		}
		seen = append(seen, cursor)

		end := min(start+perPage, len(ids))
		nodes := make([]string, 0, end-start)
		for _, id := range ids[start:end] {
			nodes = append(nodes, fmt.Sprintf(`{"id_lokalId":%q,"adgangspunkt":"p"}`, id))
		}

		fmt.Fprintf(w, `{"data":{"DAR_Husnummer":{
			"pageInfo":{"hasNextPage":%t,"endCursor":"%d"},
			"nodes":[%s]}}}`,
			end < len(ids), end, strings.Join(nodes, ","))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func darPageAll(endpoint string) ([]darHusnummerNode, error) {
	return darPostAll(endpoint, "query", func(r *darHusnummerResp) *darConnection[darHusnummerNode] {
		return &r.Husnummer
	})
}

// A result set larger than one page must come back whole. This is the #29
// regression: before cursor paging, everything past the first page was silently
// dropped and a truncated radius looked like a complete one.
func TestDARPostAllWalksEveryPage(t *testing.T) {
	ids := make([]string, 250)
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%03d", i)
	}
	srv, calls := darPageServer(t, ids, 100)

	got, err := darPageAll(srv.URL)
	if err != nil {
		t.Fatalf("darPostAll: %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("got %d nodes, want %d — pages past the first were dropped", len(got), len(ids))
	}
	for i, n := range got {
		if n.IDLokalID != ids[i] {
			t.Fatalf("node %d is %q, want %q (pages out of order or duplicated)", i, n.IDLokalID, ids[i])
		}
	}
	// 250 ids at 100/page = 3 requests, starting at no cursor then 100, 200.
	if want := []string{"", "100", "200"}; !equalStrs(*calls, want) {
		t.Errorf("cursors requested = %v, want %v", *calls, want)
	}
}

// The common case must not pay for pagination: one page in, one request out.
func TestDARPostAllSinglePage(t *testing.T) {
	srv, calls := darPageServer(t, []string{"a", "b"}, 100)

	got, err := darPageAll(srv.URL)
	if err != nil {
		t.Fatalf("darPostAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d nodes, want 2", len(got))
	}
	if len(*calls) != 1 {
		t.Errorf("made %d requests for a single page, want 1", len(*calls))
	}
}

// A server claiming hasNextPage while returning no cursor would otherwise pin
// the walk on page one forever. Stop instead of spinning.
func TestDARPostAllStopsOnEmptyCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"DAR_Husnummer":{
			"pageInfo":{"hasNextPage":true,"endCursor":""},
			"nodes":[{"id_lokalId":"only","adgangspunkt":"p"}]}}}`)
	}))
	defer srv.Close()

	got, err := darPageAll(srv.URL)
	if err != nil {
		t.Fatalf("darPostAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d nodes, want 1", len(got))
	}
}

// A server that always reports another page must be abandoned with an error
// rather than hammering a rate-limited API indefinitely.
func TestDARPostAllBoundsPageCount(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		fmt.Fprintf(w, `{"data":{"DAR_Husnummer":{
			"pageInfo":{"hasNextPage":true,"endCursor":"%d"},
			"nodes":[{"id_lokalId":"n%d","adgangspunkt":"p"}]}}}`, n, n)
	}))
	defer srv.Close()

	_, err := darPageAll(srv.URL)
	if err == nil {
		t.Fatal("expected an error when the server never stops paging")
	}
	if !strings.Contains(err.Error(), "pagination exceeded") {
		t.Errorf("error %q does not name the page cap", err)
	}
	if n != darMaxPages {
		t.Errorf("made %d requests, want exactly darMaxPages (%d)", n, darMaxPages)
	}
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
