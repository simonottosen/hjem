package hjem

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// A tab closed mid-lookup leaves its goroutine running for minutes. Unless
// cancelling it also gives back its slot, eight abandoned tabs lock everyone
// out until their lookups finish on their own.
func TestSessionStoreCancelFreesSlot(t *testing.T) {
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

	if !st.Cancel(sessions[0].ID) {
		t.Fatal("Cancel reported a live session as unknown")
	}
	if sessions[0].ctx.Err() == nil {
		t.Error("the cancelled lookup's context is still live")
	}
	if sessions[1].ctx.Err() != nil {
		t.Error("cancelling one lookup cancelled another")
	}
	if _, ok := st.Get(sessions[0].ID); ok {
		t.Error("the cancelled session is still readable")
	}
	if _, err := st.Create(""); err != nil {
		t.Fatalf("a cancelled session is still consuming a slot: %v", err)
	}
}

func TestSessionStoreCancelUnknown(t *testing.T) {
	st, _ := newTestStore()
	st.Create("")

	for _, id := range []string{"", "deadbeef"} {
		if st.Cancel(id) {
			t.Errorf("Cancel(%q) reported a session that does not exist", id)
		}
	}
}

// The page holds a finished result back for a moment before showing it, so a
// tab closed in that moment still sends a cancel. The lookup is done by then
// and holds no slot; deleting it would only lose the result a reload could
// still have read.
func TestSessionStoreCancelLeavesFinishedResult(t *testing.T) {
	st, _ := newTestStore()
	sess, _ := st.Create("")
	sess.Progress.Update(StageDone, "Færdig!", 0, 0)

	if st.Cancel(sess.ID) {
		t.Error("Cancel reported cancelling a finished lookup")
	}
	if _, ok := st.Get(sess.ID); !ok {
		t.Error("cancelling a finished lookup threw away its result")
	}
}

func TestHandleCancel(t *testing.T) {
	s := &server{sessions: newSessionStore()}
	sess, _ := s.sessions.Create("")

	// text/plain is what navigator.sendBeacon labels a string body with, and
	// it is the only request a page being closed can still rely on sending.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/cancel", strings.NewReader(`{"lookup_id":"`+sess.ID+`"}`))
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	s.handleCancel()(rec, req)

	if rec.Code != 204 {
		t.Fatalf("known id: got status %d, want 204", rec.Code)
	}
	if sess.ctx.Err() == nil {
		t.Error("the lookup was not cancelled")
	}
	if _, ok := s.sessions.Get(sess.ID); ok {
		t.Error("the cancelled session is still readable")
	}

	// A second beacon for the same lookup — or one for a lookup that already
	// aged out — finds nothing, and says so.
	rec = httptest.NewRecorder()
	s.handleCancel()(rec, httptest.NewRequest("POST", "/api/cancel", strings.NewReader(`{"lookup_id":"`+sess.ID+`"}`)))
	if rec.Code != 404 {
		t.Errorf("unknown id: got status %d, want 404", rec.Code)
	}
}

func TestHandleCancelRejectsGet(t *testing.T) {
	s := &server{sessions: newSessionStore()}
	sess, _ := s.sessions.Create("")

	rec := httptest.NewRecorder()
	s.handleCancel()(rec, httptest.NewRequest("GET", "/api/cancel?lookup_id="+sess.ID, nil))

	if rec.Code != 405 {
		t.Fatalf("got status %d, want 405", rec.Code)
	}
	if sess.ctx.Err() != nil {
		t.Error("a GET cancelled the lookup")
	}
}
