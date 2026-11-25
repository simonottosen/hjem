package hjem

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A request that cannot produce a result is turned away before it is charged
// to the client or given a session — otherwise a blank search spends one of a
// handful of lookups the client is allowed, and an unbounded radius gets to
// run in full.
func TestHandleLookupRejectsInvalidRequests(t *testing.T) {
	s := &server{sessions: newSessionStore(), dc: refusingCacher{}, stats: NewHealthStats()}

	post := func(body string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/lookup", strings.NewReader(body))
		req.RemoteAddr = "198.51.100.7:51000"
		s.handleLookup()(rec, req)
		return rec.Code
	}

	invalid := map[string]string{
		"blank address":      `{"q":"","ranges":[250]}`,
		"whitespace address": `{"q":"  \t ","ranges":[250]}`,
		"no ranges":          `{"q":"Flensborggade 40","ranges":[]}`,
		"ranges missing":     `{"q":"Flensborggade 40"}`,
		"zero radius":        `{"q":"Flensborggade 40","ranges":[0]}`,
		"negative radius":    `{"q":"Flensborggade 40","ranges":[-250]}`,
		"radius too large":   `{"q":"Flensborggade 40","ranges":[50000]}`,
		"too many ranges":    `{"q":"Flensborggade 40","ranges":[250,500,750,1000,250]}`,
	}
	for name, body := range invalid {
		if code := post(body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, code)
		}
	}

	if n := len(s.sessions.sessions); n != 0 {
		t.Errorf("%d sessions created by rejected requests, want 0", n)
	}
	// More rejected requests than the client's budget holds, so had any been
	// charged, this valid one would be refused.
	if len(invalid) <= s.sessions.maxClient {
		t.Fatalf("only %d invalid requests against a budget of %d; the check below proves nothing", len(invalid), s.sessions.maxClient)
	}
	if code := post(`{"q":"Flensborggade 40","ranges":[1000]}`); code != http.StatusAccepted {
		t.Errorf("valid request after rejected ones: status %d, want 202", code)
	}

	// The accepted lookup runs on in its own goroutine and logs its summary
	// line on the way out. Waited for, so that line cannot land in a later
	// test that captures the log. runLookup cancels its context last.
	s.sessions.mu.Lock()
	var running []*lookupSession
	for _, sess := range s.sessions.sessions {
		running = append(running, sess)
	}
	s.sessions.mu.Unlock()
	for _, sess := range running {
		<-sess.ctx.Done()
	}
}
