package inline

import (
	"strings"
	"testing"

	"cursortab/assert"
	sourcectx "cursortab/ctx"
	"cursortab/provider"
	"cursortab/types"
)

func TestRetrievalRequestedWhenEnabled(t *testing.T) {
	enabled := NewProvider(&types.ProviderConfig{RetrievalEnabled: true})
	_, ok := sourcectx.Find[sourcectx.Retrieval](enabled.RequiredMaterials())
	assert.True(t, ok, "retrieval requested when enabled")

	disabled := NewProvider(&types.ProviderConfig{})
	_, ok = sourcectx.Find[sourcectx.Retrieval](disabled.RequiredMaterials())
	assert.False(t, ok, "retrieval not requested when disabled")
}

func TestBuildPromptRendersRetrievedBlocksFirst(t *testing.T) {
	config := &types.ProviderConfig{ProviderModel: "test-model"}
	p := NewProvider(config)

	input := completionInput([]string{"func main() {"}, 1, 13)
	input.Materials = sourcectx.Materials{
		sourcectx.Retrieval{Data: &types.RetrievalContext{Chunks: []types.RetrievalChunk{
			{Path: "store.go", Content: "func LoadUser() {}"},
		}}},
	}
	state := &provider.RequestState{
		Input:  input,
		Window: provider.RequestWindow{Lines: input.Current.File.Lines, CursorLine: input.Current.Cursor.Row - 1},
	}

	req := buildPromptForTest(p, state)

	assert.True(t, strings.HasPrefix(req.Prompt, "# store.go\nfunc LoadUser() {}\n\n"), "retrieved block leads the prompt")
	assert.Contains(t, req.Prompt, "func main() {", "buffer content still present")
}

func TestBuildPromptWithoutRetrievalIsUnchanged(t *testing.T) {
	config := &types.ProviderConfig{ProviderModel: "test-model"}
	p := NewProvider(config)

	input := completionInput([]string{"func main() {"}, 1, 13)
	state := &provider.RequestState{
		Input:  input,
		Window: provider.RequestWindow{Lines: input.Current.File.Lines, CursorLine: input.Current.Cursor.Row - 1},
	}

	req := buildPromptForTest(p, state)

	assert.False(t, strings.Contains(req.Prompt, "# "), "no reference blocks without retrieval")
}
