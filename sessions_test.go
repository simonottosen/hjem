package hjem

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type refusingCacher struct{}

func (refusingCacher) Do(DawaRequest) ([]*Address, error) {
	return nil, errors.New("no address service in this test")
}

// newTestStore returns a store on a frozen clock so age-based behaviour can be
// exercised without sleeping.
func newTestStore() (*sessionStore, *time.Time) {
	clock := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	st := newSessionStore()
	st.now = func() time.Time { return clock }
	return st, &clock
}

func TestSessionStoreIsolatesConcurrentLookups(t *testing.T) {
	st, _ := newTestStore()

	a, err := st.Create("")
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}
	b, err := st.Create("")
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}

	if a.ID == b.ID {
		t.Fatalf("both sessions got id %q", a.ID)
	}
	if a.Progress == b.Progress {
		t.Fatal("sessions share one Progress; a second user would overwrite the first's")
	}

	// The bug this replaces: creating B cancelled A.
	if a.ctx.Err() != nil {
		t.Fatal("creating a second session cancelled the first")
	}

	a.Progress.Update(StageBoligaList, "A", 3, 9)
	b.Progress.Update(StageDawa, "B", 0, 0)

	if got := a.Progress.Snapshot(); got.Message != "A" || got.Current != 3 {
		t.Fatalf("A's progress was clobbered: %+v", got)
	}
	if got := b.Progress.Snapshot(); got.Message != "B" {
		t.Fatalf("B's progress was clobbered: %+v", got)
	}
}

func TestSessionStoreCancelsOnlyThePreviousSession(t *testing.T) {
	st, _ := newTestStore()

	mine, _ := st.Create("")
	theirs, _ := st.Create("")

	if _, err := st.Create(mine.ID); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if mine.ctx.Err() == nil {
		t.Error("a client's new search did not cancel its own previous one")
	}
	if theirs.ctx.Err() != nil {
		t.Error("another client's in-flight lookup was cancelled")
	}
	if _, ok := st.Get(mine.ID); ok {
		t.Error("the cancelled session is still readable")
	}
	if _, ok := st.Get(theirs.ID); !ok {
		t.Error("the other client's session was dropped")
	}
}

func TestSessionStoreEvictsFinishedAfterTTL(t *testing.T) {
	st, clock := newTestStore()

	sess, _ := st.Create("")
	sess.Progress.Update(StageDone, "Færdig!", 0, 0)

	*clock = clock.Add(st.finishedTTL - time.Second)
	if _, ok := st.Get(sess.ID); !ok {
		t.Fatal("finished session evicted before its TTL; a tab reload would lose the result")
	}

	*clock = clock.Add(2 * time.Second)
	if _, ok := st.Get(sess.ID); ok {
		t.Fatal("finished session outlived its TTL")
	}
}

// A lookup is allowed to run for up to maxLookupDuration, which is longer
// than a finished result is retained. Its result must still survive being
// collected: retaining from createdAt made a lookup that ran longer than
// finishedTTL expire the instant it completed, so the poll that would have
// delivered the result evicted it and answered 404 instead — and the slower
// the lookup, the more certainly its work was thrown away.
func TestSessionStoreRetainsResultOfLongRunningLookup(t *testing.T) {
	st, clock := newTestStore()

	sess, _ := st.Create("")

	// Runs for longer than a finished result is kept, but well inside what an
	// in-flight lookup is allowed.
	*clock = clock.Add(st.finishedTTL + time.Minute)
	if _, ok := st.Get(sess.ID); !ok {
		t.Fatal("a lookup still running inside maxLookupDuration was evicted")
	}
	sess.Progress.Update(StageDone, "Færdig!", 0, 0)

	if _, ok := st.Get(sess.ID); !ok {
		t.Fatal("the result was evicted by the very poll that came to collect it")
	}

	// And it is then kept for the full window, measured from completion.
	*clock = clock.Add(st.finishedTTL - time.Second)
	if _, ok := st.Get(sess.ID); !ok {
		t.Fatal("result evicted before its TTL had run from the time it finished")
	}
	*clock = clock.Add(2 * time.Second)
	if _, ok := st.Get(sess.ID); ok {
		t.Fatal("finished session outlived its TTL")
	}
}

func TestSessionStoreDoesNotEvictRunningLookups(t *testing.T) {
	st, clock := newTestStore()

	sess, _ := st.Create("")
	sess.Progress.Update(StageBoligaList, "still going", 0, 0)

	// Past the finished-session TTL but short of maxDuration: eviction
	// cancels, so applying the TTL to live work would kill a slow-but-healthy
	// lookup.
	*clock = clock.Add(st.finishedTTL + time.Minute)

	if _, ok := st.Get(sess.ID); !ok {
		t.Fatal("an in-flight lookup was evicted on age")
	}
	if sess.ctx.Err() != nil {
		t.Fatal("an in-flight lookup was cancelled on age")
	}
}

func TestSessionStoreCancelsOverrunningLookup(t *testing.T) {
	st, clock := newTestStore()

	sess, _ := st.Create("")
	sess.Progress.Update(StageBoligaList, "wedged", 0, 0)

	*clock = clock.Add(st.maxDuration)

	if _, ok := st.Get(sess.ID); ok {
		t.Fatal("a lookup past maxLookupDuration is still holding a slot")
	}
	if sess.ctx.Err() == nil {
		t.Fatal("a lookup past maxLookupDuration was dropped without being cancelled")
	}
}

func TestSessionStoreCapsActiveLookups(t *testing.T) {
	st, _ := newTestStore()

	var sessions []*lookupSession
	for i := 0; i < st.maxActive; i++ {
		sess, err := st.Create("")
		if err != nil {
			t.Fatalf("Create %d of %d: %v", i+1, st.maxActive, err)
		}
		sessions = append(sessions, sess)
	}

	if _, err := st.Create(""); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("over the cap: got err %v, want ErrTooManySessions", err)
	}

	// Finishing one frees a slot: the cap exists to bound concurrent outbound
	// work, and a finished session does none.
	sessions[0].Progress.Update(StageDone, "Færdig!", 0, 0)
	if _, err := st.Create(""); err != nil {
		t.Fatalf("a finished session is still consuming a slot: %v", err)
	}
}

func TestSessionMetricsSeparateActiveFromFinished(t *testing.T) {
	st, _ := newTestStore()

	running, _ := st.Create("")
	running.Progress.Update(StageBoligaList, "working", 0, 0)
	done, _ := st.Create("")
	done.Progress.Update(StageDone, "Færdig!", 0, 0)

	got := st.PrometheusMetrics()
	for _, want := range []string{
		`hjem_lookup_sessions{state="active"} 1`,
		`hjem_lookup_sessions{state="finished"} 1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("metrics missing %q:\n%s", want, got)
		}
	}
}

func TestHandleProgressScopesToSession(t *testing.T) {
	s := &server{sessions: newSessionStore()}

	mine, _ := s.sessions.Create("")
	mine.Progress.Update(StageBoligaList, "mine", 2, 5)
	theirs, _ := s.sessions.Create("")
	theirs.Progress.Update(StageDawa, "theirs", 0, 0)

	rec := httptest.NewRecorder()
	s.handleProgress()(rec, httptest.NewRequest("GET", "/api/progress?id="+mine.ID, nil))

	var evt ProgressEvent
	if err := json.NewDecoder(rec.Body).Decode(&evt); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if evt.Message != "mine" || evt.Current != 2 {
		t.Fatalf("got another session's progress: %+v", evt)
	}
}

func TestHandleProgressRejectsUnknownID(t *testing.T) {
	s := &server{sessions: newSessionStore()}

	for _, id := range []string{"", "deadbeef"} {
		rec := httptest.NewRecorder()
		s.handleProgress()(rec, httptest.NewRequest("GET", "/api/progress?id="+id, nil))

		// A 200 with an idle-looking event would leave the client polling an
		// id that will never advance.
		if rec.Code != 404 {
			t.Errorf("id %q: got status %d, want 404", id, rec.Code)
		}
	}
}

// The hole a concurrency count alone leaves: a lookup that fails in
// milliseconds releases its slot immediately, so a client looping fast
// failures is never once seen holding more than one. Charging on starts is
// what makes the budget bind on it.
func TestClientBudgetSurvivesFastFailingLookups(t *testing.T) {
	st, _ := newTestStore()

	for i := 0; i < st.maxClient; i++ {
		if err := st.Admit("198.51.100.7"); err != nil {
			t.Fatalf("lookup %d of %d: %v", i+1, st.maxClient, err)
		}
		sess, err := st.Create("")
		if err != nil {
			t.Fatalf("Create %d: %v", i+1, err)
		}
		// Failed and gone: both the slot and, were the count taken from the
		// map, the client's whole history with it.
		sess.Progress.Update(StageError, "no found address", 0, 0)
	}

	if err := st.Admit("198.51.100.7"); !errors.Is(err, ErrTooManyClientLookups) {
		t.Fatalf("over the client budget: got err %v, want ErrTooManyClientLookups", err)
	}
	if n := st.countActiveLocked(); n != 0 {
		t.Fatalf("%d sessions still active; the budget has to bind without them", n)
	}
}

// A client that replaces its own lookup deletes that session from the store,
// which is the other way a map-derived count would be reset — by a parameter
// the caller chooses.
func TestClientBudgetSurvivesSelfReplacement(t *testing.T) {
	st, _ := newTestStore()

	previous := ""
	for i := 0; i < st.maxClient; i++ {
		if err := st.Admit("198.51.100.7"); err != nil {
			t.Fatalf("lookup %d of %d: %v", i+1, st.maxClient, err)
		}
		sess, err := st.Create(previous)
		if err != nil {
			t.Fatalf("Create %d: %v", i+1, err)
		}
		previous = sess.ID
	}

	if err := st.Admit("198.51.100.7"); !errors.Is(err, ErrTooManyClientLookups) {
		t.Fatalf("got err %v, want ErrTooManyClientLookups", err)
	}
}

func TestClientBudgetRefillsAfterWindow(t *testing.T) {
	st, clock := newTestStore()

	for i := 0; i < st.maxClient; i++ {
		if err := st.Admit("198.51.100.7"); err != nil {
			t.Fatalf("lookup %d: %v", i+1, err)
		}
	}
	if err := st.Admit("203.0.113.9"); err != nil {
		t.Fatalf("a second client was refused: %v", err)
	}

	*clock = clock.Add(st.clientWindow)
	if err := st.Admit("198.51.100.7"); err != nil {
		t.Fatalf("still refused a whole window later: %v", err)
	}

	// Expired starts are dropped rather than merely ignored, and a client that
	// has gone quiet stops being held at all: nothing else ever revisits this
	// map, so what it keeps here it keeps for as long as the process lives.
	if n := len(st.clientStarts["198.51.100.7"]); n != 1 {
		t.Errorf("%d starts retained, want only the one inside the window", n)
	}
	if _, ok := st.clientStarts["203.0.113.9"]; ok {
		t.Error("a client whose window has passed is still held")
	}
}

// Below maxActiveSessions by enough that no single client can fill the pool
// and leave everyone else a 429.
func TestClientBudgetIsBelowTheGlobalCap(t *testing.T) {
	st, _ := newTestStore()

	if st.maxClient >= st.maxActive {
		t.Fatalf("per-client cap %d does not bind below the global cap %d", st.maxClient, st.maxActive)
	}
}

func TestHandleLookupRejectsOverClientBudget(t *testing.T) {
	// An accepted lookup runs for real in a goroutine, so it needs something
	// to ask for the address — which here refuses, ending the lookup at its
	// first stage.
	s := &server{sessions: newSessionStore(), dc: refusingCacher{}, stats: NewHealthStats()}

	post := func() int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/lookup", strings.NewReader(`{"q":"x","ranges":[100]}`))
		req.RemoteAddr = "198.51.100.7:51000"
		s.handleLookup()(rec, req)
		return rec.Code
	}

	for i := 0; i < s.sessions.maxClient; i++ {
		if code := post(); code != http.StatusAccepted {
			t.Fatalf("lookup %d: status %d, want 202", i+1, code)
		}
	}
	if code := post(); code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429 once one client is over its budget", code)
	}

	// The global pool is nowhere near full, so a different client must still
	// be served.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/lookup", strings.NewReader(`{"q":"x","ranges":[100]}`))
	req.RemoteAddr = "203.0.113.9:51000"
	s.handleLookup()(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("second client got status %d, want 202", rec.Code)
	}
}

func TestHandleLookupRejectsOverCap(t *testing.T) {
	s := &server{sessions: newSessionStore()}
	for i := 0; i < s.sessions.maxActive; i++ {
		if _, err := s.sessions.Create(""); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/lookup", strings.NewReader(`{"q":"x","ranges":[100]}`))
	s.handleLookup()(rec, req)

	if rec.Code != 429 {
		t.Fatalf("got status %d, want 429", rec.Code)
	}
}
