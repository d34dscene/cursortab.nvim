// Package session records local completion sessions for offline evaluation.
//
// A trace is a JSONL stream of request, show, accept, and reject events with
// timing and confidence. That is enough to compute precision, latency, and
// per-keystroke stability for real editing sessions without a model in the
// loop, which is what tuning gating and retrieval needs.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// EventType classifies a recorded event.
type EventType string

const (
	EventRequest    EventType = "request"    // a completion was requested
	EventShown      EventType = "shown"      // a completion became visible
	EventAccepted   EventType = "accepted"   // the user accepted a completion
	EventRejected   EventType = "rejected"   // the completion was dropped
	EventSuppressed EventType = "suppressed" // the request was gated before a model call
)

// Event is one line in a session trace.
type Event struct {
	Time       time.Time `json:"time"`
	Type       EventType `json:"type"`
	Source     string    `json:"source,omitempty"` // typing, idle
	Path       string    `json:"path,omitempty"`
	RequestID  uint64    `json:"request_id,omitempty"`
	StartLine  int       `json:"start_line,omitempty"`
	EndLine    int       `json:"end_line,omitempty"`
	Lines      int       `json:"lines,omitempty"`
	Confidence *float64  `json:"confidence,omitempty"`
	LatencyMs  int64     `json:"latency_ms,omitempty"`
	Reason     string    `json:"reason,omitempty"`
}

// Recorder appends events to a per-session JSONL file. It is safe for
// concurrent use. A nil Recorder is valid and drops every event.
type Recorder struct {
	mu   sync.Mutex
	path string
	file *os.File
	enc  *json.Encoder
	now  func() time.Time
}

// NewRecorder creates a session file under dir and returns a Recorder writing
// to it. Pass a nil now to use the wall clock.
func NewRecorder(dir string, now func() time.Time) (*Recorder, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("session: create dir: %w", err)
	}
	if now == nil {
		now = time.Now
	}
	name := fmt.Sprintf("session-%d.jsonl", now().UnixNano())
	path := filepath.Join(dir, name)

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("session: open trace: %w", err)
	}

	return &Recorder{path: path, file: file, enc: json.NewEncoder(file), now: now}, nil
}

// Record appends one event. It is a no-op on a nil Recorder.
func (r *Recorder) Record(event Event) {
	if r == nil {
		return
	}
	if event.Time.IsZero() {
		event.Time = r.now()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return
	}
	// Trace writes must never take down a completion, so encoding errors are
	// dropped rather than surfaced.
	_ = r.enc.Encode(event)
}

// Path is the trace file location.
func (r *Recorder) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

// Close flushes and closes the trace file. It is a no-op on a nil Recorder.
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}
