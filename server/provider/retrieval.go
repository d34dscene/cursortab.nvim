package provider

import (
	"strings"

	sourcectx "cursortab/ctx"
	"cursortab/types"
)

// retrievalChunks returns the workspace code selected for this request, or
// nil when retrieval is off or nothing matched.
func retrievalChunks(input sourcectx.CompletionInput) []types.RetrievalChunk {
	material, ok := sourcectx.Find[sourcectx.Retrieval](input.Materials)
	if !ok || material.Data == nil {
		return nil
	}
	return material.Data.Chunks
}

// bodylessKinds are declaration kinds whose text carries an executable body.
// A retrieved body leaks into the completion: a chunk ending in `return nil,
// nil` makes the model emit exactly that. Prompts carry only the header for
// these kinds. Types, consts, and vars have no body to leak, so they keep
// their full text.
var bodylessKinds = map[string]bool{
	"func": true, "method": true, "function": true, "def": true, "fn": true,
	"class": true,
}

// promptForm returns the text a prompt should carry for a retrieved chunk.
func promptForm(c types.RetrievalChunk) string {
	if bodylessKinds[c.Kind] {
		return c.Signature
	}
	return c.Content
}

// renderRetrievedPlain writes retrieved chunks as bare path headers. Used by
// dialects that have no FIM context tokens to hang file sections on.
func renderRetrievedPlain(b *strings.Builder, chunks []types.RetrievalChunk) {
	for _, chunk := range chunks {
		b.WriteString("# ")
		b.WriteString(chunk.Path)
		b.WriteString("\n")
		b.WriteString(promptForm(chunk))
		b.WriteString("\n\n")
	}
}
