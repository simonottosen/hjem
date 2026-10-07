package hjem

import (
	"log"
	"math"
	"net/http"
	"sync"
	"time"
)

var DefaultClient http.Client

// hostRequestGap is the minimum spacing between requests to a host that
// throttles.
//
// Boliga states its limit in every response: `x-ratelimit-limit: 5` with an
// `x-ratelimit-reset` about eleven seconds out, and a spent request returns to
// the bucket eleven seconds after it was made. Measured against the live API —
// six requests succeed, the seventh 429s, and the header says to come back in
// ten seconds. Five per eleven seconds is therefore the rate, so that is what
// this encodes.
//
// RetryRoundTripper on its own is purely reactive: it fires as fast as the
// network allows, takes the 429, then sleeps 2s..32s. A measured 21-street
// lookup spent 3m58s that way — ~3s per request, which is the sustainable rate
// arrived at by overshooting it and backing off, with eleven logged 429s on
// the way. Waiting the gap up front costs the same and wastes none of it.
//
// Keyed by host rather than applied globally because the limit belongs to the
// server, not to us: Datafordeleren pages a dense radius search a thousand
// nodes at a time and does not throttle, so pacing it would be pure loss.
var hostRequestGap = map[string]time.Duration{
	"api.boliga.dk": 11 * time.Second / 5,
}

func init() {
	DefaultClient = http.Client{
		Transport: &RetryRoundTripper{
			// Inside the retries, so a request resumed after a backoff still
			// waits its turn rather than jumping the queue.
			next: NewPaceRoundTripper(&DefaultHeadersTripper{
				next: http.DefaultTransport,
				headers: map[string]string{
					"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
				},
			}, hostRequestGap),
			maxRetries: 5,
		},
	}
}

// PaceRoundTripper holds requests to a throttled host apart by a fixed gap.
// Slots are handed out across every caller, so concurrent lookups share one
// host's allowance instead of each believing it has the whole of it.
type PaceRoundTripper struct {
	next http.RoundTripper
	gaps map[string]time.Duration

	mu       sync.Mutex
	nextSlot map[string]time.Time
}

func NewPaceRoundTripper(next http.RoundTripper, gaps map[string]time.Duration) *PaceRoundTripper {
	return &PaceRoundTripper{next: next, gaps: gaps, nextSlot: map[string]time.Time{}}
}

func (t *PaceRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if wait := t.reserve(req.URL.Host, time.Now()); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}

	return t.next.RoundTrip(req)
}

// reserve claims the next slot for host and reports how long to wait for it.
// The slot is claimed whether or not the caller ends up using it: a request
// abandoned while waiting has already been counted against the host, which
// errs towards being slower than the limit rather than faster.
func (t *PaceRoundTripper) reserve(host string, now time.Time) time.Duration {
	gap, ok := t.gaps[host]
	if !ok {
		return 0
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	slot := now
	if next := t.nextSlot[host]; next.After(now) {
		slot = next
	}
	t.nextSlot[host] = slot.Add(gap)

	return slot.Sub(now)
}

type DefaultHeadersTripper struct {
	next    http.RoundTripper
	headers map[string]string
}

func (t *DefaultHeadersTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range t.headers {
		req.Header.Add(k, v)
	}

	return t.next.RoundTrip(req)
}

type RetryRoundTripper struct {
	next       http.RoundTripper
	maxRetries int
}

func (r *RetryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error

	for i := 0; i <= r.maxRetries; i++ {
		resp, err = r.next.RoundTrip(req)

		if err != nil {
			// Transport/network error — retry with backoff
			if i < r.maxRetries {
				backoff := time.Duration(math.Pow(2.0, float64(i+1))) * time.Second // 2s, 4s, 8s, 16s, 32s
				log.Printf("HTTP retry %d/%d for %s: transport error: %v (backoff %s)",
					i+1, r.maxRetries, req.URL.Host, err, backoff)
				time.Sleep(backoff)
				continue
			}
			return nil, err
		}

		// Non-retryable — hand the response back, successful or not. This used
		// to read `< 429` under a "success" comment, so one comparison stood in
		// for both "did it work" and "is it worth trying again", and every 4xx
		// below 429 was quietly filed as a success. A 403 from a scrape target
		// does belong here — a 2s..32s backoff will not talk it round — but it
		// belongs here deliberately rather than by accident of the threshold.
		if resp.StatusCode != 429 && resp.StatusCode < 500 {
			return resp, nil
		}

		// Rate limited (429) or server error (5xx) — retry with backoff
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			if i < r.maxRetries {
				backoff := time.Duration(math.Pow(2.0, float64(i+1))) * time.Second // 2s, 4s, 8s, 16s, 32s
				log.Printf("HTTP retry %d/%d for %s: status %d (backoff %s)",
					i+1, r.maxRetries, req.URL.Host, resp.StatusCode, backoff)
				resp.Body.Close()
				time.Sleep(backoff)
				continue
			}
		}

		return resp, nil
	}

	return resp, err
}
