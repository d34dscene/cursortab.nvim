package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cursortab/assert"
)

func readEvents(t *testing.T, path string) []Event {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open trace: %v", err)
	}
	defer file.Close()

	var events []Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode trace line: %v", err)
		}
		events = append(events, event)
	}
	assert.NoError(t, scanner.Err(), "scan")
	return events
}

func TestRecorderWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	now := func() time.Time { return time.Unix(0, 1234) }

	recorder, err := NewRecorder(dir, now)
	assert.NoError(t, err, "new recorder")
	assert.Contains(t, recorder.Path(), "session-", "session file name")

	confidence := -0.3
	recorder.Record(Event{Type: EventRequest, Source: "typing", Path: "a.go", RequestID: 7})
	recorder.Record(Event{Type: EventShown, Path: "a.go", LatencyMs: 42, Confidence: &confidence})
	recorder.Record(Event{Type: EventAccepted, Path: "a.go"})
	assert.NoError(t, recorder.Close(), "close")

	events := readEvents(t, recorder.Path())
	assert.Len(t, 3, events, "one line per event")

	assert.Equal(t, EventRequest, events[0].Type, "first event type")
	assert.Equal(t, uint64(7), events[0].RequestID, "request id")
	assert.Equal(t, int64(1234), events[0].Time.UnixNano(), "time from injected clock")

	assert.Equal(t, int64(42), events[1].LatencyMs, "latency recorded")
	assert.NotNil(t, events[1].Confidence, "confidence recorded")
	assert.Equal(t, -0.3, *events[1].Confidence, "confidence value")

	assert.Equal(t, EventAccepted, events[2].Type, "third event type")
}

func TestRecorderNilIsNoop(t *testing.T) {
	var recorder *Recorder
	recorder.Record(Event{Type: EventRequest})
	assert.Nil(t, recorder.Close(), "nil recorder close")
	assert.Equal(t, "", recorder.Path(), "nil recorder has no path")
}

func TestRecorderCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "sessions")
	recorder, err := NewRecorder(dir, nil)
	assert.NoError(t, err, "new recorder")
	defer recorder.Close()

	_, statErr := os.Stat(recorder.Path())
	assert.NoError(t, statErr, "session file exists")
}
