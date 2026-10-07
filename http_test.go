package hjem

import (
	"testing"
	"time"
)

// Asserting on reserve() rather than on wall-clock RoundTrip timings: the
// property that matters is the spacing of the slots, and a test that actually
// slept for them would take as long as the thing it is checking.
func TestPaceRoundTripperSpacesRequests(t *testing.T) {
	p := NewPaceRoundTripper(nil, map[string]time.Duration{"paced.example": time.Second})
	now := time.Now()

	for i, want := range []time.Duration{0, time.Second, 2 * time.Second} {
		if got := p.reserve("paced.example", now); got != want {
			t.Errorf("request %d: waited %s, want %s", i, got, want)
		}
	}
}

// The gap is a property of the host, so everything else must be untouched —
// pacing DAR's thousand-node pages would cost minutes per lookup for nothing.
func TestPaceRoundTripperIgnoresOtherHosts(t *testing.T) {
	p := NewPaceRoundTripper(nil, map[string]time.Duration{"paced.example": time.Second})
	now := time.Now()

	for i := 0; i < 3; i++ {
		if got := p.reserve("other.example", now); got != 0 {
			t.Errorf("request %d to unpaced host waited %s, want 0", i, got)
		}
	}
}

// An idle period is not credit towards a burst: a caller arriving after the
// slot has passed goes immediately, but the next one still waits a full gap.
func TestPaceRoundTripperDoesNotBankIdleTime(t *testing.T) {
	p := NewPaceRoundTripper(nil, map[string]time.Duration{"paced.example": time.Second})
	now := time.Now()

	p.reserve("paced.example", now)
	later := now.Add(10 * time.Second)

	if got := p.reserve("paced.example", later); got != 0 {
		t.Errorf("after idling, waited %s, want 0", got)
	}
	if got := p.reserve("paced.example", later); got != time.Second {
		t.Errorf("following request waited %s, want %s", got, time.Second)
	}
}

// Asserting on the assembled client rather than on hostRequestGap, which would
// only restate its own literal. Boliga is the one host the lookup's timing is
// built around, and unhooking the pacer from the transport chain would restore
// the four-minute lookup while leaving the map entry sitting there intact.
func TestBoligaIsPacedByDefaultClient(t *testing.T) {
	retry, ok := DefaultClient.Transport.(*RetryRoundTripper)
	if !ok {
		t.Fatalf("DefaultClient.Transport is %T, want *RetryRoundTripper", DefaultClient.Transport)
	}
	pace, ok := retry.next.(*PaceRoundTripper)
	if !ok {
		t.Fatalf("RetryRoundTripper wraps %T, want *PaceRoundTripper", retry.next)
	}
	if gap := pace.gaps["api.boliga.dk"]; gap <= 0 {
		t.Fatalf("api.boliga.dk gap is %s, want a positive spacing", gap)
	}
}
