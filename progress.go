package hjem

import (
	"encoding/json"
	"sync"
	"time"
)

type ProgressStage string

const (
	StageIdle ProgressStage = "idle"
	StageDawa ProgressStage = "dawa"
	// StageBoligaClient hands the browser a list of streets to fetch from
	// api.boliga.dk itself. The server blocks here until the client posts
	// results back, or until it gives up waiting and fetches them itself.
	StageBoligaClient ProgressStage = "boliga_client"
	StageBoligaList   ProgressStage = "boliga_list"
	StageBoligaProp   ProgressStage = "boliga_properties"
	StageDone         ProgressStage = "done"
	StageError        ProgressStage = "error"
)

type ProgressEvent struct {
	Stage     ProgressStage `json:"stage"`
	Message   string        `json:"message"`
	Current   int           `json:"current"`
	Total     int           `json:"total"`
	ElapsedMs int64         `json:"elapsed_ms"`
	Warnings  []string      `json:"warnings,omitempty"`
	Result    interface{}   `json:"result,omitempty"`

	// BoligaTasks is the street list the client should fetch. Sent only while
	// StageBoligaClient is current: the client polls every couple of seconds
	// and has no use for the list once it has started, so repeating it on every
	// later poll would be pure payload.
	BoligaTasks []BoligaPropertyRequest `json:"boliga_tasks,omitempty"`

	// Map is the loading visualization's data. Sent on the first few Boliga-stage
	// snapshots only — see mapPlanSends. Unlike BoligaTasks this cannot be gated
	// on the stage alone: StageBoligaList is re-set on every server-side street
	// fetch, so it spans most of a long lookup.
	Map *MapPlan `json:"map,omitempty"`
}

type Progress struct {
	mu          sync.Mutex
	stage       ProgressStage
	message     string
	current     int
	total       int
	startedAt   time.Time
	finishedAt  time.Time
	result      interface{}
	warnings    []string
	boligaTasks []BoligaPropertyRequest
	mapPlan     *MapPlan
	mapPlanLeft int
	notify      chan struct{}

	// now exists so the session store can hold this progress to the same clock
	// it evicts by. The two are compared against each other — a finished
	// lookup is kept for a while after it finished — and mixing a frozen test
	// clock with the wall clock makes that comparison meaningless.
	now func() time.Time
}

func NewProgress() *Progress {
	return &Progress{
		stage: StageIdle,
		// Set here rather than on first Update, because Snapshot reports
		// elapsed time relative to it and a zero value would read as decades.
		startedAt: time.Now(),
		notify:    make(chan struct{}, 1),
		now:       time.Now,
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
	// First terminal stage wins. A lookup that errors after reporting done, or
	// vice versa, should not have its retention clock pushed forward.
	if p.finishedAt.IsZero() && (stage == StageDone || stage == StageError) {
		p.finishedAt = p.now()
	}
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

// SetBoligaTasks records the street list for the client to fetch. Call it
// before advancing to StageBoligaClient, never after: a client that polls
// between the two would see the stage it acts on with no list to act on, and
// would report every task failed.
func (p *Progress) SetBoligaTasks(tasks []BoligaPropertyRequest) {
	p.mu.Lock()
	p.boligaTasks = tasks
	p.mu.Unlock()
}

// mapPlanSends is how many snapshots carry the map plan before it stops being
// sent. The client keeps the first copy it sees, so one would do; the spares
// are there so a single dropped poll does not cost the whole map. A plan is a
// few thousand coordinates — tens of kilobytes — and a long lookup is polled a
// hundred times, so sending it on every one of them would be megabytes of
// identical payload the client throws away.
const mapPlanSends = 3

// SetMapPlan records the loading visualization's data. Call it before
// advancing past StageDawa: the plan only ships on the Boliga stages, so one
// set afterwards would never be seen.
func (p *Progress) SetMapPlan(plan *MapPlan) {
	p.mu.Lock()
	p.mapPlan = plan
	p.mapPlanLeft = mapPlanSends
	p.mu.Unlock()
}

func (p *Progress) SetResult(result interface{}) {
	p.mu.Lock()
	p.result = result
	p.mu.Unlock()
}

// Finished reports whether this lookup has reached a terminal stage. The
// session store uses it to tell sessions that still hold a running goroutine
// from ones that only hold a result.
func (p *Progress) Finished() bool {
	_, ok := p.FinishedAt()
	return ok
}

// FinishedAt reports when this lookup reached a terminal stage. The session
// store retains a finished lookup for a while afterwards, and it has to
// measure that from here: measuring from when the lookup *started* would throw
// away precisely the slow results, since a lookup that ran longer than the
// retention window is already expired the moment it completes.
func (p *Progress) FinishedAt() (time.Time, bool) {
	if p == nil {
		return time.Time{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.finishedAt, !p.finishedAt.IsZero()
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
	if p.stage == StageBoligaClient {
		evt.BoligaTasks = p.boligaTasks
	}
	if p.mapPlanLeft > 0 && (p.stage == StageBoligaList || p.stage == StageBoligaClient) {
		p.mapPlanLeft--
		evt.Map = p.mapPlan
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
