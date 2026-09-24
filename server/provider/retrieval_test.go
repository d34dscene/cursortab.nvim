package provider

import (
	"testing"

	"cursortab/assert"
	"cursortab/types"
)

func TestPromptFormOmitsBodiesForExecutableDeclarations(t *testing.T) {
	chunk := types.RetrievalChunk{
		Kind:      "func",
		Signature: "func LoadUser(ctx context.Context) (*User, error) {",
		Content:   "func LoadUser(ctx context.Context) (*User, error) {\n\treturn nil, nil\n}",
	}
	assert.Equal(t, chunk.Signature, promptForm(chunk), "function body omitted")
}

func TestPromptFormKeepsTypeContent(t *testing.T) {
	chunk := types.RetrievalChunk{
		Kind:      "type",
		Signature: "type User struct {",
		Content:   "type User struct {\n\tID string\n}",
	}
	assert.Equal(t, chunk.Content, promptForm(chunk), "type fields kept")
}
