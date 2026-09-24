package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"cursortab/assert"
	"cursortab/ctx"
	"cursortab/types"
)

func TestRequestCompletion_CancelsLeftoverStreamFromAcceptDuringStreaming(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"existing"}
	buf.row = 1
	buf.col = 0
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()
	eng.streamingState = &streamingState{}
	eng.completionStream = newMockCompletionStream(streamCancel)
	eng.acceptedDuringStreaming = true
	eng.state = stateIdle

	eng.requestCompletion(SourceTyping, true)

	assert.False(t, eng.acceptedDuringStreaming, "flag should be cleared")

	select {
	case <-streamCtx.Done():
	default:
		t.Errorf("stream context should be cancelled before new request starts")
	}
}

func TestRequestCompletion_ErrorEndsRequestAndRearmsPauseTimer(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"existing"}
	buf.row = 1
	buf.col = 0
	prov := newMockProvider()
	prov.completionErr = errors.New("provider failed")
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	eng.handleEvent(Event{Type: EventTextChangeTimeout})

	select {
	case event := <-eng.eventChan:
		assert.Equal(t, EventCompletionError, event.Type, "provider failure posts an error event")
		eng.handleEvent(event)
	case <-time.After(time.Second):
		t.Fatal("completion error event timed out")
	}

	assert.Equal(t, stateIdle, eng.state, "provider error finishes the pending request")
	assert.Nil(t, eng.pending, "no request left in flight")
	assert.NotNil(t, eng.idleTimer, "error end re-arms exactly one timer")
}

func TestEvalRequestCompletion_UsesBatchProviderPath(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"existing"}
	buf.row = 1
	buf.col = 0
	stream := newMockCompletionStream(nil)
	stream.err = errors.New("stream failed")
	close(stream.lines)
	prov := newMockStreamingProvider(stream)
	prov.completionResp = completionResponse(&types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"updated"},
	})
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	res, err := eng.EvalRequestCompletion(context.Background(), true)

	assert.NoError(t, err, "eval should use batch provider path")
	assert.True(t, res.Shown, "batch completion should be shown")
	assert.Equal(t, 1, prov.completionCalls, "eval should call Complete")
	assert.Equal(t, "updated", res.CompletionText, "completion text reported to the harness")
}

func TestRequestCompletion_CollectsOnlyRequiredMaterials(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"existing"}
	buf.row = 1
	buf.col = 0
	buf.diagnostics = &types.Diagnostics{FilePath: "test.go"}
	buf.treesitter = &types.TreesitterContext{EnclosingSignature: "func main()"}

	prov := newMockProvider()
	prov.materials = ctx.Materials{ctx.Diagnostics{}}
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	eng.requestCompletion(SourceTyping, true)

	select {
	case event := <-eng.eventChan:
		assert.Equal(t, EventCompletionReady, event.Type, "completion should be ready")
	case <-time.After(time.Second):
		t.Fatal("completion ready event timed out")
	}

	assert.Equal(t, 1, prov.completionCalls, "provider should be called")
	assert.Equal(t, 1, buf.diagnosticsCalls, "diagnostics should be collected")
	assert.Equal(t, 0, buf.treesitterCalls, "treesitter should not be collected")

	diagnostics, ok := ctx.Find[ctx.Diagnostics](prov.lastInput.Materials)
	assert.True(t, ok, "diagnostics material should be passed to provider")
	assert.Equal(t, buf.diagnostics, diagnostics.Data, "diagnostics data")
}

func TestBackgroundEvents_IgnoreStaleGeneration(t *testing.T) {
	for _, event := range []Event{
		{Type: EventCompletionError, Gen: 0, Err: errors.New("old request failed")},
		{Type: EventCompletionError, Gen: 1, Err: errors.New("old request failed")},
		{Type: EventCompletionReady, Gen: 1, Response: &types.CompletionResponse{Completion: &types.Completion{
			StartLine:  1,
			EndLineInc: 1,
			Lines:      []string{"stale"},
		}}},
	} {
		buf := newMockBuffer()
		buf.lines = []string{"existing"}
		prov := newMockProvider()
		clock := newMockClock()
		eng, cancel := createTestEngineWithContext(buf, prov, clock)

		cancelled := false
		eng.state = statePendingCompletion
		eng.pending = &pendingRequest{
			gen:    2,
			role:   RoleType,
			cancel: func() { cancelled = true },
		}

		eng.handleEvent(event)

		assert.Equal(t, statePendingCompletion, eng.state, "stale event should not finish the current request")
		assert.False(t, cancelled, "stale event should not cancel the current request")
		assert.NotNil(t, eng.pending, "stale event should keep the pending request")
		cancel()
	}
}
