package engine

import (
	"context"
	"os"
	"sync"
	"time"

	"cursortab/ctx"
	"cursortab/logger"
	"cursortab/text"
	"cursortab/types"
)

// Timer represents a timer that can be stopped.
type Timer interface {
	Stop() bool
}

// Clock provides time-related operations for dependency injection.
type Clock interface {
	AfterFunc(d time.Duration, f func()) Timer
	Now() time.Time
}

// SystemClock is the default Clock implementation using the standard library.
var SystemClock Clock = systemClock{}

type systemClock struct{}

func (systemClock) AfterFunc(d time.Duration, f func()) Timer {
	return time.AfterFunc(d, f)
}

func (systemClock) Now() time.Time {
	return time.Now()
}

// pendingRequest is the single in-flight request, whichever role it targets.
type pendingRequest struct {
	gen    uint64
	source Source
	role   Role
	manual bool
	tick   uint64
	cancel context.CancelFunc
}

type Engine struct {
	WorkspacePath string

	provider  Provider
	buffer    Buffer
	clock     Clock
	state     state
	eventChan chan Event
	mu        sync.RWMutex

	// Main context and cancel for the engine lifecycle. Shutdown happens only
	// through ctx cancellation.
	mainCtx    context.Context
	mainCancel context.CancelFunc
	stopped    bool
	stopOnce   sync.Once

	idleTimer       Timer
	textChangeTimer Timer

	// gen is the monotonic request counter. Display and responses are keyed
	// against it so a superseded request can never touch a newer display.
	gen        uint64
	pending    *pendingRequest
	bufferTick uint64
	showGen    uint64
	showOrigin Role

	// Deletion streak for the single-deletion gate, classified from text
	// changed payload deltas. prevRow/prevCol is the last cursor position
	// seen in any event payload.
	deletionStreak int
	prevRow        int
	prevCol        int
	hasCursorPos   bool

	display          displayedCompletion
	cursorTarget     *types.CursorPredictionTarget
	stagedCompletion *text.StagedCompletion

	// Streaming state (line-by-line)
	streamingState          *streamingState
	completionStream        CompletionStream
	streamLinesChan         <-chan string
	acceptedDuringStreaming bool

	inInsertMode bool
	config       EngineConfig
	retriever    ctx.Retriever

	// Per-file state that persists across file switches.
	fileStateStore map[string]*FileState

	// Rejection cache: file path -> normalized content hash -> expiry.
	rejectedCompletions map[string]map[uint64]time.Time
}

// NewEngine creates a new Engine instance.
func NewEngine(provider Provider, buf Buffer, config EngineConfig, clock Clock) (*Engine, error) {
	workspacePath, err := os.Getwd()
	if err != nil {
		logger.Warn("error getting current directory, using home: %v", err)
		workspacePath = "~"
	}

	return &Engine{
		WorkspacePath:       workspacePath,
		provider:            provider,
		buffer:              buf,
		clock:               clock,
		state:               stateIdle,
		eventChan:           make(chan Event, 100),
		config:              config,
		fileStateStore:      make(map[string]*FileState),
		rejectedCompletions: make(map[string]map[uint64]time.Time),
		retriever:           config.Retriever,
	}, nil
}

// Start begins the engine event loop.
func (e *Engine) Start(ctx context.Context) {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return
	}
	e.mainCtx, e.mainCancel = context.WithCancel(ctx)
	e.mu.Unlock()

	go e.eventLoop(e.mainCtx)
	logger.Info("engine started")
}

// Stop shuts down the engine. Cancellation of the lifecycle context is the
// only shutdown signal, nothing inside the loop stops the engine itself.
func (e *Engine) Stop() {
	e.stopOnce.Do(func() {
		e.mu.Lock()
		defer e.mu.Unlock()

		e.stopped = true
		e.cancelPending()
		e.cancelStreaming()
		e.stopIdleTimer()
		e.stopTextChangeTimer()
		e.state = stateIdle
		e.cursorTarget = nil
		e.stagedCompletion = nil
		e.display = displayedCompletion{}
		if e.mainCancel != nil {
			e.mainCancel()
		}
		logger.Info("engine stopped")
	})
}

// resetCompletionFields clears per-completion display state. It does not
// cancel requests or drop the staged completion.
func (e *Engine) resetCompletionFields() {
	e.display = displayedCompletion{}
}

// cancelPending cancels the in-flight request, whichever role it targets.
// Every user event that invalidates in-flight work goes through here.
func (e *Engine) cancelPending() {
	if e.pending == nil {
		return
	}
	if e.pending.cancel != nil {
		e.pending.cancel()
	}
	e.pending = nil
}

// stopRequestTimers releases both timers while a request is in flight. Exactly
// one timer is re-armed when the request ends.
func (e *Engine) stopRequestTimers() {
	e.stopIdleTimer()
	e.stopTextChangeTimer()
}

func (e *Engine) startIdleTimer() {
	e.stopIdleTimer()
	if e.config.IdleCompletionDelay < 0 || !e.isModeEnabled() {
		return
	}
	e.idleTimer = e.clock.AfterFunc(e.config.IdleCompletionDelay, func() {
		e.mu.RLock()
		stopped := e.stopped
		mainCtx := e.mainCtx
		e.mu.RUnlock()
		if stopped || mainCtx == nil {
			return
		}
		select {
		case e.eventChan <- Event{Type: EventIdleTimeout}:
		case <-mainCtx.Done():
		}
	})
}

func (e *Engine) stopIdleTimer() {
	if e.idleTimer != nil {
		e.idleTimer.Stop()
		e.idleTimer = nil
	}
}

func (e *Engine) resetIdleTimer() {
	e.startIdleTimer()
}

func (e *Engine) startTextChangeTimer() {
	e.stopIdleTimer()
	e.stopTextChangeTimer()
	if e.config.TextChangeDebounce < 0 || !e.isModeEnabled() {
		return
	}
	e.textChangeTimer = e.clock.AfterFunc(e.config.TextChangeDebounce, func() {
		e.mu.RLock()
		stopped := e.stopped
		mainCtx := e.mainCtx
		e.mu.RUnlock()
		if stopped || mainCtx == nil {
			return
		}
		select {
		case e.eventChan <- Event{Type: EventTextChangeTimeout}:
		case <-mainCtx.Done():
		}
	})
}

func (e *Engine) stopTextChangeTimer() {
	if e.textChangeTimer != nil {
		e.textChangeTimer.Stop()
		e.textChangeTimer = nil
	}
}

// armAfterTextChange arms the debounce when automatic typing completions are
// enabled, otherwise falls back to the pause timer so idle retrigger and the
// edit consult survive a disabled debounce.
func (e *Engine) armAfterTextChange() {
	if e.config.TextChangeDebounce >= 0 {
		e.startTextChangeTimer()
		return
	}
	e.startIdleTimer()
}

// isModeEnabled reports whether automatic completions may run in the current
// mode. Manual requests bypass this through suppressRequest.
func (e *Engine) isModeEnabled() bool {
	if e.inInsertMode {
		return e.config.CompleteInInsert
	}
	return e.config.CompleteInNormal
}
