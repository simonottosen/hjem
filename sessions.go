package hjem

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	// finishedSessionTTL is how long a finished lookup stays readable. The
	// frontend stops polling as soon as it sees "done", so this only needs to
	// outlast a user reloading the tab and wanting their result back.
	finishedSessionTTL = 15 * time.Minute

	// maxLookupDuration is a safety valve, not the normal path: an in-flight
	// lookup is never evicted on age, because eviction cancels it and killing
	// live work would be worse than holding the memory. But a wedged lookup
	// would then occupy a slot under maxActiveSessions forever, so one that
	// has run implausibly long is cancelled. A 500 m radius in central
	// Copenhagen — ~10k addresses, Boliga dominating — takes about 4 minutes.
	maxLookupDuration = 30 * time.Minute

	// maxActiveSessions caps concurrent in-flight lookups. Each one issues
	// dozens of outbound Boliga requests against a shared rate limit, so
	// admitting unbounded lookups degrades every user rather than just the
	// newest. Finished sessions do not count against it: they hold memory
	// until the TTL evicts them, but they no longer do work.
	maxActiveSessions = 8
)

var (
	// ErrTooManySessions and ErrUnknownSession are returned to the browser as
	// the error text of a 429 and a 404, so they are phrased in Danish like
	// the progress messages.
	ErrTooManySessions = errors.New("for mange samtidige søgninger — prøv igen om et øjeblik")
	ErrUnknownSession  = errors.New("ukendt eller udløbet søgning")
)

// lookupSession is the state of one lookup: previously the server held a
// single Progress and a single cancel func, so a second user searching
// cancelled the first user's lookup and overwrote their progress. Giving each
// lookup its own means concurrent users no longer interfere.
type lookupSession struct {
	ID        string
	Progress  *Progress
	ctx       context.Context
	cancel    context.CancelFunc
	createdAt time.Time
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*lookupSession

	finishedTTL time.Duration
	maxDuration time.Duration
	maxActive   int
	now         func() time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{
		sessions:    map[string]*lookupSession{},
		finishedTTL: finishedSessionTTL,
		maxDuration: maxLookupDuration,
		maxActive:   maxActiveSessions,
		now:         time.Now,
	}
}

// Create registers a new session. previousID, when it names a live session,
// is cancelled first: a client starting a new search still replaces its own
// in-flight one, but no longer anyone else's. Ids are unguessable, so knowing
// one is what proves the caller started it.
func (st *sessionStore) Create(previousID string) (*lookupSession, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	st.evictLocked()

	if prev, ok := st.sessions[previousID]; ok {
		prev.cancel()
		delete(st.sessions, previousID)
	}

	if n := st.countActiveLocked(); n >= st.maxActive {
		return nil, fmt.Errorf("%w (%d aktive)", ErrTooManySessions, n)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sess := &lookupSession{
		ID:        newSessionID(),
		Progress:  NewProgress(),
		ctx:       ctx,
		cancel:    cancel,
		createdAt: st.now(),
	}
	st.sessions[sess.ID] = sess

	return sess, nil
}

// Get returns a session by id. A miss — replaced, or aged out — is reported
// as such rather than as an idle-looking progress event, so the caller can
// stop polling an id that will never advance.
func (st *sessionStore) Get(id string) (*lookupSession, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()

	st.evictLocked()

	sess, ok := st.sessions[id]
	return sess, ok
}

// evictLocked drops finished sessions past their TTL and cancels in-flight
// ones that have exceeded maxLookupDuration. Eviction is driven by Create and
// Get rather than a background ticker: the map only grows when it is touched,
// so there is nothing to clean up in between.
func (st *sessionStore) evictLocked() {
	now := st.now()
	for id, sess := range st.sessions {
		age := now.Sub(sess.createdAt)
		switch {
		case sess.Progress.Finished():
			if age >= st.finishedTTL {
				delete(st.sessions, id)
			}
		case age >= st.maxDuration:
			log.Printf("Lookup session %s exceeded %s; cancelling", id, st.maxDuration)
			sess.cancel()
			delete(st.sessions, id)
		}
	}
}

func (st *sessionStore) countActiveLocked() int {
	var n int
	for _, sess := range st.sessions {
		if !sess.Progress.Finished() {
			n++
		}
	}
	return n
}

func (st *sessionStore) PrometheusMetrics() string {
	st.mu.Lock()
	total, active := len(st.sessions), st.countActiveLocked()
	st.mu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, "# HELP hjem_lookup_sessions Lookup sessions currently held in memory, by state.\n")
	fmt.Fprintf(&b, "# TYPE hjem_lookup_sessions gauge\n")
	fmt.Fprintf(&b, "hjem_lookup_sessions{state=\"active\"} %d\n", active)
	fmt.Fprintf(&b, "hjem_lookup_sessions{state=\"finished\"} %d\n", total-active)

	return b.String()
}

// newSessionID returns an unguessable id. Guessability is the access control
// here: a client that knows an id can cancel that lookup and read its result.
// crypto/rand.Read never returns an error as of Go 1.24.
func newSessionID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
