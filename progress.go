package hjem

import (
	"encoding/json"
	"sync"
	"time"
)

type ProgressStage string

const (
	StageIdle       ProgressStage = "idle"
	StageDawa       ProgressStage = "dawa"
	StageBoligaList ProgressStage = "boliga_list"
	StageBoligaProp ProgressStage = "boliga_properties"
	StageDone       ProgressStage = "done"
	StageError      ProgressStage = "error"
)

type ProgressEvent struct {
	Stage     ProgressStage `json:"stage"`
	Message   string        `json:"message"`
	Current   int           `json:"current"`
	Total     int           `json:"total"`
	ElapsedMs int64         `json:"elapsed_ms"`
	Warnings  []string      `json:"warnings,omitempty"`
	Result    interface{}   `json:"result,omitempty"`
}

type Progress struct {
	mu        sync.Mutex
	stage     ProgressStage
	message   string
	current   int
	total     int
	startedAt time.Time
	result    interface{}
	warnings  []string
	notify    chan struct{}
}

func NewProgress() *Progress {
	return &Progress{
		stage:  StageIdle,
		notify: make(chan struct{}, 1),
	}
}

func (p *Progress) Update(stage ProgressStage, message string, current, total int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.stage = stage
	p.message = message
	p.current = current
	p.total = total
	p.mu.Unlock()

	select {
	case p.notify <- struct{}{}:
	default:
	}
}

func (p *Progress) AddWarning(msg string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.warnings = append(p.warnings, msg)
	p.mu.Unlock()
}

func (p *Progress) SetResult(result interface{}) {
	p.mu.Lock()
	p.result = result
	p.mu.Unlock()
}

func (p *Progress) Reset() {
	p.mu.Lock()
	p.stage = StageIdle
	p.message = ""
	p.current = 0
	p.total = 0
	p.result = nil
	p.warnings = nil
	p.startedAt = time.Now()
	p.mu.Unlock()
}

func (p *Progress) Snapshot() ProgressEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	evt := ProgressEvent{
		Stage:     p.stage,
		Message:   p.message,
		Current:   p.current,
		Total:     p.total,
		ElapsedMs: time.Since(p.startedAt).Milliseconds(),
	}
	if len(p.warnings) > 0 {
		evt.Warnings = p.warnings
	}
	if p.stage == StageDone && p.result != nil {
		evt.Result = p.result
	}
	return evt
}

func (p *Progress) SnapshotJSON() []byte {
	snap := p.Snapshot()
	b, _ := json.Marshal(snap)
	return b
}

func (p *Progress) Wait() <-chan struct{} {
	return p.notify
}
