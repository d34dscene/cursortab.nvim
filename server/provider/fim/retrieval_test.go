package fim

import (
	"strings"
	"testing"

	"cursortab/assert"
	sourcectx "cursortab/ctx"
	"cursortab/types"
)

func retrievalInput(lines []string, chunks []types.RetrievalChunk) sourcectx.CompletionInput {
	input := completionInput(lines, 1, len(lines[0]))
	input.Materials = sourcectx.Materials{
		sourcectx.Retrieval{Data: &types.RetrievalContext{Chunks: chunks}},
	}
	return input
}

func TestRequiredMaterialsIncludeRetrievalWhenEnabled(t *testing.T) {
	enabled := NewProvider(&types.ProviderConfig{
		RetrievalEnabled: true,
		FIMTokens:        &types.FIMTokenConfig{Prefix: "<p>", Suffix: "<s>", Middle: "<m>"},
	})
	_, ok := sourcectx.Find[sourcectx.Retrieval](enabled.RequiredMaterials())
	assert.True(t, ok, "retrieval requested when enabled")

	disabled := NewProvider(&types.ProviderConfig{
		FIMTokens: &types.FIMTokenConfig{Prefix: "<p>", Suffix: "<s>", Middle: "<m>"},
	})
	_, ok = sourcectx.Find[sourcectx.Retrieval](disabled.RequiredMaterials())
	assert.False(t, ok, "retrieval not requested when disabled")
}

func TestBuildPromptRendersRetrievalWithFilenameTokens(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "mellum",
		FIMTokens: &types.FIMTokenConfig{
			Prefix: "<PRE>", Suffix: "<SUF>", Middle: "<MID>",
			Filename: "<filename>", SuffixFirst: true,
		},
	}
	p := NewProvider(config)
	chunks := []types.RetrievalChunk{{Path: "pkg/helper.go", Content: "func helper() {}"}}

	req := buildPromptForTest(p, stateForInput(retrievalInput([]string{"x"}, chunks)))

	assert.Contains(t, req.Prompt, "<filename>pkg/helper.go\nfunc helper() {}", "retrieved chunk rendered")
	assert.True(t, strings.Contains(req.Prompt, "<MID>"), "FIM tokens still present")

	// The retrieved block must precede the current file header so the model
	// reads it as context, not as the file being completed.
	assert.True(t,
		strings.Index(req.Prompt, "pkg/helper.go") < strings.LastIndex(req.Prompt, "<filename>"),
		"retrieved chunk precedes the current file header")
}

func TestBuildPromptRendersRetrievalAsFileSeparatedSections(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "qwen",
		FIMTokens: &types.FIMTokenConfig{
			Prefix: "<PRE>", Suffix: "<SUF>", Middle: "<MID>",
			RepoName: "<repo_name>", FileSep: "<file_sep>",
		},
	}
	p := NewProvider(config)
	chunks := []types.RetrievalChunk{{Path: "pkg/helper.go", Content: "func helper() {}"}}

	req := buildPromptForTest(p, stateForInput(retrievalInput([]string{"x"}, chunks)))

	assert.Contains(t, req.Prompt, "<file_sep>pkg/helper.go\nfunc helper() {}", "retrieved section rendered")
}

func TestBuildPromptWithoutRetrievalKeepsRepoContext(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "qwen",
		FIMTokens: &types.FIMTokenConfig{
			Prefix: "<PRE>", Suffix: "<SUF>", Middle: "<MID>",
			RepoName: "<repo_name>", FileSep: "<file_sep>",
		},
	}
	p := NewProvider(config)

	input := completionInput([]string{"x"}, 1, 1)
	req := buildPromptForTest(p, stateForInput(input))

	assert.False(t, strings.Contains(req.Prompt, "pkg/helper.go"), "no retrieval section without chunks")
}
