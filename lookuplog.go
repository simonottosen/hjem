package hjem

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// lookupLog accumulates what one lookup did, so that it can be reported as a
// single line when the lookup returns. The stage timings also go to
// Prometheus, but a counter cannot say which address produced them, and the
// question asked of a live search is almost always about one address: why did
// this one come back empty, or slow, or with nothing newer than August.
//
// No mutex. Every field is written from runLookup's own goroutine: FetchSales
// calls the street fetcher synchronously, so even the relay counts, filled in
// further down the stack, arrive on that same goroutine.
type lookupLog struct {
	id        string
	query     string
	ranges    []int
	startedAt time.Time

	stages []lookupStage

	// addrs is how many addresses the radius search returned. Kept apart from
	// the response because it is known even when the lookup later dies in
	// Boliga, and on a lookup that comes back empty it is what says whether the
	// radius was the problem.
	addrs int

	relayFetched, relayTasks int

	// resp is nil unless the lookup got far enough to build one.
	resp *LookupResponse
}

type lookupStage struct {
	name string
	took time.Duration
}

// emit writes the summary line. It takes the outcome as arguments so the
// caller can pass Progress.Outcome() straight in.
//
// Space-separated key=value in the same plain log as everything else, not
// JSON: these lines are read by eye in `docker logs` first and parsed second,
// and one JSON line per lookup among the printf lines is worse at both.
func (l *lookupLog) emit(outcome ProgressStage, message string) {
	var b strings.Builder

	fmt.Fprintf(&b, "lookup %s %s took=%s query=%q",
		shortLookupID(l.id), outcomeName(outcome),
		time.Since(l.startedAt).Round(time.Millisecond), l.query)

	if len(l.ranges) > 0 {
		radii := make([]string, len(l.ranges))
		for i, r := range l.ranges {
			radii[i] = strconv.Itoa(r)
		}
		fmt.Fprintf(&b, " radius=%s", strings.Join(radii, ","))
	}
	if outcome == StageError {
		fmt.Fprintf(&b, " err=%q", message)
	}
	if l.addrs > 0 {
		fmt.Fprintf(&b, " addrs=%d", l.addrs)
	}
	if l.relayTasks > 0 {
		fmt.Fprintf(&b, " relay=%d/%d", l.relayFetched, l.relayTasks)
	}
	if l.resp != nil {
		writeResult(&b, l.resp)
	}
	if len(l.stages) > 0 {
		stages := make([]string, len(l.stages))
		for i, s := range l.stages {
			stages[i] = s.name + ":" + s.took.Round(time.Millisecond).String()
		}
		fmt.Fprintf(&b, " stages=%s", strings.Join(stages, ","))
	}

	log.Print(b.String())
}

// writeResult reports what the page will show. Absent estimates are named
// rather than left out: "this address got no comps estimate" is the answer
// being looked for when a page comes back half empty, and a missing key would
// read the same as a lookup that never got that far.
func writeResult(b *strings.Builder, r *LookupResponse) {
	fmt.Fprintf(b, " sales=%d", len(r.Sales))

	var newest time.Time
	for _, s := range r.Sales {
		if s.When.After(newest) {
			newest = s.When
		}
	}
	if !newest.IsZero() {
		fmt.Fprintf(b, " newest=%s", newest.Format(time.DateOnly))
	}
	if r.SalesFetchedAt != nil {
		fmt.Fprintf(b, " fetched=%s", r.SalesFetchedAt.Format(time.DateOnly))
	}

	if c := r.CompsEstimate; c != nil {
		fmt.Fprintf(b, " comps=%d(%d/m2,n=%d,%s)", c.Value, c.SqmPrice, c.NumComps, c.Confidence)
	} else {
		b.WriteString(" comps=none")
	}

	if mean, year := latestAggMean(r.SquareMeters.Global); mean > 0 {
		fmt.Fprintf(b, " sqm=%.0f/m2@%d", mean, year.Year())
	} else {
		b.WriteString(" sqm=none")
	}

	switch {
	case r.Valuation != nil:
		fmt.Fprintf(b, " public=%.0f(n=%d)", r.Valuation.Mean, r.Valuation.CountEvals)
	case r.ValuationUnavailable:
		b.WriteString(" public=unavailable")
	default:
		b.WriteString(" public=none")
	}

	if n := len(r.Warnings); n > 0 {
		fmt.Fprintf(b, " warnings=%d", n)
	}
}

func outcomeName(stage ProgressStage) string {
	if stage == StageDone || stage == StageError {
		return string(stage)
	}
	// runLookup returns without reaching a terminal stage only from its
	// cancellation checks: the client started another search, or the overrun
	// valve fired.
	return "cancelled"
}

// shortLookupID is as much of the session id as correlating lines within one
// log needs. Deliberately not all of it: the id is the access control on a
// lookup — anyone holding one can cancel that search and read its result —
// and logs are shipped off the host, pasted into issues and kept far longer
// than the fifteen minutes a session lives.
func shortLookupID(id string) string {
	const keep = 8
	if len(id) > keep {
		return id[:keep]
	}
	return id
}
