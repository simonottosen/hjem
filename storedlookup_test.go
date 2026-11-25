package hjem

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gorm.io/gorm"
)

func getResult(t *testing.T, s *server, id string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/result?id="+id, nil))
	return rec
}

// A link has to bring back what the page showed, plus what the search form
// held, without the lookup being run again.
func TestStoredLookupRoundTrip(t *testing.T) {
	s := &server{results: newLookupStore(newTestDB(t))}

	resp := &LookupResponse{
		PrimaryIndex: 0,
		Addrs:        []*Address{{StreetName: "Flensborggade", StreetNumber: "40"}},
		Sales:        []*JSONSale{{AddrIndex: 0, Amount: 3_100_000, SqMeters: 74}},
		Warnings:     []string{"en advarsel"},
	}
	if err := s.results.Save(context.Background(), "Flensborggade 40", []int{500}, 2, resp); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(resp.ResultID) != 32 {
		t.Fatalf("ResultID = %q, want a 32-character hex id", resp.ResultID)
	}

	rec := getResult(t, s, resp.ResultID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	var got struct {
		Query     string         `json:"query"`
		Ranges    []int          `json:"ranges"`
		Filter    int            `json:"filter"`
		CreatedAt time.Time      `json:"created_at"`
		Result    LookupResponse `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Query != "Flensborggade 40" || len(got.Ranges) != 1 || got.Ranges[0] != 500 || got.Filter != 2 {
		t.Errorf("form state = %q %v %d, want Flensborggade 40 [500] 2", got.Query, got.Ranges, got.Filter)
	}
	if time.Since(got.CreatedAt) > time.Minute {
		t.Errorf("created_at = %v, want about now", got.CreatedAt)
	}
	if len(got.Result.Sales) != 1 || got.Result.Sales[0].Amount != 3_100_000 {
		t.Errorf("result sales = %+v, want the one stored", got.Result.Sales)
	}
	if got.Result.ResultID != resp.ResultID {
		t.Errorf("result_id = %q, want %q", got.Result.ResultID, resp.ResultID)
	}
}

func TestStoredLookupUnknownIDIsNotFound(t *testing.T) {
	s := &server{results: newLookupStore(newTestDB(t))}

	for _, id := range []string{"", "0123456789abcdef0123456789abcdef"} {
		rec := getResult(t, s, id)
		if rec.Code != http.StatusNotFound {
			t.Errorf("id %q: status = %d, want 404", id, rec.Code)
		}
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
			t.Errorf("id %q: body %s, want an error message", id, rec.Body)
		}
	}
}

// Pruning runs only when a lookup finishes, so on a quiet server an expired
// row can still be in the table. The age check on read is what keeps the
// promise of thirty days.
func TestStoredLookupExpiredIsNotFound(t *testing.T) {
	db := newTestDB(t)
	s := &server{results: newLookupStore(db)}

	old := StoredLookup{
		ID:        "expired",
		Query:     "Flensborggade 40",
		Ranges:    "250",
		Result:    "{}",
		CreatedAt: time.Now().Add(-storedLookupTTL - time.Hour),
	}
	if err := db.Create(&old).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}

	if rec := getResult(t, s, "expired"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a row older than the TTL", rec.Code)
	}
}

func TestStoredLookupSavePrunesExpired(t *testing.T) {
	db := newTestDB(t)
	s := &server{results: newLookupStore(db)}

	for id, age := range map[string]time.Duration{
		"expired": storedLookupTTL + time.Hour,
		"recent":  storedLookupTTL - time.Hour,
	} {
		row := StoredLookup{ID: id, Query: "q", Ranges: "250", Result: "{}", CreatedAt: time.Now().Add(-age)}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	if err := s.results.Save(context.Background(), "q", []int{250}, 1, &LookupResponse{}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var ids []string
	db.Model(&StoredLookup{}).Order("id").Pluck("id", &ids)
	if len(ids) != 2 {
		t.Fatalf("rows = %v, want the recent one and the new one", ids)
	}
	for _, id := range ids {
		if id == "expired" {
			t.Errorf("expired row survived a save: %v", ids)
		}
	}
}

// A lookup replaced while its result is being written has no one left to
// hand the share id to. Keeping the row would hold an address nobody can
// reach for a month, so a cancellation that lands during the save must leave
// nothing behind. Here it lands after the insert has committed, which the
// context on the query itself cannot undo.
func TestStoredLookupSaveCancelledLeavesNoRow(t *testing.T) {
	db := newTestDB(t)
	s := &server{results: newLookupStore(db)}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := db.Callback().Create().After("gorm:commit_or_rollback_transaction").Register("test:cancel", func(*gorm.DB) {
		cancel()
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}

	resp := &LookupResponse{}
	if err := s.results.Save(ctx, "Flensborggade 40", []int{250}, 1, resp); err == nil {
		t.Error("Save returned nil for a cancelled lookup, want an error")
	}
	if resp.ResultID != "" {
		t.Errorf("ResultID = %q, want none for a cancelled lookup", resp.ResultID)
	}

	var n int64
	db.Model(&StoredLookup{}).Count(&n)
	if n != 0 {
		t.Errorf("stored rows = %d, want 0 after a cancelled save", n)
	}
}
