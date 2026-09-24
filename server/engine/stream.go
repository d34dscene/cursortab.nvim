package engine

import (
	"cursortab/logger"
	"cursortab/text"
	"cursortab/types"
	"cursortab/utils"
)

func (e *Engine) startCompletionStream(stream CompletionStream, manual bool) {
	e.state = stateStreamingCompletion

	viewportTop, viewportBottom := e.buffer.ViewportBounds()
	windowStart, oldLines := stream.Window()

	if e.pending != nil {
		e.showGen = e.pending.gen
		e.showOrigin = e.pending.role
	}

	e.streamingState = &streamingState{
		Manual: manual,
		StageBuilder: text.NewIncrementalStageBuilder(
			oldLines,
			windowStart+1, // baseLineOffset (1-indexed)
			e.config.CursorPrediction.ProximityThreshold,
			e.config.MaxVisibleLines,
			viewportTop,
			viewportBottom,
			e.buffer.Row(),
			e.buffer.Col(),
			e.buffer.Path(),
			e.buffer.AvailableWidth(),
		),
	}
	e.completionStream = stream
	e.streamLinesChan = stream.Lines()
}

// cancelStreaming drops the live stream and releases its request. It may run
// while a different request is being set up, so callers supersede first.
func (e *Engine) cancelStreaming() {
	e.streamLinesChan = nil
	if e.completionStream != nil {
		e.completionStream.Cancel()
		e.completionStream = nil
	}
	e.cancelPending()
	e.streamingState = nil
	e.acceptedDuringStreaming = false
}

func (e *Engine) cancelStreamingKeepPartial() {
	e.streamLinesChan = nil
	if e.completionStream != nil {
		e.completionStream.Cancel()
		e.completionStream = nil
	}
	e.cancelPending()
	e.streamingState = nil
}

func (e *Engine) handleStreamLine(line string) {
	ss := e.streamingState
	if ss == nil {
		return
	}

	if e.acceptedDuringStreaming {
		return
	}

	if ss.HasPendingLine {
		finalized := ss.StageBuilder.AddLine(ss.PendingLine)
		if finalized != nil && !ss.FirstStageRendered {
			viewportTop, viewportBottom := e.buffer.ViewportBounds()
			needsNav := text.StageNeedsNavigation(
				finalized,
				e.buffer.Row(),
				viewportTop, viewportBottom,
				e.config.CursorPrediction.ProximityThreshold,
			)
			if !needsNav {
				if e.renderStreamedStage(finalized) {
					ss.FirstStageRendered = true
				}
			}
		}
	}

	ss.PendingLine = line
	ss.HasPendingLine = true
}

// releasePending ends the streaming request's lifecycle after Finish. The
// request context stays alive until then so the tail can still be read.
func (e *Engine) releasePending() {
	if e.pending == nil {
		return
	}
	if e.pending.cancel != nil {
		e.pending.cancel()
	}
	e.pending = nil
}

// armStreamEnd re-arms exactly one timer after a stream finishes: type-role
// streams arm the pause timer, edit-role streams follow §6.3 and wait for the
// next user event.
func (e *Engine) armStreamEnd(role Role) {
	if role == RoleType {
		e.startIdleTimer()
	}
}

func (e *Engine) handleStreamCompleteSimple() {
	e.streamLinesChan = nil

	if e.streamingState == nil {
		return
	}

	ss := e.streamingState
	role := RoleType
	if e.pending != nil {
		role = e.pending.role
	}

	if e.acceptedDuringStreaming {
		e.acceptedDuringStreaming = false
		if e.completionStream != nil {
			resp, err := e.completionStream.Finish()
			if err != nil {
				logger.Error("stream completion error after accept: %v", err)
			} else {
				e.handleStreamCompleteAfterAccept(resp, ss.Manual)
			}
			e.completionStream = nil
		}
		e.releasePending()
		e.streamingState = nil
		e.armStreamEnd(role)
		return
	}

	firstStageRendered := ss.FirstStageRendered

	if ss.HasPendingLine {
		ss.StageBuilder.AddLine(ss.PendingLine)
		ss.HasPendingLine = false
	}

	var streamResponse *types.CompletionResponse
	if e.completionStream != nil {
		if resp, err := e.completionStream.Finish(); err == nil {
			streamResponse = resp
		} else {
			logger.Error("stream completion error: %v", err)
			e.releasePending()
			e.streamingState = nil
			e.completionStream = nil
			e.reject()
			e.armStreamEnd(role)
			return
		}
	}
	e.releasePending()

	// The provider's Parse rejected the accumulated text (e.g. FIM suffix
	// overlap stripped to empty) although streaming already built stages from
	// the raw lines. Batch requests would have shown nothing, drop the
	// completion for parity.
	if streamResponse != nil &&
		streamResponse.Completion == nil && streamResponse.CursorTarget == nil {
		e.reject()
		e.armStreamEnd(role)
		return
	}

	stagingResult := ss.StageBuilder.Finalize()
	if streamResponse != nil && streamResponse.CursorTarget != nil && stagingResult != nil && len(stagingResult.Stages) > 0 {
		stagingResult.Stages[len(stagingResult.Stages)-1].CursorTarget = streamResponse.CursorTarget
	}

	e.streamingState = nil
	e.completionStream = nil

	if stagingResult == nil || len(stagingResult.Stages) == 0 {
		e.state = stateIdle
		e.armStreamEnd(role)
		return
	}

	e.stagedCompletion = &text.StagedCompletion{
		Stages:     stagingResult.Stages,
		CurrentIdx: 0,
		Manual:     ss.Manual,
	}

	// If the first stage matches a recent rejection, drop everything.
	// Important: if we already rendered a stage during streaming, ghost text
	// and display batch are live; a plain state flip would leave them visible
	// with no (idle, accept) transition, so Tab would do nothing. Route
	// through reject() so ClearUI and completion/display batch cleanup happen.
	firstStage := stagingResult.Stages[0]
	if e.suppressRejectedCompletionForStage(firstStage, ss.Manual) {
		e.reject()
		e.armStreamEnd(role)
		return
	}

	// If we already rendered a stage during streaming, keep it as-is.
	// Re-rendering would cause visible flicker since Finalize() diffs against
	// full old lines (vs partial during streaming), producing different
	// groups. The accept path reconciles any boundary mismatch (accept.go).
	if firstStageRendered {
		e.cursorTarget = firstStage.CursorTarget
		e.state = stateHasCompletion
		e.armStreamEnd(role)
		return
	}

	e.buffer.ClearUI()

	if stagingResult.FirstNeedsNavigation {
		e.showStageCursorTarget(stagingResult.Stages[0])
	} else {
		e.showCurrentStage()
	}
	e.armStreamEnd(role)
}

// handleStreamCompleteAfterAccept processes the stream tail that arrived
// after the user accepted mid-stream: a follow-up completion is shown when
// its first changed line is close to the cursor, otherwise only the jump is
// shown.
func (e *Engine) handleStreamCompleteAfterAccept(resp *types.CompletionResponse, manual bool) {
	if resp == nil {
		return
	}
	if resp.Completion == nil {
		if resp.CursorTarget != nil {
			e.cursorTarget = resp.CursorTarget
			e.handleCursorTarget()
		}
		return
	}

	e.syncBuffer()

	comp := resp.Completion
	bufferLines := e.buffer.Lines()

	var oldLines []string
	endLine := max(comp.EndLineInc, comp.StartLine+len(comp.Lines)-1)
	for i := comp.StartLine; i <= endLine && i-1 < len(bufferLines); i++ {
		oldLines = append(oldLines, bufferLines[i-1])
	}

	targetLine := text.FindFirstChangedLine(oldLines, comp.Lines, comp.StartLine-1)
	if targetLine <= 0 {
		if resp.CursorTarget != nil {
			e.cursorTarget = resp.CursorTarget
			e.handleCursorTarget()
		}
		return
	}

	distance := utils.Abs(targetLine - e.buffer.Row())
	if distance <= e.config.CursorPrediction.ProximityThreshold {
		if e.pending != nil {
			e.showGen = e.pending.gen
			e.showOrigin = e.pending.role
		}
		e.processCompletionWithManual(resp, manual)
		return
	}

	e.showCursorTargetWithCandidate(&types.CursorPredictionTarget{
		LineNumber:      int32(targetLine),
		ShouldRetrigger: false,
	}, e.rejectedCompletionFor(comp))
}

func (e *Engine) renderStreamedStage(stage *text.Stage) bool {
	ss := e.streamingState
	if ss == nil || stage == nil || len(stage.Groups) == 0 {
		return false
	}

	// Suppress before rendering so cached rejections do not flash during
	// streaming and then disappear when the full stream finalizes.
	if e.suppressRejectedCompletionForStage(stage, ss.Manual) {
		e.cancelStreaming()
		e.reject()
		return false
	}

	e.cursorTarget = stage.CursorTarget
	e.setDisplayedStage(stage, stage.Groups)

	return true
}
