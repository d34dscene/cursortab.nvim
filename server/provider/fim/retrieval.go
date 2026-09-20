package fim

import (
	"strings"

	"cursortab/provider"
	"cursortab/types"
)

// filenameRetrieval emits retrieved chunks in Mellum's per-file format, one
// "<filename>path\n<content>" block per chunk.
func filenameRetrieval(b *strings.Builder, tokens types.FIMTokenConfig, chunks []types.RetrievalChunk) {
	for _, chunk := range chunks {
		b.WriteString(tokens.Filename)
		b.WriteString(chunk.Path)
		b.WriteString("\n")
		b.WriteString(provider.PromptForm(chunk))
		b.WriteString("\n")
	}
}

// sectionRetrieval emits retrieved chunks as file_sep sections (Qwen style),
// tagged with their real workspace-relative path so the model reads them the
// same way it reads any other repo file.
func sectionRetrieval(b *strings.Builder, fileSep string, chunks []types.RetrievalChunk) {
	for _, chunk := range chunks {
		b.WriteString(fileSep)
		b.WriteString(chunk.Path)
		b.WriteString("\n")
		b.WriteString(provider.PromptForm(chunk))
		b.WriteString("\n")
	}
}
