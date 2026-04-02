package hjem

import (
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
