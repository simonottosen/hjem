package hjem

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

// captureLog points the standard logger at a buffer for the rest of the test,
// without timestamps, so lines can be matched exactly.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(out)
		log.SetFlags(flags)
	})
	return &buf
}

func lookupLines(logged string) []string {
	var lines []string
	for _, l := range strings.Split(logged, "\n") {
		if strings.HasPrefix(l, "lookup ") {
			lines = append(lines, l)
		}
	}
	return lines
}

func requireAll(t *testing.T, line string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(line, w) {
			t.Errorf("summary line lacks %s\n  %s", w, line)
		}
	}
}

// A failed lookup reports its failure to the browser through the session's
// progress, and that was all: nothing reached the server log. So this drives
// a real lookup into its first error exit and expects exactly one summary line
// naming the failure — which is what the defer in runLookup is for, since
// that exit, like the five others, returns without a word.
func TestFailedLookupIsLogged(t *testing.T) {
	logged := captureLog(t)

	s := &server{sessions: newSessionStore(), dc: refusingCacher{}, stats: NewHealthStats()}
	sess, err := s.sessions.Create("")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	s.runLookup(sess, "Flensborggade 40, 4. tv", []int{250}, 0)

	lines := lookupLines(logged.String())
	if len(lines) != 1 {
		t.Fatalf("got %d summary lines, want 1:\n%s", len(lines), logged)
	}
	requireAll(t, lines[0],
		"lookup "+sess.ID[:8]+" error ",
		`query="Flensborggade 40, 4. tv"`,
		"radius=250",
		`err="no address service in this test"`,
		"stages=address:",
	)

	// The id is the lookup's access control; the log gets a prefix of it only.
	if strings.Contains(logged.String(), sess.ID) {
		t.Errorf("the full lookup id reached the log")
	}
}

// What a finished lookup reports is what its page shows, absences included:
// a missing comps estimate or an unreachable Dingeo has to be legible in the
// line, not inferred from a key that is not there.
func TestFinishedLookupReportsItsEstimates(t *testing.T) {
	logged := captureLog(t)

	fetched := time.Date(2026, 11, 2, 12, 40, 0, 0, time.UTC)
	rec := &lookupLog{
		id: "0123456789abcdef0123456789abcdef", query: "Flensborggade 40", ranges: []int{250, 500},
		startedAt: time.Now(), addrs: 3589, relayFetched: 0, relayTasks: 24,
		stages: []lookupStage{{"address", 500 * time.Millisecond}, {"boliga", 94 * time.Second}},
		resp: &LookupResponse{
			Sales: []*JSONSale{
				{When: time.Date(2026, 8, 27, 22, 0, 0, 0, time.UTC)},
				{When: time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)},
			},
			SalesFetchedAt:       &fetched,
			CompsEstimate:        &CompsEstimate{Value: 5_800_000, SqmPrice: 77_333, NumComps: 30, Confidence: "medium"},
			ValuationUnavailable: true,
			SquareMeters: SquareMeterPrices{Global: map[time.Time]Aggregation{
				time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC): {Mean: 88_000, N: 40},
			}},
			Warnings: []string{"one"},
		},
	}
	rec.emit(StageDone, "Færdig!")

	lines := lookupLines(logged.String())
	if len(lines) != 1 {
		t.Fatalf("got %d summary lines, want 1:\n%s", len(lines), logged)
	}
	requireAll(t, lines[0],
		"lookup 01234567 done ",
		"radius=250,500",
		"addrs=3589",
		"relay=0/24",
		"sales=2",
		"newest=2026-08-27",
		"fetched=2026-11-02",
		"comps=5800000(77333/m2,n=30,medium)",
		"sqm=88000/m2@2026",
		"public=unavailable",
		"warnings=1",
		"stages=address:500ms,boliga:1m34s",
	)
	if strings.Contains(lines[0], "err=") {
		t.Errorf("a finished lookup reported an error: %s", lines[0])
	}
}

// runLookup returns without a terminal stage only from its cancellation
// checks, so that is what an unfinished stage has to read as.
func TestUnfinishedLookupReadsAsCancelled(t *testing.T) {
	logged := captureLog(t)
	(&lookupLog{id: "abc", startedAt: time.Now()}).emit(StageBoligaList, "Henter salgslister fra Boliga...")
	requireAll(t, logged.String(), "lookup abc cancelled ")
}
