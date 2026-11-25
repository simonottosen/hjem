package hjem

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// storedLookupTTL is how long a finished lookup stays reachable by its link.
// Long enough to send to someone and have them open it next week; short
// enough that the table does not grow without bound, and that nobody mistakes
// a stale result for today's market — the page says when it was made.
const storedLookupTTL = 30 * 24 * time.Hour

var ErrUnknownResult = errors.New("ukendt eller udløbet resultat")

// StoredLookup is a finished lookup kept so that a link can show it again
// without redoing the minutes of work behind it. The id is not the session id:
// that one lets its holder cancel the lookup, and a link is meant to be passed
// around. Result is the response exactly as the page received it — the
// dashboard recomputes everything else from it — kept as plain text because
// SQLite and Postgres agree on no JSON column type.
type StoredLookup struct {
	ID        string `gorm:"primaryKey"`
	Query     string `gorm:"not null"`
	Ranges    string `gorm:"not null"`
	Filter    int
	Result    string    `gorm:"not null"`
	CreatedAt time.Time `gorm:"index"`
}

type lookupStore struct {
	db *gorm.DB
}

func newLookupStore(db *gorm.DB) *lookupStore {
	if err := db.AutoMigrate(&StoredLookup{}); err != nil {
		log.Printf("StoredLookup migration failed: %v", err)
	}
	return &lookupStore{db: db}
}

// Save keeps resp under a new share id, which it also sets on resp as
// ResultID so the page can put it in the address bar. On failure resp is left
// without one: the lookup itself succeeded and is delivered regardless, it
// just cannot be linked to.
//
// Expired rows are pruned here rather than on a timer. Saves are the only
// thing that grows the table, so pruning alongside them bounds it without a
// goroutine of its own.
func (ls *lookupStore) Save(query string, ranges []int, filter int, resp *LookupResponse) error {
	if err := ls.db.Where("created_at < ?", time.Now().Add(-storedLookupTTL)).Delete(&StoredLookup{}).Error; err != nil {
		log.Printf("Pruning stored lookups failed: %v", err)
	}

	resp.ResultID = newSessionID()
	result, err := json.Marshal(resp)
	if err != nil {
		resp.ResultID = ""
		return err
	}

	rs := make([]string, len(ranges))
	for i, r := range ranges {
		rs[i] = strconv.Itoa(r)
	}

	row := StoredLookup{
		ID:     resp.ResultID,
		Query:  query,
		Ranges: strings.Join(rs, ","),
		Filter: filter,
		Result: string(result),
	}
	if err := ls.db.Create(&row).Error; err != nil {
		resp.ResultID = ""
		return err
	}
	return nil
}

// Get returns the stored lookup with this id, or false if there is none or it
// has outlived storedLookupTTL. The age is checked here too, not left to
// pruning, because pruning only runs when someone finishes a lookup.
func (ls *lookupStore) Get(id string) (*StoredLookup, bool) {
	if id == "" {
		return nil, false
	}
	var row StoredLookup
	if err := ls.db.Where("id = ?", id).First(&row).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("Reading stored lookup failed: %v", err)
		}
		return nil, false
	}
	if time.Since(row.CreatedAt) >= storedLookupTTL {
		return nil, false
	}
	return &row, true
}

func (s *server) handleResult() http.HandlerFunc {
	type Reply struct {
		Query     string          `json:"query"`
		Ranges    []int           `json:"ranges"`
		Filter    int             `json:"filter"`
		CreatedAt time.Time       `json:"created_at"`
		Result    json.RawMessage `json:"result"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		row, ok := s.results.Get(r.URL.Query().Get("id"))
		if !ok {
			replyJSONErr(w, ErrUnknownResult, http.StatusNotFound)
			return
		}

		ranges := []int{}
		for _, part := range strings.Split(row.Ranges, ",") {
			if n, err := strconv.Atoi(part); err == nil {
				ranges = append(ranges, n)
			}
		}

		replyJSON(w, Reply{
			Query:     row.Query,
			Ranges:    ranges,
			Filter:    row.Filter,
			CreatedAt: row.CreatedAt,
			Result:    json.RawMessage(row.Result),
		}, http.StatusOK)
	}
}
