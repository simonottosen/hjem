package hjem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The id is not a secret: it is in every progress URL the browser asks for and
// in the JS that asks. Before the token, holding one was enough to post sales
// into a stranger's lookup and have them counted in the estimate the user acts
// on.
func TestBoligaIngestRequiresTheSessionsToken(t *testing.T) {
	s := testServer(t)
	sess, err := s.sessions.Create("")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.sessions.Create("")
	if err != nil {
		t.Fatal(err)
	}

	post := func(token string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"lookup_id":%q,"ingest_token":%q,"fetched":[]}`, sess.ID, token)
		req := httptest.NewRequest(http.MethodPost, "/api/boliga/ingest", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		s.handleBoligaIngest()(rec, req)
		return rec
	}

	for _, tc := range []struct {
		name  string
		token string
	}{
		{"no token", ""},
		{"a guess", "deadbeefdeadbeefdeadbeefdeadbeef"},
		{"another lookup's token", other.Token},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(tc.token)
			// Not 404: the session is perfectly real, and saying so separates
			// a stale tab from a caller that has an id without the token that
			// goes with it.
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403", rec.Code)
			}
			if n := len(sess.boligaIngest); n != 0 {
				t.Fatalf("%d ingest(s) reached the lookup anyway", n)
			}
		})
	}

	t.Run("the token it was issued", func(t *testing.T) {
		if rec := post(sess.Token); rec.Code != http.StatusAccepted {
			t.Fatalf("status %d, want 202 — the browser's own upload must still land", rec.Code)
		}
	})
}

// The browser can only send what it was given, so the token has to come back
// from the request that starts the lookup — and be a different secret from the
// id, which is published to anyone who can see a URL.
func TestHandleLookupIssuesAnIngestToken(t *testing.T) {
	s := &server{sessions: newSessionStore(), dc: refusingCacher{}, stats: NewHealthStats()}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/lookup", bytes.NewBufferString(`{"q":"x","ranges":[100]}`))
	s.handleLookup()(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202", rec.Code)
	}

	var got map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got["ingest_token"] == "" {
		t.Fatalf("no ingest_token in %v", got)
	}
	if got["ingest_token"] == got["lookup_id"] {
		t.Fatal("the token is the id; it would be exposed everywhere the id is")
	}

	sess, ok := s.sessions.Get(got["lookup_id"])
	if !ok {
		t.Fatal("the session the response names does not exist")
	}
	if sess.Token != got["ingest_token"] {
		t.Fatal("the token handed to the browser is not the one the session will check")
	}
}
