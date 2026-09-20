package ctx

import (
	"context"
	"path/filepath"
	"strings"

	"cursortab/types"
	"cursortab/utils"
)

// Retriever answers cursor-derived lookups against a workspace code index.
// Implemented by index.Manager. It must never block on index construction.
type Retriever interface {
	Retrieve(q types.RetrievalQuery) []types.RetrievalChunk
}

// retrievalQueryWindowLines is how far around the cursor source is scanned to
// build the query. Wide enough to catch the block being edited, narrow enough
// to stay about the cursor.
const retrievalQueryWindowLines = 40

// perChunkOverheadBytes covers the path line and section framing added when a
// chunk is rendered into a prompt.
const perChunkOverheadBytes = 16

// Retrieval carries workspace code selected for the cursor. Conversation with
// the index happens through ContextSourceInput.Retriever.
type Retrieval struct {
	Data *types.RetrievalContext
}

func (Retrieval) collect(_ context.Context, input ContextSourceInput) (material, error) {
	if input.Retriever == nil {
		return Retrieval{}, nil
	}
	q := buildRetrievalQuery(input)
	if q.Root == "" || len(q.Identifiers) == 0 {
		return Retrieval{}, nil
	}

	chunks := input.Retriever.Retrieve(q)
	if len(chunks) == 0 {
		return Retrieval{}, nil
	}

	kept := make([]types.RetrievalChunk, 0, len(chunks))
	for _, c := range chunks {
		if input.Budget != nil {
			cost := len(c.Content) + len(c.Path) + perChunkOverheadBytes
			if cost > input.Budget.Remaining() {
				continue
			}
			input.Budget.Take(cost)
		}
		kept = append(kept, c)
	}
	if len(kept) == 0 {
		return Retrieval{}, nil
	}
	return Retrieval{Data: &types.RetrievalContext{Chunks: kept}}, nil
}

// buildRetrievalQuery derives the lookup from the cursor. Identifiers come
// from the lines immediately around the cursor and the enclosing declaration,
// which is where the reused names live.
func buildRetrievalQuery(input ContextSourceInput) types.RetrievalQuery {
	lines := input.Current.File.Lines
	if len(lines) == 0 {
		return types.RetrievalQuery{}
	}

	row := input.Current.Cursor.Row
	start := max(1, row-retrievalQueryWindowLines)
	end := min(len(lines), row+retrievalQueryWindowLines)
	window := strings.Join(lines[start-1:end], "\n")

	nearStart := max(1, row-4)
	nearEnd := min(len(lines), row+4)
	near := strings.Join(lines[nearStart-1:nearEnd], "\n")

	var enclosing string
	if input.Buffer != nil {
		if ts := input.Buffer.TreesitterSymbols(row, input.Current.Cursor.Col, 0); ts != nil {
			enclosing = ts.EnclosingSignature
		}
	}

	identifiers := utils.TokenizeCode(near + "\n" + enclosing)

	limit := input.Limits.MaxRetrievalChunks
	if limit <= 0 {
		limit = 0 // index applies its own default
	}

	return types.RetrievalQuery{
		Root:        input.Current.WorkspacePath,
		CurrentPath: relativePath(input.Current.WorkspacePath, input.Current.File.Path),
		Identifiers: identifiers,
		Tokens:      utils.TokenizeCode(window),
		Limit:       limit,
	}
}

func relativePath(root, path string) string {
	if root == "" || path == "" {
		return filepath.ToSlash(path)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}
