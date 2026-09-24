package ctx

import (
	"context"

	"cursortab/logger"
	"cursortab/types"
)

// Collect executes the requested materials in order against parent and reports
// how many bytes they add to the prompt, as consumed from the limits' shared
// budget. A material whose collect fails degrades to its empty request with a
// Debug log. An absent material is never an error.
func Collect(parent context.Context, input ContextSourceInput, requirements Materials) (Materials, int) {
	if len(requirements) == 0 {
		return nil, 0
	}

	if input.Limits.ContextChars >= 0 {
		input.Budget = NewBudget(input.Limits.ContextChars)
	}

	collected := make(Materials, len(requirements))
	for i, requirement := range requirements {
		material, err := requirement.collect(parent, input)
		if err != nil {
			logger.Debug("context: material %T failed: %v, using empty", requirement, err)
			material = requirement
		}
		collected[i] = material
	}

	used := 0
	if input.Budget != nil {
		used = input.Budget.Used()
	}
	return collected, used
}

// Budget bounds the bytes context materials may add to a prompt. Collectors
// consume from it in the order the provider lists its materials, so the list
// order is the priority order.
type Budget struct {
	remaining int
	used      int
}

func NewBudget(chars int) *Budget {
	return &Budget{remaining: max(chars, 0)}
}

func (b *Budget) Remaining() int {
	return b.remaining
}

func (b *Budget) Used() int {
	return b.used
}

// Take consumes up to n bytes and returns how many were granted.
func (b *Budget) Take(n int) int {
	granted := min(max(n, 0), b.remaining)
	b.remaining -= granted
	b.used += granted
	return granted
}

// Materials is a set of context material values. The concrete Go type is the
// material identity. Zero-value materials are also collection requests.
type Materials []material

func Find[T material](materials Materials) (T, bool) {
	for _, material := range materials {
		if typed, ok := material.(T); ok {
			return typed, true
		}
	}
	var zero T
	return zero, false
}

type material interface {
	collect(context.Context, ContextSourceInput) (material, error)
}

// CompletionInput is the provider-visible request shape: current editor state
// plus the materials the provider asked the collector to gather. ContextChars
// is the byte size the collected materials add to the prompt, which providers
// subtract from the window budget.
type CompletionInput struct {
	Current      CurrentSnapshot
	Materials    Materials
	ContextChars int
}

type CurrentSnapshot struct {
	WorkspacePath  string
	File           FileSnapshot
	Cursor         CursorPosition
	ViewportHeight int
}

type FileSnapshot struct {
	Path  string
	Lines []string
}

type CursorPosition struct {
	// Row is 1-indexed.
	Row int
	// Col is a 0-indexed byte column.
	Col int
}

// ContextSourceInput is collector-only input. It may hold live readers and
// engine limits, but providers only see collected material values. Budget is
// nil when the provider does not bound cross-file context.
type ContextSourceInput struct {
	Current   CurrentSnapshot
	Snapshot  FileContextSnapshot
	Buffer    bufferContextReader
	Limits    CollectionLimits
	Budget    *Budget
	Retriever Retriever
}

type bufferContextReader interface {
	Diagnostics() *types.Diagnostics
	TreesitterSymbols(row int, col int, maxSiblings int) *types.TreesitterContext
}

type FileContextSnapshot struct {
	CurrentDiffHistories []*types.DiffEntry
	RecentFiles          []RecentFileSnapshot
	NowNs                int64
}

type RecentFileSnapshot struct {
	Path          string
	FirstLines    []string
	DiffHistories []*types.DiffEntry
	LastAccessNs  int64
}

// CollectionLimits are engine-owned execution bounds for collection.
// Providers choose material types. The engine chooses runtime limits.
type CollectionLimits struct {
	MaxSiblings        int
	MaxDiffBytes       int
	MaxChangedSymbols  int
	MaxRecentSnapshots int
	MaxRecentFileBytes int
	MaxDiffTokens      int
	MaxRetrievalChunks int
	// ContextChars bounds the total bytes cross-file materials may add to
	// the prompt. Negative disables budgeting.
	ContextChars int
}

type fileContextNeeds struct {
	RecentFileLines         bool
	RecentFileDiffHistories bool
	CurrentDiffHistories    bool
}

// FileContextNeeds reports which frozen file-context fields collection reads.
func (materials Materials) FileContextNeeds() fileContextNeeds {
	var needs fileContextNeeds
	for _, material := range materials {
		switch material.(type) {
		case RecentFiles:
			needs.RecentFileLines = true
		case EditHistory:
			needs.RecentFileDiffHistories = true
			needs.CurrentDiffHistories = true
		}
	}
	return needs
}
