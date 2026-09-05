package engine

import (
	"testing"
	"time"

	"cursortab/assert"
	sourcectx "cursortab/ctx"
	"cursortab/types"
)

// calls reads the provider's completion call count under its lock.
func (p *mockProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.completionCalls
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func waitForEvent(t *testing.T, eng *Engine) Event {
	t.Helper()
	select {
	case ev := <-eng.eventChan:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for engine event")
		return Event{}
	}
}

func assertNoEvent(t *testing.T, eng *Engine) {
	t.Helper()
	select {
	case ev := <-eng.eventChan:
		t.Fatalf("unexpected event: %v", ev.Type)
	case <-time.After(50 * time.Millisecond):
	}
}

func nextEditEditResponse() *types.CompletionResponse {
	return &types.CompletionResponse{
		Completion: &types.Completion{
			StartLine:  2,
			EndLineInc: 2,
			Lines:      []string{"edited line 2"},
		},
	}
}

// driveGhostShown runs a typing request through the engine until the FIM
// completion is displayed, leaving the next-edit pause timer armed.
func driveGhostShown(t *testing.T, eng *Engine) *types.Completion {
	t.Helper()
	eng.handleEvent(Event{Type: EventTextChangeTimeout})
	eng.handleEvent(waitForEvent(t, eng))

	assert.True(t, eng.state == stateHasCompletion, "state should be HasCompletion")
	assert.NotNil(t, eng.display.current(), "ghost should be displayed")
	assert.NotNil(t, eng.nextEditTimer, "next-edit timer should be armed")
	return eng.display.current()
}

func TestDualMode_NextEditSwapsGhostAfterPause(t *testing.T) {
	buf := newMockBuffer()
	main := newMockProviderWithKind(CompletionFIM)
	ne := newMockProviderWithKind(CompletionEdit)
	ne.completionResp = nextEditEditResponse()
	ne.materials = sourcectx.Materials{}

	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, main, clock)
	defer cancel()
	eng.nextEditProvider = ne
	eng.config.NextEditIdleDelay = 100 * time.Millisecond

	ghost := driveGhostShown(t, eng)
	assert.Equal(t, 0, ne.calls(), "next-edit should not be called yet")

	clock.Advance(150 * time.Millisecond)
	eng.handleEvent(waitForEvent(t, eng))

	waitFor(t, func() bool { return ne.calls() == 1 })
	eng.handleEvent(waitForEvent(t, eng))

	assert.Equal(t, []string{"edited line 2"}, eng.display.current().Lines, "edit should replace ghost")
	assert.True(t, eng.display.current() != ghost, "display should be a new completion")
	assert.True(t, eng.state == stateHasCompletion, "state should stay HasCompletion")
}

func TestDualMode_NextEditEmptyKeepsGhost(t *testing.T) {
	buf := newMockBuffer()
	main := newMockProviderWithKind(CompletionFIM)
	ne := newMockProviderWithKind(CompletionEdit)
	ne.completionResp = &types.CompletionResponse{}

	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, main, clock)
	defer cancel()
	eng.nextEditProvider = ne
	eng.config.NextEditIdleDelay = 100 * time.Millisecond

	ghost := driveGhostShown(t, eng)

	clock.Advance(150 * time.Millisecond)
	eng.handleEvent(waitForEvent(t, eng))

	waitFor(t, func() bool { return ne.calls() == 1 })
	eng.handleEvent(waitForEvent(t, eng))

	assert.True(t, eng.display.current() == ghost, "ghost should be kept when next-edit has nothing")
}

func TestDualMode_TypingCancelsNextEdit(t *testing.T) {
	buf := newMockBuffer()
	main := newMockProviderWithKind(CompletionFIM)
	ne := newMockProviderWithKind(CompletionEdit)

	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, main, clock)
	defer cancel()
	eng.nextEditProvider = ne
	eng.config.NextEditIdleDelay = 100 * time.Millisecond

	driveGhostShown(t, eng)

	eng.handleEvent(Event{Type: EventTextChanged})
	assert.Nil(t, eng.nextEditTimer, "timer should be stopped by typing")

	clock.Advance(300 * time.Millisecond)
	// The FIM debounce timer may legitimately fire here; only next-edit must not.
	select {
	case ev := <-eng.eventChan:
		if ev.Type == EventTextChangeTimeout {
			break
		}
		t.Fatalf("unexpected event: %v", ev.Type)
	default:
	}
	assertNoEvent(t, eng)
	assert.Equal(t, 0, ne.calls(), "next-edit should never be asked")
}

func TestDualMode_StaleDisplayDropsResponse(t *testing.T) {
	buf := newMockBuffer()
	main := newMockProviderWithKind(CompletionFIM)
	ne := newMockProviderWithKind(CompletionEdit)
	ne.completionResp = nextEditEditResponse()

	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, main, clock)
	defer cancel()
	eng.nextEditProvider = ne
	eng.config.NextEditIdleDelay = 100 * time.Millisecond

	driveGhostShown(t, eng)

	clock.Advance(150 * time.Millisecond)
	eng.handleEvent(waitForEvent(t, eng))

	waitFor(t, func() bool { return ne.calls() == 1 })

	// The ghost gets replaced by something else before the response lands.
	fresh := &types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"fresh ghost"}}
	showDisplayedCompletionForTest(eng, fresh, []string{"line 1"}, nil)

	eng.handleEvent(waitForEvent(t, eng))

	assert.True(t, eng.display.current() == fresh, "stale next-edit response should be dropped")
}

func TestDualMode_IdleRequestsRouteToNextEditProvider(t *testing.T) {
	buf := newMockBuffer()
	main := newMockProviderWithKind(CompletionFIM)
	ne := newMockProviderWithKind(CompletionEdit)
	ne.completionResp = nextEditEditResponse()

	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, main, clock)
	defer cancel()
	eng.nextEditProvider = ne
	eng.config.NextEditIdleDelay = 100 * time.Millisecond

	eng.handleEvent(Event{Type: EventIdleTimeout})

	waitFor(t, func() bool { return ne.calls() == 1 })
	assert.Equal(t, 0, main.calls(), "main provider should not serve idle requests")
	assert.True(t, eng.state == statePendingCompletion, "state should be PendingCompletion")

	eng.handleEvent(waitForEvent(t, eng))
	assert.Equal(t, []string{"edited line 2"}, eng.display.current().Lines, "edit should be displayed")
}

func TestDualMode_AcceptCancelsInFlightNextEdit(t *testing.T) {
	buf := newMockBuffer()
	main := newMockProviderWithKind(CompletionFIM)
	ne := newMockProviderWithKind(CompletionEdit)
	ne.completionResp = nextEditEditResponse()

	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, main, clock)
	defer cancel()
	eng.nextEditProvider = ne
	eng.config.NextEditIdleDelay = 100 * time.Millisecond

	driveGhostShown(t, eng)

	clock.Advance(150 * time.Millisecond)
	eng.handleEvent(waitForEvent(t, eng))

	waitFor(t, func() bool { return ne.calls() == 1 })

	// User accepts the ghost while the next-edit request is in flight. The
	// accept triggers a buffer change (text_changed) which cancels the side
	// request; the late response must not swap the display.
	eng.handleEvent(Event{Type: EventTextChanged})
	eng.handleEvent(waitForEvent(t, eng))

	assert.True(t, eng.display.current() == nil || eng.display.current().Lines[0] != "edited line 2",
		"late next-edit response must not replace the display after accept")
}
