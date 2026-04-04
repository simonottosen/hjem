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
	ErrorRatePct float64   `json:"error_rate_pct"`
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

type HealthStats struct {
	mu           sync.Mutex
	startedAt    time.Time
	totalLookups int64
	cacheHits    int64
	cacheMisses  int64
	boligaOK     int64
	boligaFail   int64
	boligaLastOK *time.Time
	boligaLastFail *time.Time
	recentErrors []HealthError
}

func NewHealthStats() *HealthStats {
	return &HealthStats{
		startedAt: time.Now(),
	}
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
