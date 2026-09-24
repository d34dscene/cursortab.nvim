package engine

import (
	"context"
	"errors"
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

func (p *mockProvider) resetCalls() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.completionCalls = 0
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
// completion is displayed, leaving the pause timer armed.
func driveGhostShown(t *testing.T, eng *Engine) *types.Completion {
	t.Helper()
	eng.handleEvent(Event{Type: EventTextChangeTimeout})
	eng.handleEvent(waitForEvent(t, eng))

	assert.Equal(t, stateHasCompletion, eng.state, "state should be HasCompletion")
	assert.NotNil(t, eng.display.completion, "ghost should be displayed")
	assert.NotNil(t, eng.idleTimer, "pause timer should be armed after the type response")
	return eng.display.completion
}

// dualEngine builds an engine in dual mode with a fast pause timer.
func dualEngine(t *testing.T) (*Engine, *mockBuffer, *mockProvider, *mockProvider, *mockClock, context.CancelFunc) {
	t.Helper()
	buf := newMockBuffer()
	buf.col = len(buf.lines[0])
	main := newMockProviderWithKind(CompletionFIM)
	ne := newMockProviderWithKind(CompletionEdit)
	ne.completionResp = nextEditEditResponse()
	ne.materials = sourcectx.Materials{}

	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, main, clock)
	eng.config.NextEditProvider = ne
	eng.config.IdleCompletionDelay = 100 * time.Millisecond
	return eng, buf, main, ne, clock, cancel
}

func TestDualMode_PauseConsultsEditProviderAndUpgrades(t *testing.T) {
	eng, _, _, ne, clock, cancel := dualEngine(t)
	defer cancel()

	ghost := driveGhostShown(t, eng)
	assert.Equal(t, 0, ne.calls(), "edit provider should not be asked yet")

	clock.Advance(150 * time.Millisecond)
	eng.handleEvent(waitForEvent(t, eng))

	waitFor(t, func() bool { return ne.calls() == 1 })
	eng.handleEvent(waitForEvent(t, eng))

	assert.Equal(t, []string{"edited line 2"}, eng.display.completion.Lines, "edit should replace ghost")
	assert.True(t, eng.display.completion != ghost, "display should be a new completion")
	assert.Equal(t, RoleEdit, eng.display.origin, "upgraded display is edit-origin")
	assert.Equal(t, stateHasCompletion, eng.state, "state should stay HasCompletion")
	// §6.3: a completed edit request does not re-arm the edit timer.
	assert.Nil(t, eng.idleTimer, "edit response must not re-arm the pause timer")
}

func TestDualMode_QuietEditKeepsGhost(t *testing.T) {
	eng, _, _, ne, clock, cancel := dualEngine(t)
	defer cancel()
	ne.completionResp = &types.CompletionResponse{}

	ghost := driveGhostShown(t, eng)

	clock.Advance(150 * time.Millisecond)
	eng.handleEvent(waitForEvent(t, eng))

	waitFor(t, func() bool { return ne.calls() == 1 })
	eng.handleEvent(waitForEvent(t, eng))

	assert.True(t, eng.display.completion == ghost, "quiet edit keeps the type ghost")
	assert.Equal(t, RoleType, eng.display.origin, "origin stays type")
	assert.Nil(t, eng.idleTimer, "quiet must not re-arm the pause timer until the next user event")
}

func TestDualMode_EditErrorLeavesDisplayUntouched(t *testing.T) {
	eng, _, _, ne, clock, cancel := dualEngine(t)
	defer cancel()
	ne.completionErr = errors.New("edit server down")

	driveGhostShown(t, eng)

	clock.Advance(150 * time.Millisecond)
	eng.handleEvent(waitForEvent(t, eng))

	waitFor(t, func() bool { return ne.calls() == 1 })
	eng.handleEvent(waitForEvent(t, eng))

	assert.Equal(t, stateHasCompletion, eng.state, "error keeps the display state")
	assert.NotNil(t, eng.display.completion, "error keeps the displayed ghost")
	assert.Nil(t, eng.idleTimer, "error does not re-arm the edit timer")
	assert.Nil(t, eng.pending, "request is finished")
}

func TestDualMode_TypingCancelsInFlightEdit(t *testing.T) {
	eng, _, main, ne, clock, cancel := dualEngine(t)
	defer cancel()

	driveGhostShown(t, eng)

	clock.Advance(150 * time.Millisecond)
	eng.handleEvent(waitForEvent(t, eng))
	waitFor(t, func() bool { return ne.calls() == 1 })

	eng.handleEvent(Event{Type: EventTextChanged})
	assert.Nil(t, eng.pending, "text change cancels the in-flight edit request")

	// The late edit response must be dropped as stale.
	eng.handleEvent(waitForEvent(t, eng))
	assert.Equal(t, 1, main.calls(), "type provider saw no new call from the dropped response")
	assert.Equal(t, 1, ne.calls(), "edit provider saw no second call")

	// And the pause timer must not fire an edit consult while typing.
	assert.Nil(t, eng.idleTimer, "typing stops the pause timer")
}

func TestDualMode_StaleTickDropsEditResponse(t *testing.T) {
	eng, _, _, _, _, cancel := dualEngine(t)
	defer cancel()

	ghost := &types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"fresh ghost"}}
	showDisplayedCompletionForTest(eng, ghost, []string{"line 1"}, nil)

	eng.pending = &pendingRequest{gen: 9, role: RoleEdit, tick: 5}
	eng.bufferTick = 6 // buffer moved after the request started

	eng.handleEditReady(nextEditEditResponse(), eng.pending)

	assert.True(t, eng.display.completion == ghost, "stale edit response must not replace the display")
}

func TestDualMode_IdleRequestsRouteToEditProvider(t *testing.T) {
	eng, _, main, ne, _, cancel := dualEngine(t)
	defer cancel()

	eng.handleEvent(Event{Type: EventIdleTimeout})

	waitFor(t, func() bool { return ne.calls() == 1 })
	assert.Equal(t, 0, main.calls(), "type provider should not serve idle requests in dual mode")
	assert.Equal(t, stateIdle, eng.state, "edit consult is background and does not take over state")
	assert.NotNil(t, eng.pending, "edit request is in flight")

	eng.handleEvent(waitForEvent(t, eng))
	assert.Equal(t, []string{"edited line 2"}, eng.display.completion.Lines, "edit should be displayed")
}

func TestDualMode_IdleSkippedWhenDisplayTouched(t *testing.T) {
	eng, _, main, ne, _, cancel := dualEngine(t)
	defer cancel()

	ghost := driveGhostShown(t, eng)
	// Simulate the user typing after the ghost was shown.
	eng.bufferTick = eng.display.bufferTick + 1
	main.resetCalls()
	ne.resetCalls()

	eng.handleEvent(Event{Type: EventIdleTimeout})

	assert.Equal(t, 0, ne.calls(), "touched display blocks the pause consult")
	assert.Equal(t, 0, main.calls(), "no request issued")
	assert.True(t, eng.display.completion == ghost, "display untouched")
}

func TestSingleMode_IdleRequestsRouteToTypeProvider(t *testing.T) {
	buf := newMockBuffer()
	buf.col = len(buf.lines[0])
	prov := newMockProviderWithKind(CompletionFIM)
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	eng.handleEvent(Event{Type: EventIdleTimeout})

	waitFor(t, func() bool { return prov.calls() == 1 })
	assert.Equal(t, statePendingCompletion, eng.state, "type request takes over completion state")

	eng.handleEvent(waitForEvent(t, eng))
	assert.NotNil(t, eng.display.completion, "idle completion displayed in single mode")
}

func TestSingleMode_IdleIgnoredWhileDisplayShown(t *testing.T) {
	buf := newMockBuffer()
	buf.col = len(buf.lines[0])
	prov := newMockProviderWithKind(CompletionFIM)
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	driveGhostShown(t, eng)
	prov.mu.Lock()
	prov.completionCalls = 0
	prov.mu.Unlock()

	eng.handleEvent(Event{Type: EventIdleTimeout})

	assert.Equal(t, 0, prov.calls(), "single mode never replaces a shown ghost from idle")
}

func TestTypeError_RearmsPauseTimer(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	prov.completionErr = errors.New("provider failed")
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	eng.handleEvent(Event{Type: EventTextChangeTimeout})
	eng.handleEvent(waitForEvent(t, eng))

	assert.Equal(t, stateIdle, eng.state, "provider error returns to idle")
	assert.NotNil(t, eng.idleTimer, "error end re-arms exactly one timer")
}
