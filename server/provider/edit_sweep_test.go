package provider

import (
	"cursortab/assert"
	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/types"
	"strings"
	"testing"
)

func sweepCompletionInput(filePath string, lines []string, cursorRow int, cursorCol int, materials ...sourcectx.Materials) sourcectx.CompletionInput {
	var collected sourcectx.Materials
	for _, material := range materials {
		collected = append(collected, material...)
	}
	return sourcectx.CompletionInput{
		Current: sourcectx.CurrentSnapshot{
			File: sourcectx.FileSnapshot{
				Path:  filePath,
				Lines: lines,
			},
			Cursor: sourcectx.CursorPosition{
				Row: cursorRow,
				Col: cursorCol,
			},
		},
		Materials: collected,
	}
}

func TestSweepBuildPrompt_EmptyLines(t *testing.T) {
	config := &types.ProviderConfig{
		Endpoint: types.EndpointConfig{Model: "test-model"},
	}
	p := newSweepTestProvider(config)

	ctx := stateForInput(sweepCompletionInput("main.go", nil, 1, 0))

	req := buildPromptForTest(p, ctx)

	assert.True(t, strings.Contains(req.Prompt, "<|file_sep|>original/main.go"), "should have original marker")
	assert.True(t, strings.Contains(req.Prompt, "<|file_sep|>current/main.go"), "should have current marker")
	assert.True(t, strings.Contains(req.Prompt, "<|file_sep|>updated/main.go"), "should have updated marker")
}

func TestBuildPrompt_WithContent(t *testing.T) {
	config := &types.ProviderConfig{
		Endpoint: types.EndpointConfig{Model: "test-model"},
	}
	p := newSweepTestProvider(config)

	ctx := stateForInput(sweepCompletionInput("main.go", []string{"line 1", "line 2"}, 1, 0))

	req := buildPromptForTest(p, ctx)

	assert.True(t, strings.Contains(req.Prompt, "line 1\nline 2"), "should contain file content")
}

func TestBuildPrompt_WithDiffHistory(t *testing.T) {
	config := &types.ProviderConfig{
		Endpoint: types.EndpointConfig{Model: "test-model"},
	}
	p := newSweepTestProvider(config)

	ctx := stateForInput(sweepCompletionInput("main.go", []string{"line 1"}, 1, 0, sourcectx.Materials{
		sourcectx.EditHistory{Files: []*types.FileDiffHistory{
			{
				FileName: "other.go",
				DiffHistory: []*types.DiffEntry{
					{Original: "old code", Updated: "new code"},
				},
			},
		}},
	}))

	req := buildPromptForTest(p, ctx)

	assert.True(t, strings.Contains(req.Prompt, "other.go.diff"), "should have diff section")
	assert.True(t, strings.Contains(req.Prompt, "original:\nold code"), "should have original in diff")
	assert.True(t, strings.Contains(req.Prompt, "updated:\nnew code"), "should have updated in diff")
}

func TestParseCompletion_NoChange(t *testing.T) {
	config := &types.ProviderConfig{
		Endpoint: types.EndpointConfig{Model: "test-model"},
	}
	p := newSweepTestProvider(config)

	ctx := stateForInput(sweepCompletionInput("", []string{"line 1", "line 2"}, 1, 0))

	resp := parseCompletionForTest(p, ctx, &openai.CompletionResult{
		Text: "line 1\nline 2",
	})
	assert.Nil(t, resp.Completion, "no completions when text is same")
}

func TestParseCompletion_WithChange(t *testing.T) {
	config := &types.ProviderConfig{
		Endpoint: types.EndpointConfig{Model: "test-model"},
	}
	p := newSweepTestProvider(config)

	ctx := stateForInput(sweepCompletionInput("", []string{"line 1", "line 2"}, 1, 0))

	resp := parseCompletionForTest(p, ctx, &openai.CompletionResult{
		Text: "line 1\nmodified line 2",
	})
	assert.NotNil(t, resp, "should have response")
	assert.True(t, resp.Completion != nil, "should have completions")
}

func TestParseCompletion_StripsStopMarkers(t *testing.T) {
	config := &types.ProviderConfig{
		Endpoint: types.EndpointConfig{Model: "test-model"},
	}
	p := newSweepTestProvider(config)

	ctx := stateForInput(sweepCompletionInput("", []string{"line 1"}, 1, 0))

	resp := parseCompletionForTest(p, ctx, &openai.CompletionResult{
		Text: "modified line 1<|file_sep|>",
	})
	assert.NotNil(t, resp, "should have response")
}

func newSweepTestProvider(config *types.ProviderConfig) testDialectProvider {
	return mustBuildProvider(RoleEdit, "edit-sweep", config)
}
