package hjem

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type HealthError struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
}

type HealthCache struct {
	Hits   int64 `json:"hits"`
	Misses int64 `json:"misses"`
}

type HealthBoliga struct {
	LastOK       *time.Time `json:"last_ok,omitempty"`
	LastFail     *time.Time `json:"last_fail,omitempty"`
	ErrorRatePct float64    `json:"error_rate_pct"`
	TotalOK      int64      `json:"total_ok"`
	TotalFail    int64      `json:"total_fail"`
}

type HealthResponse struct {
	UptimeSeconds int64         `json:"uptime_seconds"`
	TotalLookups  int64         `json:"total_lookups"`
	Cache         HealthCache   `json:"cache"`
	Boliga        HealthBoliga  `json:"boliga"`
	RecentErrors  []HealthError `json:"recent_errors"`
}

// lookupStages names every stage runLookup times, in the order it runs them.
// Listed up front rather than discovered from recorded samples so that every
// series exists on a server that has served no lookups: a metric that
// materialises on first use reads as "missing" at exactly the moment someone is
// checking whether the thing is broken.
var lookupStages = []string{"address", "nearby", "boliga", "format", "dingeo"}

// Dingeo outcomes, kept apart because they call for different responses:
// "ok" and "none" are both Dingeo answering, "unavailable" is Dingeo or
// Cloudflare refusing, and "unconfigured" is this deployment having no
// FlareSolverr to get past the bot check with — see ErrDingeoUnconfigured.
var dingeoResults = []string{"ok", "none", "unavailable", "unconfigured"}

type HealthStats struct {
	mu             sync.Mutex
	startedAt      time.Time
	totalLookups   int64
	cacheHits      int64
	cacheMisses    int64
	boligaOK       int64
	boligaFail     int64
	boligaLastOK   *time.Time
	boligaLastFail *time.Time
	recentErrors   []HealthError

	// Streets the browser was asked to fetch, and the subset it returned.
	// Exported as two counters rather than one ratio: a ratio divided here is
	// a lifetime average, and an instance that has been up for a month would
	// hide a relay that stopped working this morning. Two counters can be
	// rated over a window.
	relayStreets   int64
	relayOffloaded int64
	stageSeconds   map[string]float64
	stageRuns      map[string]int64
	dingeoByResult map[string]int64
}

func NewHealthStats() *HealthStats {
	return &HealthStats{
		startedAt:      time.Now(),
		stageSeconds:   map[string]float64{},
		stageRuns:      map[string]int64{},
		dingeoByResult: map[string]int64{},
	}
}

// RecordStage accumulates one stage's wall time. Seconds-and-count rather than
// a histogram: the interesting question is which stage owns a lookup's
// latency, and a measured production lookup spent 94% of itself in one of
// them, so the mean over a window answers it without buckets to choose.
func (h *HealthStats) RecordStage(stage string, d time.Duration) {
	h.mu.Lock()
	h.stageSeconds[stage] += d.Seconds()
	h.stageRuns[stage]++
	h.mu.Unlock()
}

// RecordRelayStreets records how much of a street list the browser covered.
// Counted in streets, not sales, for the same reason clientStreetFetcher
// counts them that way: an empty street is a successful fetch.
func (h *HealthStats) RecordRelayStreets(offloaded, total int) {
	h.mu.Lock()
	h.relayOffloaded += int64(offloaded)
	h.relayStreets += int64(total)
	h.mu.Unlock()
}

func (h *HealthStats) RecordDingeo(result string) {
	h.mu.Lock()
	h.dingeoByResult[result]++
	h.mu.Unlock()
}

// rateLimits counts throttling responses by host. Package-level, unlike every
// other counter here, because the only place that sees a 429 is
// RetryRoundTripper inside DefaultClient — a transport built in init(), long
// before any server and its HealthStats exist. Threading an instance into it
// would mean either a mutable global pointer set at NewServer time, which is a
// global with extra steps and a data race besides, or rebuilding DefaultClient
// per server, which would give each one its own pacing slots and so let two
// servers in one process exceed a host's limit together. The quantity is
// process-wide either way: the pacing it reflects is shared.
var rateLimits = struct {
	mu     sync.Mutex
	byHost map[string]int64
}{byHost: map[string]int64{}}

func recordRateLimit(host string) {
	rateLimits.mu.Lock()
	rateLimits.byHost[host]++
	rateLimits.mu.Unlock()
}

// rateLimitCounts reports the 429 tally, seeded with the hosts we already know
// throttle so their series exist before the first one is taken. Hosts outside
// that set appear only once they have actually refused us — there is no list of
// every host we might ever call.
func rateLimitCounts() map[string]int64 {
	out := map[string]int64{}
	for host := range hostRequestGap {
		out[host] = 0
	}
	rateLimits.mu.Lock()
	for host, n := range rateLimits.byHost {
		out[host] = n
	}
	rateLimits.mu.Unlock()
	return out
}

func (h *HealthStats) RecordLookup() {
	h.mu.Lock()
	h.totalLookups++
	h.mu.Unlock()
}

func (h *HealthStats) RecordCacheHit(n int) {
	h.mu.Lock()
	h.cacheHits += int64(n)
	h.mu.Unlock()
}

func (h *HealthStats) RecordCacheMiss(n int) {
	h.mu.Lock()
	h.cacheMisses += int64(n)
	h.mu.Unlock()
}

func (h *HealthStats) RecordBoligaOK() {
	h.mu.Lock()
	h.boligaOK++
	now := time.Now()
	h.boligaLastOK = &now
	h.mu.Unlock()
}

func (h *HealthStats) RecordBoligaFail(errType, msg string) {
	h.mu.Lock()
	h.boligaFail++
	now := time.Now()
	h.boligaLastFail = &now
	h.recentErrors = append(h.recentErrors, HealthError{
		Time:    now,
		Type:    errType,
		Message: msg,
	})
	// Keep only last 20 errors
	if len(h.recentErrors) > 20 {
		h.recentErrors = h.recentErrors[len(h.recentErrors)-20:]
	}
	h.mu.Unlock()
}

func (h *HealthStats) Snapshot() HealthResponse {
	h.mu.Lock()
	defer h.mu.Unlock()

	var errorRate float64
	total := h.boligaOK + h.boligaFail
	if total > 0 {
		errorRate = float64(h.boligaFail) / float64(total) * 100
	}

	return HealthResponse{
		UptimeSeconds: int64(time.Since(h.startedAt).Seconds()),
		TotalLookups:  h.totalLookups,
		Cache: HealthCache{
			Hits:   h.cacheHits,
			Misses: h.cacheMisses,
		},
		Boliga: HealthBoliga{
			LastOK:       h.boligaLastOK,
			LastFail:     h.boligaLastFail,
			ErrorRatePct: errorRate,
			TotalOK:      h.boligaOK,
			TotalFail:    h.boligaFail,
		},
		RecentErrors: h.recentErrors,
	}
}

func (h *HealthStats) PrometheusMetrics() string {
	h.mu.Lock()
	defer h.mu.Unlock()

	var b strings.Builder

	metric := func(name, help, typ string, value interface{}) {
		fmt.Fprintf(&b, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&b, "# TYPE %s %s\n", name, typ)
		fmt.Fprintf(&b, "%s %v\n", name, value)
	}

	uptime := time.Since(h.startedAt).Seconds()
	metric("hjem_uptime_seconds", "Time since server start in seconds.", "gauge", uptime)
	metric("hjem_lookups_total", "Total number of address lookups.", "counter", h.totalLookups)
	metric("hjem_cache_hits_total", "Total number of address cache hits.", "counter", h.cacheHits)
	metric("hjem_cache_misses_total", "Total number of address cache misses.", "counter", h.cacheMisses)

	fmt.Fprintf(&b, "# HELP hjem_boliga_requests_total Total Boliga API requests by result.\n")
	fmt.Fprintf(&b, "# TYPE hjem_boliga_requests_total counter\n")
	fmt.Fprintf(&b, "hjem_boliga_requests_total{result=\"ok\"} %d\n", h.boligaOK)
	fmt.Fprintf(&b, "hjem_boliga_requests_total{result=\"fail\"} %d\n", h.boligaFail)

	if h.boligaLastOK != nil {
		metric("hjem_boliga_last_ok_timestamp", "Unix timestamp of last successful Boliga request.", "gauge", h.boligaLastOK.Unix())
	}
	if h.boligaLastFail != nil {
		metric("hjem_boliga_last_fail_timestamp", "Unix timestamp of last failed Boliga request.", "gauge", h.boligaLastFail.Unix())
	}

	metric("hjem_boliga_relay_streets_total", "Streets handed to the browser to fetch from Boliga.", "counter", h.relayStreets)
	metric("hjem_boliga_relay_streets_offloaded_total", "Streets the browser fetched, sparing the server the request.", "counter", h.relayOffloaded)

	fmt.Fprintf(&b, "# HELP hjem_stage_duration_seconds_total Cumulative wall time spent in each lookup stage.\n")
	fmt.Fprintf(&b, "# TYPE hjem_stage_duration_seconds_total counter\n")
	for _, stage := range lookupStages {
		fmt.Fprintf(&b, "hjem_stage_duration_seconds_total{stage=%q} %v\n", stage, h.stageSeconds[stage])
	}
	fmt.Fprintf(&b, "# HELP hjem_stage_runs_total Times each lookup stage ran. Divide the duration by this for a mean.\n")
	fmt.Fprintf(&b, "# TYPE hjem_stage_runs_total counter\n")
	for _, stage := range lookupStages {
		fmt.Fprintf(&b, "hjem_stage_runs_total{stage=%q} %d\n", stage, h.stageRuns[stage])
	}

	fmt.Fprintf(&b, "# HELP hjem_dingeo_lookups_total Public valuation lookups by outcome.\n")
	fmt.Fprintf(&b, "# TYPE hjem_dingeo_lookups_total counter\n")
	for _, result := range dingeoResults {
		fmt.Fprintf(&b, "hjem_dingeo_lookups_total{result=%q} %d\n", result, h.dingeoByResult[result])
	}

	fmt.Fprintf(&b, "# HELP hjem_rate_limited_total Requests answered 429 by host.\n")
	fmt.Fprintf(&b, "# TYPE hjem_rate_limited_total counter\n")
	for host, n := range rateLimitCounts() {
		fmt.Fprintf(&b, "hjem_rate_limited_total{host=%q} %d\n", host, n)
	}

	// Error counts by type
	errCounts := map[string]int{}
	for _, e := range h.recentErrors {
		errCounts[e.Type]++
	}
	if len(errCounts) > 0 {
		fmt.Fprintf(&b, "# HELP hjem_recent_errors Recent errors by type (last 20).\n")
		fmt.Fprintf(&b, "# TYPE hjem_recent_errors gauge\n")
		for typ, count := range errCounts {
			fmt.Fprintf(&b, "hjem_recent_errors{type=%q} %d\n", typ, count)
		}
	}

	return b.String()
}
