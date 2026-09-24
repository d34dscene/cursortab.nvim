package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cursortab/logger"
)

// EvalRequestResult reports the outcome of a synchronous eval request.
type EvalRequestResult struct {
	// Shown is true when gating passed, a non-empty completion came back,
	// and staging produced at least one stage.
	Shown bool
	// Suppressed is true when a gating layer rejected the request before
	// it reached the provider.
	Suppressed bool
	// SuppressReason identifies which layer fired: one of the
	// SuppressReason values (e.g. "no-edits", "rejection-cache").
	SuppressReason string
	// ProviderLatency is the wall-clock duration of the provider call.
	// Under replay this reflects the recorded duration (when a latency-aware
	// transport is used) or zero if bypassed.
	ProviderLatency time.Duration
	// CompletionText is the provider's proposed completion as joined lines,
	// empty when nothing was proposed or the request was suppressed.
	CompletionText string
	// StagedLines is the buffer contents with all stages applied. Useful
	// for quality scoring when the scenario doesn't accept the completion.
	StagedLines []string
}

// EvalRequestCompletion runs gating, provider request, and staging
// synchronously. It is the single entry point used by the eval harness: the
// production requestCompletion spawns a goroutine and routes through the
// event loop, which is not friendly to deterministic evaluation.
//
// A non-manual request carries SourceIdle semantics (single mode routes it to
// the type provider), so it hits the same gates a pause-time request would,
// including no-edits. The manualTrigger parameter bypasses every gate,
// matching production manual trigger semantics.
func (e *Engine) EvalRequestCompletion(ctx context.Context, manualTrigger bool) (*EvalRequestResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.stopped {
		return nil, fmt.Errorf("engine stopped")
	}

	result := &EvalRequestResult{}
	e.syncBuffer()

	source := SourceIdle
	if manualTrigger {
		source = SourceTyping
	}
	if reason, detail := e.suppressRequest(source, manualTrigger); reason != SuppressNone {
		logSuppressed(reason, detail)
		result.Suppressed = true
		result.SuppressReason = string(reason)
		return result, nil
	}

	role := RoleType
	activeProvider := e.provider
	if source == SourceIdle && e.config.NextEditProvider != nil {
		role = RoleEdit
		activeProvider = e.config.NextEditProvider
	}
	logger.Debug("eval request: source=%d role=%d manual=%v", source, role, manualTrigger)

	input := e.prepareCompletionInputFor(activeProvider, ctx)

	start := time.Now()
	resp, err := activeProvider.Complete(ctx, input)
	result.ProviderLatency = time.Since(start)
	if err != nil {
		return result, fmt.Errorf("provider: %w", err)
	}

	if resp == nil || resp.Completion == nil {
		if resp != nil && resp.CursorTarget != nil {
			e.cursorTarget = resp.CursorTarget
		}
		return result, nil
	}
	result.CompletionText = strings.Join(resp.Completion.Lines, "\n")

	shown := e.processCompletionWithManual(resp, manualTrigger) == completionShown
	result.Shown = shown
	stageCount := 0
	if e.stagedCompletion != nil {
		stageCount = len(e.stagedCompletion.Stages)
		result.StagedLines = e.applyAllStagesToBufferCopy()
	}
	logger.Debug("eval request done: shown=%v stages=%d latency=%s",
		shown, stageCount, result.ProviderLatency)
	return result, nil
}

// EvalAccept accepts the current staged completion synchronously. Unlike the
// interactive event loop, eval never speculatively retriggers provider
// requests from an accept step: scenarios request the next completion
// explicitly. That keeps cassette replay deterministic and ensures per-step
// latency only reflects the requests the scenario asked for.
func (e *Engine) EvalAccept() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stagedCompletion == nil {
		return
	}
	if e.cursorTarget != nil {
		e.cursorTarget.ShouldRetrigger = false
	}
	for _, stage := range e.stagedCompletion.Stages {
		if stage != nil && stage.CursorTarget != nil {
			stage.CursorTarget.ShouldRetrigger = false
		}
	}
	e.acceptCompletion()
}

// EvalReject rejects the current completion synchronously.
func (e *Engine) EvalReject() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rejectAndRemember()
}

// applyAllStagesToBufferCopy returns the buffer with all staged completion
// stages applied (copied so engine state isn't mutated).
func (e *Engine) applyAllStagesToBufferCopy() []string {
	if e.stagedCompletion == nil {
		return e.buffer.Lines()
	}
	return applyAllStages(e.buffer.Lines(), e.stagedCompletion.Stages)
}
