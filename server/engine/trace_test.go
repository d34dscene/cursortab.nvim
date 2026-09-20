package engine

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	"cursortab/assert"
	"cursortab/session"
	"cursortab/types"
)

func tracedEngine(t *testing.T) (*Engine, *session.Recorder) {
	t.Helper()
	recorder, err := session.NewRecorder(t.TempDir(), nil)
	assert.NoError(t, err, "new recorder")

	buf := newMockBuffer()
	buf.lines = []string{"line 1"}
	eng, cancel := createTestEngineWithContext(buf, newMockProvider(), newMockClock())
	t.Cleanup(cancel)
	eng.tracer = recorder
	return eng, recorder
}

func readTrace(t *testing.T, path string) []session.Event {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open trace: %v", err)
	}
	defer file.Close()

	var events []session.Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event session.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		events = append(events, event)
	}
	return events
}

func TestTraceRecordsRequestShownAndAccepted(t *testing.T) {
	eng, recorder := tracedEngine(t)

	eng.traceRequest(types.CompletionSourceTyping, 3)
	outcome := eng.processCompletion(completionResponse(&types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"line 1 changed"},
	}))
	assert.Equal(t, completionShown, outcome, "completion shown")

	eng.acceptCompletion()
	assert.NoError(t, recorder.Close(), "close")

	events := readTrace(t, recorder.Path())
	assert.Len(t, 3, events, "request, shown, accepted")
	assert.Equal(t, session.EventRequest, events[0].Type, "request recorded first")
	assert.Equal(t, uint64(3), events[0].RequestID, "request id recorded")
	assert.Equal(t, session.EventShown, events[1].Type, "shown recorded")
	assert.Equal(t, session.EventAccepted, events[2].Type, "accepted recorded")
}

func TestTraceRecordsRejection(t *testing.T) {
	eng, recorder := tracedEngine(t)

	outcome := eng.processCompletion(completionResponse(&types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"line 1 changed"},
	}))
	assert.Equal(t, completionShown, outcome, "completion shown")

	eng.reject()
	assert.NoError(t, recorder.Close(), "close")

	events := readTrace(t, recorder.Path())
	assert.Len(t, 2, events, "shown and rejected")
	assert.Equal(t, session.EventShown, events[0].Type, "shown recorded")
	assert.Equal(t, session.EventRejected, events[1].Type, "rejected recorded")
}
