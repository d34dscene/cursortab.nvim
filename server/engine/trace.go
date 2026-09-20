package engine

import (
	"cursortab/session"
	"cursortab/text"
	"cursortab/types"
)

// trace writes one session event. Nil tracers drop it, so tracing is a no-op
// unless the daemon opened a recorder.
func (e *Engine) trace(event session.Event) {
	e.tracer.Record(event)
}

func (e *Engine) traceRequest(source types.CompletionSource, requestID uint64) {
	e.requestStartedAt = e.clock.Now()
	e.lastConfidence = nil
	e.trace(session.Event{
		Type:      session.EventRequest,
		Source:    completionSourceLabel(source),
		Path:      e.buffer.Path(),
		RequestID: requestID,
	})
}

func (e *Engine) traceSuppressed(source types.CompletionSource, reason string) {
	e.trace(session.Event{
		Type:   session.EventSuppressed,
		Source: completionSourceLabel(source),
		Path:   e.buffer.Path(),
		Reason: reason,
	})
}

func (e *Engine) traceShown(stage *text.Stage) {
	event := session.Event{
		Type:       session.EventShown,
		Source:     completionSourceLabel(e.lastCompletionSource),
		Path:       e.buffer.Path(),
		Confidence: e.lastConfidence,
	}
	if !e.requestStartedAt.IsZero() {
		event.LatencyMs = e.clock.Now().Sub(e.requestStartedAt).Milliseconds()
	}
	if stage != nil {
		event.StartLine = stage.BufferStart
		event.EndLine = stage.BufferEnd
		event.Lines = len(stage.Lines)
	}
	e.trace(event)
}

func (e *Engine) traceAccepted() {
	e.trace(session.Event{
		Type:       session.EventAccepted,
		Source:     completionSourceLabel(e.lastCompletionSource),
		Path:       e.buffer.Path(),
		Confidence: e.lastConfidence,
	})
}

func (e *Engine) traceRejected() {
	e.trace(session.Event{
		Type:       session.EventRejected,
		Source:     completionSourceLabel(e.lastCompletionSource),
		Path:       e.buffer.Path(),
		Confidence: e.lastConfidence,
	})
}

func completionSourceLabel(source types.CompletionSource) string {
	if source == types.CompletionSourceIdle {
		return "idle"
	}
	return "typing"
}
