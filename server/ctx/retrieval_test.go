package ctx

import (
	"context"
	"testing"

	"cursortab/assert"
	"cursortab/types"
)

type fakeRetriever struct {
	got    types.RetrievalQuery
	chunks []types.RetrievalChunk
}

func (f *fakeRetriever) Retrieve(q types.RetrievalQuery) []types.RetrievalChunk {
	f.got = q
	return f.chunks
}

func retrievalInput(retriever Retriever, budget *Budget) ContextSourceInput {
	return ContextSourceInput{
		Current: CurrentSnapshot{
			WorkspacePath: "/w",
			File: FileSnapshot{
				Path: "/w/pkg/a.go",
				Lines: []string{
					"package pkg",
					"",
					"func handleUser(id string) {",
					"",
					"",
					"",
				},
			},
			Cursor: CursorPosition{Row: 4, Col: 0},
		},
		Buffer:    &materialBuffer{},
		Budget:    budget,
		Retriever: retriever,
	}
}

func TestRetrievalCollectsChunks(t *testing.T) {
	retriever := &fakeRetriever{
		chunks: []types.RetrievalChunk{{Path: "pkg/b.go", Name: "helper", Content: "func helper() {}"}},
	}

	material, err := Retrieval{}.collect(context.Background(), retrievalInput(retriever, nil))
	assert.NoError(t, err, "collect")

	data := material.(Retrieval).Data
	assert.NotNil(t, data, "data present")
	assert.Len(t, 1, data.Chunks, "chunks kept")

	assert.Equal(t, "/w", retriever.got.Root, "query root")
	assert.Equal(t, "pkg/a.go", retriever.got.CurrentPath, "query uses workspace-relative path")
	assert.Contains(t, joinTokens(retriever.got.Identifiers), "handle", "near-cursor identifiers include the declaration")
}

func TestRetrievalWithoutRetrieverIsEmpty(t *testing.T) {
	material, err := Retrieval{}.collect(context.Background(), retrievalInput(nil, nil))
	assert.NoError(t, err, "collect")
	assert.Nil(t, material.(Retrieval).Data, "no retriever means no data")
}

func TestRetrievalHonorsByteBudget(t *testing.T) {
	chunks := []types.RetrievalChunk{
		{Path: "a.go", Content: "small"},
		{Path: "b.go", Content: "0123456789012345678901234567890123456789"},
	}
	retriever := &fakeRetriever{chunks: chunks}

	// Enough for the small chunk only.
	material, err := Retrieval{}.collect(context.Background(), retrievalInput(retriever, NewBudget(40)))
	assert.NoError(t, err, "collect")

	data := material.(Retrieval).Data
	assert.NotNil(t, data, "data present")
	assert.Len(t, 1, data.Chunks, "oversized chunk dropped")
	assert.Equal(t, "a.go", data.Chunks[0].Path, "small chunk kept")
}

func joinTokens(tokens []string) string {
	out := ""
	for _, token := range tokens {
		out += token + " "
	}
	return out
}
