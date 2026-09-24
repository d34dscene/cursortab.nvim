package engine

import (
	"context"

	"cursortab/ctx"
	"cursortab/types"
)

// prepareCompletionInputFor collects the materials one provider requires and
// freezes the request input before the provider goroutine starts.
func (e *Engine) prepareCompletionInputFor(p Provider, parent context.Context) ctx.CompletionInput {
	requirements := p.RequiredMaterials()
	sourceInput := e.buildContextSourceInput(requirements, p.MaterialsBudgetChars())
	collected, contextChars := ctx.Collect(parent, sourceInput, requirements)
	return ctx.CompletionInput{Current: sourceInput.Current, Materials: collected, ContextChars: contextChars}
}

func (e *Engine) startProviderCompletionFor(p Provider, reqCtx context.Context, input ctx.CompletionInput) (*types.CompletionResponse, CompletionStream, error) {
	if streamingProvider, ok := p.(StreamingProvider); ok {
		stream, err := streamingProvider.StreamCompletion(reqCtx, input)
		if err != nil {
			return nil, nil, err
		}
		if stream != nil {
			return nil, stream, nil
		}
	}

	result, err := p.Complete(reqCtx, input)
	if err != nil {
		return nil, nil, err
	}
	return result, nil, nil
}

// requestCompletion starts one request. Typing and manual requests run
// against the type provider and take over the completion state: idle requests
// go to the edit provider in dual mode (and to the type provider in single
// mode) as a background consult that never disturbs the live display until a
// response lands. Every path ends by starting no request at all (gates), or
// hands the lifecycle to the response handlers which re-arm exactly one
// timer or render.
func (e *Engine) requestCompletion(source Source, manual bool) {
	if e.stopped {
		return
	}

	// A new request supersedes any in-flight request (any role) and any
	// leftover stream from a prior accept-during-streaming.
	e.cancelPending()
	e.cancelStreaming()
	e.syncBuffer()

	role := RoleType
	activeProvider := e.provider
	if source == SourceIdle && e.config.NextEditProvider != nil {
		role = RoleEdit
		activeProvider = e.config.NextEditProvider
	}

	if reason, detail := e.suppressRequest(source, manual); reason != SuppressNone {
		logSuppressed(reason, detail)
		return
	}

	e.stopRequestTimers()
	e.gen++
	gen := e.gen

	input := e.prepareCompletionInputFor(activeProvider, e.mainCtx)

	if role == RoleType {
		e.state = statePendingCompletion
	}

	timeout := e.config.CompletionTimeout
	if role == RoleEdit {
		timeout = e.config.NextEditTimeout
	}
	reqCtx, cancel := context.WithTimeout(e.mainCtx, timeout)
	e.pending = &pendingRequest{
		gen:    gen,
		source: source,
		role:   role,
		manual: manual,
		tick:   e.bufferTick,
		cancel: cancel,
	}
	mainCtx := e.mainCtx

	go func() {
		result, stream, err := e.startProviderCompletionFor(activeProvider, reqCtx, input)
		if err != nil {
			cancel()
			select {
			case e.eventChan <- Event{Type: EventCompletionError, Gen: gen, Err: err}:
			case <-mainCtx.Done():
			}
			return
		}
		if stream != nil {
			select {
			case e.eventChan <- Event{Type: EventCompletionReady, Gen: gen, Stream: stream}:
			case <-mainCtx.Done():
				cancel()
			}
			return
		}
		cancel()
		select {
		case e.eventChan <- Event{Type: EventCompletionReady, Gen: gen, Response: result}:
		case <-mainCtx.Done():
		}
	}()
}
