package engine

import (
	"context"
	"errors"
	"runtime/debug"

	"cursortab/logger"
	"cursortab/types"
)

// EventType represents the type of event in the engine
type EventType string

// Event type constants
const (
	EventEsc               EventType = "esc"
	EventTextChanged       EventType = "text_changed"
	EventTextChangeTimeout EventType = "text_change_timeout"
	EventTrigger           EventType = "trigger_completion"
	EventCursorMoved       EventType = "cursor_moved"
	EventInsertEnter       EventType = "insert_enter"
	EventInsertLeave       EventType = "insert_leave"
	EventAccept            EventType = "accept"
	EventPartialAccept     EventType = "partial_accept"
	EventFileSaved         EventType = "file_saved"
	EventIdleTimeout       EventType = "idle_timeout"
	EventCompletionReady   EventType = "completion_ready"
	EventCompletionError   EventType = "completion_error"
)

type Event struct {
	Type     EventType
	Payload  map[string]any
	Gen      uint64
	Response *types.CompletionResponse
	Stream   CompletionStream
	Err      error
}

// EventTypeFromString returns the EventType for a known event string, or "" if unknown.
func EventTypeFromString(s string) EventType {
	switch EventType(s) {
	case EventEsc, EventTextChanged, EventTextChangeTimeout, EventTrigger,
		EventCursorMoved, EventInsertEnter, EventInsertLeave, EventAccept,
		EventPartialAccept, EventFileSaved, EventIdleTimeout:
		return EventType(s)
	}
	return ""
}

// Transition represents a valid state transition in the engine's state machine
type Transition struct {
	From   state
	Event  EventType
	Action func(*Engine)
}

// transitions defines all valid state transitions in the engine.
//
// State Machine:
//
//	                  TextChangeTimeout / IdleTimeout
//	  +-------+              +----------+            +-----------+
//	  | Idle  |------------->| Pending  |----------->| Streaming |
//	  +-------+              +----------+            +-----------+
//	      ^                       |                       |
//	      |                       | CompletionReady       | StreamComplete
//	      |                       v                       |
//	      |                  +-----------+                |
//	      |                  | HasCompl. |<---------------+
//	      |                  +-----------+
//	      |                       | Tab
//	      |         +-------------+-------------+
//	      |         | no cursor target          | has cursor target
//	      v         v                           v
//	    (idle)  (idle)                   +--------------+
//	                                     | HasCursorTgt |
//	                                     +--------------+
//	                                          | Tab
//	                                          v
//	                                     HasCompl. or Pending
//
//	Rejection (any -> Idle): Esc, InsertLeave, TextChanged mismatch
//	CursorMoved: resets the pause timer and rejects (any state)
var transitions = []Transition{
	// From stateIdle
	{stateIdle, EventTextChangeTimeout, (*Engine).doRequestCompletion},
	{stateIdle, EventTrigger, (*Engine).doManualTrigger},
	{stateIdle, EventIdleTimeout, (*Engine).doIdleTimeout},
	{stateIdle, EventCursorMoved, (*Engine).doResetIdleTimer},
	{stateIdle, EventInsertEnter, (*Engine).stopIdleTimer},
	{stateIdle, EventInsertLeave, (*Engine).startIdleTimer},
	{stateIdle, EventEsc, (*Engine).stopIdleTimer},
	{stateIdle, EventFileSaved, (*Engine).doFileSaved},
	{stateIdle, EventTextChanged, (*Engine).startTextChangeTimer},

	// From statePendingCompletion
	{statePendingCompletion, EventTextChanged, (*Engine).doTextChangePending},
	{statePendingCompletion, EventEsc, (*Engine).doReject},
	{statePendingCompletion, EventInsertLeave, (*Engine).doRejectAndStartIdleTimer},
	{statePendingCompletion, EventFileSaved, (*Engine).doFileSaved},
	{statePendingCompletion, EventCursorMoved, (*Engine).doResetIdleTimer},

	// From stateHasCompletion
	{stateHasCompletion, EventAccept, (*Engine).acceptCompletion},
	{stateHasCompletion, EventPartialAccept, (*Engine).partialAcceptCompletion},
	{stateHasCompletion, EventEsc, (*Engine).doReject},
	{stateHasCompletion, EventTextChanged, (*Engine).handleTextChangeImpl},
	{stateHasCompletion, EventFileSaved, (*Engine).doFileSaved},
	{stateHasCompletion, EventInsertLeave, (*Engine).doRejectAndStartIdleTimer},
	{stateHasCompletion, EventCursorMoved, (*Engine).doResetIdleTimer},
	{stateHasCompletion, EventIdleTimeout, (*Engine).doIdleTimeout},

	// From stateHasCursorTarget
	{stateHasCursorTarget, EventAccept, (*Engine).acceptCursorTarget},
	{stateHasCursorTarget, EventEsc, (*Engine).doReject},
	{stateHasCursorTarget, EventTextChanged, (*Engine).doRejectAndDebounce},
	{stateHasCursorTarget, EventFileSaved, (*Engine).doFileSaved},
	{stateHasCursorTarget, EventInsertLeave, (*Engine).doRejectAndStartIdleTimer},
	{stateHasCursorTarget, EventCursorMoved, (*Engine).doResetIdleTimer},
	{stateHasCursorTarget, EventIdleTimeout, (*Engine).doIdleTimeout},

	// From stateStreamingCompletion
	{stateStreamingCompletion, EventAccept, (*Engine).doAcceptStreamingCompletion},
	{stateStreamingCompletion, EventEsc, (*Engine).doRejectStreaming},
	{stateStreamingCompletion, EventPartialAccept, (*Engine).doPartialAcceptStreaming},
	{stateStreamingCompletion, EventTextChanged, (*Engine).doRejectStreamingAndDebounce},
	{stateStreamingCompletion, EventFileSaved, (*Engine).doFileSaved},
	{stateStreamingCompletion, EventInsertLeave, (*Engine).doRejectStreamingAndStartIdleTimer},
	{stateStreamingCompletion, EventCursorMoved, (*Engine).doResetIdleTimer},
}

// transitionMap provides O(1) lookup for transitions by (state, event) pair
var transitionMap map[transitionKey]*Transition

type transitionKey struct {
	from  state
	event EventType
}

func init() {
	transitionMap = make(map[transitionKey]*Transition)
	for i := range transitions {
		t := &transitions[i]
		transitionMap[transitionKey{from: t.From, event: t.Event}] = t
	}
}

// findTransition looks up a valid transition for the given state and event.
func findTransition(from state, event EventType) *Transition {
	return transitionMap[transitionKey{from: from, event: event}]
}

func (e *Engine) eventLoop(ctx context.Context) {
	for {
		// Get current stream channels (nil when not streaming)
		e.mu.RLock()
		linesChan := e.streamLinesChan
		e.mu.RUnlock()

		select {
		case <-ctx.Done():
			return

		case line, ok := <-linesChan:
			e.withEventLock("stream", func() {
				if e.stopped || e.streamLinesChan != linesChan {
					return
				}
				if !ok {
					e.handleStreamCompleteSimple()
					return
				}
				e.handleStreamLine(line)
			})

		case event, ok := <-e.eventChan:
			if !ok {
				return
			}
			e.withEventLock(string(event.Type), func() {
				e.handleEvent(event)
			})
		}
	}
}

// withEventLock runs one loop iteration under the engine mutex. A panic in
// any branch is logged with its stack and the loop keeps running: only ctx
// cancellation ends it.
func (e *Engine) withEventLock(label string, fn func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			logger.Error("engine panic recovered in %s: %v\n%s", label, r, debug.Stack())
		}
	}()
	fn()
}

// RegisterEventHandler registers the event handler for nvim RPC callbacks.
func (e *Engine) RegisterEventHandler() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.stopped {
		return
	}

	if err := e.buffer.RegisterEventHandler(func(event string, payload map[string]any) {
		e.mu.RLock()
		stopped := e.stopped
		mainCtx := e.mainCtx
		e.mu.RUnlock()

		if stopped || mainCtx == nil {
			return
		}

		eventType := EventTypeFromString(event)
		if eventType == "" {
			return
		}
		select {
		case e.eventChan <- Event{Type: eventType, Payload: payload}:
		case <-mainCtx.Done():
		}
	}); err != nil {
		logger.Error("error registering event handler for new connection: %v", err)
	}
}

func (e *Engine) handleEvent(event Event) {
	if e.stopped {
		return
	}

	// Buffer tick and deletion classification come from the payload before any
	// handler syncs the mirror.
	if tick, ok := payloadInt(event.Payload["tick"]); ok {
		e.bufferTick = uint64(tick)
	}
	if event.Type == EventTextChanged {
		e.updateDeletionStreak(event.Payload)
	} else if event.Type == EventCursorMoved {
		// The last action was a movement, not a deletion.
		e.deletionStreak = 0
	}
	if row, col, ok := payloadPosition(event.Payload); ok {
		e.prevRow, e.prevCol, e.hasCursorPos = row, col, true
	}

	logger.Debug("handle event: %v (state=%s)", event.Type, e.state)
	defer func() {
		logger.Debug("after event: %v (state=%s)", event.Type, e.state)
	}()

	// Track insert/normal mode (always, regardless of state)
	switch event.Type {
	case EventInsertEnter:
		e.inInsertMode = true
	case EventInsertLeave:
		e.inInsertMode = false
	}

	// Background/async results
	if e.handleBackgroundEvent(event) {
		return
	}

	// Dispatch table for user/timer events
	e.dispatch(event)

	// In-flight work for the old display or buffer is invalid now. A full
	// accept during streaming keeps the request context alive so Finish can
	// still return the tail: the stream lifecycle releases it. Saving the
	// file does not invalidate a request: the buffer and its tick are
	// unchanged by a write to disk.
	switch event.Type {
	case EventTextChanged, EventEsc, EventInsertLeave, EventPartialAccept:
		e.cancelPending()
	case EventAccept:
		if e.streamingState == nil {
			e.cancelPending()
		}
	}

	// Cancel completions when entering a disabled mode (handles transitions
	// not in the table, e.g. InsertEnter from PendingCompletion)
	if !e.isModeEnabled() && e.state != stateIdle {
		e.cancelStreaming()
		e.reject()
	}
}

// dispatch finds and executes the appropriate transition for an event.
func (e *Engine) dispatch(event Event) bool {
	t := findTransition(e.state, event.Type)
	if t == nil {
		return false
	}
	if t.Action != nil {
		t.Action(e)
	}

	// InsertLeave always commits uncommitted user edits and drops the pending
	// debounce: automatic typing completions stop when leaving insert mode.
	if event.Type == EventInsertLeave {
		e.stopTextChangeTimer()
		e.syncBuffer()
		if e.buffer.CommitUserEdits() {
			e.saveCurrentFileState()
		}
	}

	return true
}

// handleBackgroundEvent routes async completion results. Every result is
// matched against the live request generation: a superseded result is dropped
// at Debug because a user event already owns the timers.
func (e *Engine) handleBackgroundEvent(event Event) bool {
	switch event.Type {
	case EventCompletionReady:
		if e.pending == nil || event.Gen != e.pending.gen {
			logger.Debug("dropped stale %s (gen %d, want live gen)", event.Type, event.Gen)
			return true
		}
		if event.Stream != nil {
			// Streaming keeps the request context alive until Finish: the
			// pending request stays live so user events can still cancel it.
			e.startCompletionStream(event.Stream, e.pending.manual)
			return true
		}
		pending := e.pending
		if pending.cancel != nil {
			pending.cancel()
		}
		e.pending = nil
		e.stopRequestTimers()
		if pending.role == RoleEdit {
			e.handleEditReady(event.Response, pending)
		} else {
			e.handleTypeReady(event.Response, pending)
		}
		return true

	case EventCompletionError:
		if e.pending == nil || event.Gen != e.pending.gen {
			logger.Debug("dropped stale %s (gen %d, want live gen)", event.Type, event.Gen)
			return true
		}
		pending := e.pending
		if pending.cancel != nil {
			pending.cancel()
		}
		e.pending = nil
		e.stopRequestTimers()
		if errors.Is(event.Err, context.Canceled) {
			return true
		}
		if pending.role == RoleEdit {
			// §6.3: an edit failure never disables the type path and does not
			// re-arm the edit timer until the next user event.
			logger.Info("next-edit request failed: %v", event.Err)
			return true
		}
		logger.Error("completion error: %v", event.Err)
		if e.state == statePendingCompletion {
			e.state = stateIdle
		}
		e.startIdleTimer()
		return true
	}
	return false
}

// Action functions for state transitions

func (e *Engine) doRequestCompletion() {
	e.requestCompletion(SourceTyping, false)
}

func (e *Engine) doManualTrigger() {
	e.requestCompletion(SourceTyping, true)
}

// doIdleTimeout runs the pause policy: dual mode consults the edit provider
// when nothing is displayed or the display is untouched, single mode only
// re-requests when idle so a shown ghost is never replaced in a loop.
func (e *Engine) doIdleTimeout() {
	if e.pending != nil {
		return
	}
	if e.config.NextEditProvider != nil {
		if e.display.completion != nil && e.display.bufferTick != e.bufferTick {
			return
		}
		e.requestCompletion(SourceIdle, false)
		return
	}
	if e.state != stateIdle {
		return
	}
	e.requestCompletion(SourceIdle, false)
}

func (e *Engine) doResetIdleTimer() {
	e.reject()
	e.resetIdleTimer()
}

func (e *Engine) doFileSaved() {
	e.syncBuffer()
	e.buffer.ClearDiffHistory()
	e.saveCurrentFileState()
}

func (e *Engine) doTextChangePending() {
	e.cancelPending()
	e.state = stateIdle
	e.armAfterTextChange()
}

func (e *Engine) doReject() {
	e.rejectAndRemember()
	e.stopIdleTimer()
	e.stopTextChangeTimer()
}

func (e *Engine) doRejectAndDebounce() {
	e.rejectAndRemember()
	e.armAfterTextChange()
}

func (e *Engine) doRejectAndStartIdleTimer() {
	e.reject()
	e.startIdleTimer()
}

func (e *Engine) doPartialAcceptStreaming() {
	if e.streamingState != nil && e.streamingState.FirstStageRendered {
		e.cancelStreamingKeepPartial()
		e.partialAcceptCompletion()
	}
}

// Streaming state action functions

func (e *Engine) doRejectStreaming() {
	e.cancelStreaming()
	e.rejectAndRemember()
	e.stopIdleTimer()
	e.stopTextChangeTimer()
}

func (e *Engine) cancelStreamAndCheckTyping(cancelFn func()) {
	cancelFn()
	e.syncBuffer()
	matches, hasRemaining := e.checkTypingMatchesPrediction()
	if matches && hasRemaining {
		e.state = stateHasCompletion
		return
	}
	if matches {
		e.reject()
		e.armAfterTextChange()
		return
	}
	e.rejectAndRemember()
	e.armAfterTextChange()
}

func (e *Engine) doRejectStreamingAndDebounce() {
	if e.streamingState != nil && e.display.completion != nil {
		e.cancelStreamAndCheckTyping(e.cancelStreamingKeepPartial)
		return
	}

	e.cancelStreaming()
	e.reject()
	e.armAfterTextChange()
}

func (e *Engine) doRejectStreamingAndStartIdleTimer() {
	e.cancelStreaming()
	e.reject()
	e.startIdleTimer()
}

func (e *Engine) doAcceptStreamingCompletion() {
	hasStreaming := e.streamingState != nil

	if e.display.completion != nil {
		// Mark that we accepted during streaming so handleStreamCompleteSimple
		// knows to compute cursor prediction from accumulated text
		if hasStreaming {
			e.acceptedDuringStreaming = true
		}
		e.state = stateHasCompletion
		e.acceptCompletion()
		return
	}
	if hasStreaming {
		// Keep streaming, will show result when complete
		e.acceptedDuringStreaming = true
		return
	}
	e.reject()
}
