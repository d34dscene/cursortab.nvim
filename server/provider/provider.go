// Package provider builds dialect-driven completion providers. The family
// table resolves a model id to one of six dialects; each dialect owns its
// prompt bytes, response parsing, and stream behavior, and the transport is
// the shared OpenAI completions client.
package provider

import (
	"context"
	"fmt"
	"strings"

	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/engine"
	"cursortab/types"
	"cursortab/utils"
)

// Role selects which completion slot a provider fills.
type Role int

const (
	RoleType Role = iota
	RoleEdit
)

func (r Role) String() string {
	if r == RoleEdit {
		return "edit"
	}
	return "type"
}

// Generation budgets applied when Endpoint.MaxTokens is 0.
const (
	defaultTypeMaxTokens = 64
	defaultEditMaxTokens = 256
)

// providerBase carries the facts engine reads plus the dialect dispatch for
// one provider instance.
type providerBase struct {
	OpenAI
	d         *dialect
	kind      engine.CompletionKind
	materials sourcectx.Materials
}

// streamingProvider adds line streaming. Batch-only dialects (edit-zeta21)
// stay a bare *providerBase so engine never sees a fake streaming seam.
type streamingProvider struct {
	providerBase
}

var (
	_ engine.Provider          = (*providerBase)(nil)
	_ engine.StreamingProvider = (*streamingProvider)(nil)
)

// Build constructs a provider for role. An empty dialect resolves from
// cfg.Endpoint.Model through the family table; a pinned dialect bypasses the
// role check so exotic setups stay possible. cfg is copied: role defaults
// never leak back to the caller.
func Build(role Role, dialectName string, cfg *types.ProviderConfig) (engine.Provider, error) {
	explicit := dialectName != ""
	d := dialectByName[dialectName]
	if d == nil {
		if explicit {
			return nil, fmt.Errorf("provider: unknown dialect %q (known: %s)",
				dialectName, strings.Join(Dialects(), ", "))
		}
		d = dialectForModel(cfg.Endpoint.Model)
	}
	if !explicit && d.role != role {
		return nil, fmt.Errorf(
			"provider: model %q resolves to dialect %s for role %s, not %s; pin an explicit dialect to override",
			cfg.Endpoint.Model, d.name, d.role, role)
	}

	resolved := *cfg
	if resolved.FIMTokens == nil && d.tokens != nil {
		resolved.FIMTokens = d.tokens()
	}
	if resolved.Endpoint.MaxTokens == 0 {
		if role == RoleType {
			resolved.Endpoint.MaxTokens = defaultTypeMaxTokens
		} else {
			resolved.Endpoint.MaxTokens = defaultEditMaxTokens
		}
	}

	base := providerBase{
		OpenAI:    NewOpenAI(d.name, &resolved),
		d:         d,
		kind:      engine.CompletionFIM,
		materials: d.materials(&resolved),
	}
	if role == RoleEdit {
		base.kind = engine.CompletionEdit
	}
	if d.stream != nil {
		return &streamingProvider{providerBase: base}, nil
	}
	return &base, nil
}

func (p *providerBase) CompletionKind() engine.CompletionKind {
	return p.kind
}

func (p *providerBase) RequiredMaterials() sourcectx.Materials {
	return p.materials
}

// MaterialsBudgetChars reports the byte budget cross-file materials may add
// to the prompt.
func (p *providerBase) MaterialsBudgetChars() int {
	return utils.EstimateCharsFromTokens(materialsTokens(p.config))
}

func (p *providerBase) Complete(ctx context.Context, input sourcectx.CompletionInput) (*types.CompletionResponse, error) {
	state := prepareRequestState(input, p.config)
	req, err := p.build(state)
	if err != nil {
		return nil, err
	}
	result, err := p.Call(ctx, req)
	if err != nil {
		return nil, err
	}
	response, err := p.parse(state, result)
	if err != nil {
		return nil, err
	}
	attachConfidence(response, result)
	return response, nil
}

func (p *streamingProvider) StreamCompletion(ctx context.Context, input sourcectx.CompletionInput) (engine.CompletionStream, error) {
	state := prepareRequestState(input, p.config)
	req, err := p.build(state)
	if err != nil {
		return nil, err
	}
	return p.startStream(ctx, state, req, p.d.stream(state), p.parse)
}

func (p *providerBase) build(state *RequestState) (*openai.CompletionRequest, error) {
	return p.d.build(p, state)
}

func (p *providerBase) parse(state *RequestState, result *openai.CompletionResult) (*types.CompletionResponse, error) {
	return p.d.parse(p, state, result)
}

// RequestWindow is the source window shared by build, parse, and streaming.
type RequestWindow struct {
	Lines      []string
	Start      int
	CursorLine int
	MaxLines   int
}

// RequestState is the shared fact source for one provider call.
type RequestState struct {
	Input  sourcectx.CompletionInput
	Window RequestWindow
}

// prepareRequestState derives the source frame shared by build, parse, and
// stream windowing.
func prepareRequestState(input sourcectx.CompletionInput, config *types.ProviderConfig) *RequestState {
	current := input.Current
	state := &RequestState{Input: input}
	cursorLine := current.Cursor.Row - 1
	var syntaxRanges []*types.LineRange
	if material, ok := sourcectx.Find[sourcectx.Treesitter](input.Materials); ok && material.Data != nil {
		syntaxRanges = material.Data.SyntaxRanges
	}
	windowBudget := 0
	if config != nil {
		windowBudget = windowTokens(config, input.ContextChars)
	}
	trimmedLines, newCursorLine, _, trimOffset, didTrim := utils.TrimContentAroundCursor(
		current.File.Lines,
		cursorLine,
		current.Cursor.Col,
		windowBudget,
		syntaxRanges,
	)
	state.Window.Lines = trimmedLines
	state.Window.CursorLine = newCursorLine
	state.Window.Start = trimOffset

	if didTrim {
		state.Window.MaxLines = len(trimmedLines)
	}
	if current.ViewportHeight > 0 {
		if state.Window.MaxLines == 0 || current.ViewportHeight < state.Window.MaxLines {
			state.Window.MaxLines = current.ViewportHeight
		}
	}

	return state
}

func emptyResponse() *types.CompletionResponse {
	return &types.CompletionResponse{}
}

// buildCompletion applies the parsed replacement unless it is a no-op
// against the current buffer lines.
func buildCompletion(state *RequestState, startLine, endLineInc int, lines []string) *types.CompletionResponse {
	currentLines := state.Input.Current.File.Lines
	if endLineInc <= len(currentLines) && isNoOpReplacement(lines, currentLines[startLine-1:endLineInc]) {
		return emptyResponse()
	}

	return &types.CompletionResponse{
		Completion: &types.Completion{
			StartLine:  startLine,
			EndLineInc: endLineInc,
			Lines:      lines,
		},
	}
}
