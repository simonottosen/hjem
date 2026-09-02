package hjem

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
