package hjem

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func assertMetric(t *testing.T, out, line string) {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if l == line {
			return
		}
	}
	t.Fatalf("missing metric line %q in:\n%s", line, out)
}

// An operator checks /metrics precisely when they suspect something stopped
// working, and a series that only appears after its first event reads as
// "missing" at that moment — indistinguishable from the metric having been
// dropped. Every new series must therefore exist at zero.
func TestPrometheusMetricsExistBeforeAnyLookup(t *testing.T) {
	out := NewHealthStats().PrometheusMetrics()

	assertMetric(t, out, "hjem_boliga_relay_streets_total 0")
	assertMetric(t, out, "hjem_boliga_relay_streets_offloaded_total 0")
	assertMetric(t, out, `hjem_rate_limited_total{host="api.boliga.dk"} 0`)

	for _, stage := range lookupStages {
		assertMetric(t, out, `hjem_stage_duration_seconds_total{stage="`+stage+`"} 0`)
		assertMetric(t, out, `hjem_stage_runs_total{stage="`+stage+`"} 0`)
	}
	for _, result := range dingeoResults {
		assertMetric(t, out, `hjem_dingeo_lookups_total{result="`+result+`"} 0`)
	}

	for _, name := range []string{
		"hjem_boliga_relay_streets_total",
		"hjem_boliga_relay_streets_offloaded_total",
		"hjem_stage_duration_seconds_total",
		"hjem_stage_runs_total",
		"hjem_dingeo_lookups_total",
		"hjem_rate_limited_total",
	} {
		assertMetric(t, out, "# TYPE "+name+" counter")
		if !strings.Contains(out, "# HELP "+name+" ") {
			t.Fatalf("missing HELP line for %s in:\n%s", name, out)
		}
	}
}

// The offload ratio is the number that says whether the client relay is doing
// its job. It is exported as its two counters so Prometheus can rate it over a
// window rather than over the instance's whole uptime.
func TestPrometheusRelayCountersAccumulate(t *testing.T) {
	h := NewHealthStats()
	h.RecordRelayStreets(6, 27)
	h.RecordRelayStreets(24, 24)

	out := h.PrometheusMetrics()
	assertMetric(t, out, "hjem_boliga_relay_streets_total 51")
	assertMetric(t, out, "hjem_boliga_relay_streets_offloaded_total 30")
}

func TestPrometheusStageAndDingeoCounters(t *testing.T) {
	h := NewHealthStats()
	h.RecordStage("boliga", 90*time.Second)
	h.RecordStage("boliga", 30*time.Second)
	h.RecordStage("dingeo", 500*time.Millisecond)
	h.RecordDingeo("unavailable")
	h.RecordDingeo("unavailable")
	h.RecordDingeo("none")

	out := h.PrometheusMetrics()
	assertMetric(t, out, `hjem_stage_duration_seconds_total{stage="boliga"} 120`)
	assertMetric(t, out, `hjem_stage_runs_total{stage="boliga"} 2`)
	assertMetric(t, out, `hjem_stage_duration_seconds_total{stage="dingeo"} 0.5`)
	assertMetric(t, out, `hjem_dingeo_lookups_total{result="unavailable"} 2`)
	assertMetric(t, out, `hjem_dingeo_lookups_total{result="none"} 1`)
	assertMetric(t, out, `hjem_dingeo_lookups_total{result="ok"} 0`)
}

// The 429 tally lives outside HealthStats, so it is asserted by delta rather
// than against an absolute: other tests in this package share the counter.
//
// Driven through a RetryRoundTripper with the retries turned off, because the
// real one sleeps 2s..32s between five attempts and this is about what gets
// counted, not about the backoff ladder.
func TestRateLimitCountedByHost(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	host := strings.TrimPrefix(ts.URL, "http://")
	before := rateLimitCounts()[host]

	client := http.Client{Transport: &RetryRoundTripper{next: http.DefaultTransport}}
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()

	want := before + 1
	if got := rateLimitCounts()[host]; got != want {
		t.Fatalf("count = %d, want %d", got, want)
	}
	assertMetric(t, NewHealthStats().PrometheusMetrics(),
		`hjem_rate_limited_total{host="`+host+`"} `+strconv.FormatInt(want, 10))
}
