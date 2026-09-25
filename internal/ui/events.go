package ui

import (
	"encoding/json"
	"io"
	"slices"
	"sync"
	"time"
)

// EventType enumerates the kinds of lifecycle events the research driver
// emits. The UI renderer and the headless JSONL sink both consume them.
type EventType string

const (
	// Phase marks a high-level pipeline phase (plan, research, analyze,
	// fact-check, summarize, report).
	Phase EventType = "phase"
	// Search is a fresh search query issued by a sub-agent.
	Search EventType = "search"
	// Read is a page fetch/scrape that produced source content.
	Read EventType = "read"
	// Verify reports the quality/availability of a specific URL.
	Verify EventType = "verify"
	// Citation records a distinct verified source added to the context store.
	Citation EventType = "citation"
	// Token updates the running token/cost tally.
	Token EventType = "token"
	// SubAgent toggles the state of a research sub-agent node.
	SubAgent EventType = "subagent"
	// Report marks completion of the final report.
	Report EventType = "report"
	// Detach reports that the run was sent to the background.
	Detach EventType = "detach"
	// Info is a non-fatal progress line — which model is being asked, that a
	// call is being retried, how many results a search returned. It is its own
	// type because routing these through Error rendered ordinary progress with
	// the failure glyph and logged a healthy run as a stream of errors.
	Info EventType = "info"
	// Error surfaces a fatal or non-fatal failure.
	Error EventType = "error"
)

// Event is a single, self-describing signal emitted by the driver. Every
// field is optional; consumers key off Type and Phase.
type Event struct {
	Type EventType `json:"type"`
	Time time.Time `json:"time"`

	// High-level phase name, e.g. "Research".
	Phase string `json:"phase,omitempty"`
	// Detail is a free-text status line attached to the phase.
	Detail string `json:"detail,omitempty"`

	// Search / Read.
	Query  string `json:"query,omitempty"`
	URL    string `json:"url,omitempty"`
	Domain string `json:"domain,omitempty"`
	// SourceTitle / SourceURL identify the source a citation refers to.
	SourceTitle string `json:"source_title,omitempty"`
	SourceURL   string `json:"source_url,omitempty"`

	// Verify: status is one of ok / degraded / dropped; Code carries the
	// HTTP status or failure reason behind a degraded or dropped source.
	Status string `json:"status,omitempty"`
	Code   string `json:"code,omitempty"`

	// Token / Citation running totals.
	Tokens  int `json:"tokens,omitempty"`
	Sources int `json:"sources,omitempty"`

	// SubAgent node identity/state.
	SubID    string `json:"sub_id,omitempty"`
	SubName  string `json:"sub_name,omitempty"`
	SubState string `json:"sub_state,omitempty"` // running | queued | done | error
	Progress int    `json:"progress,omitempty"`
	// Line is the latest status line shown under a sub-agent node.
	Line string `json:"line,omitempty"`
	// Transient marks progress that replaces the previous status rather than
	// adding to the history. The live frame shows only the latest; JSONL and
	// the exported timeline keep every one.
	Transient bool `json:"transient,omitempty"`
}

func (e Event) withTime() Event {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	return e
}

// Sink consumes events. The renderer and the JSONL logger both implement it.
type Sink interface {
	Emit(Event)
}

// MultiSink fans a single event out to every registered sink and always
// appends the event to its own in-memory timeline for later export. Emit is
// safe for concurrent use: the driver fans sub-agents out in parallel and each
// may emit simultaneously, so the mutex keeps every sink and the timeline
// consistent one event at a time.
type MultiSink struct {
	mu       sync.Mutex
	Sinks    []Sink
	Timeline []Event
}

func (m *MultiSink) Emit(e Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e = e.withTime()
	m.Timeline = append(m.Timeline, e)
	for _, s := range m.Sinks {
		if s == nil {
			continue
		}
		s.Emit(e)
	}
}

// TimelineSnapshot returns a copy of the recorded timeline taken under the
// lock. Exports run while the key reader can still emit (a late detach), and
// ranging over the live slice from another goroutine is a data race.
func (m *MultiSink) TimelineSnapshot() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.Timeline)
}

// JSONL writes one compact JSON object per event. It is used for headless /
// CI integration where structured streaming is preferred over a TUI.
type JSONL struct {
	W io.Writer
}

func (j JSONL) Emit(e Event) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = j.W.Write(b)
	_, _ = io.WriteString(j.W, "\n")
}
