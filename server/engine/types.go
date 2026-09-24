package engine

import (
	"context"
	"time"

	"cursortab/buffer"
	"cursortab/ctx"
	"cursortab/text"
	"cursortab/types"
)

type Buffer interface {
	Sync(workspacePath string) (*buffer.SyncResult, error)
	Lines() []string
	Row() int
	Col() int
	Path() string
	ViewportBounds() (top, bottom int)
	AvailableWidth() int
	PreviousLines() []string
	OriginalLines() []string
	DiffHistories() []*types.DiffEntry
	DiskLines() []string
	Diagnostics() *types.Diagnostics
	TreesitterSymbols(row, col, maxSiblings int) *types.TreesitterContext
	CursorScopes() []string
	SetFileContext(ctx buffer.FileContext)
	HasChanges(startLine, endLineInc int, lines []string) bool
	PrepareCompletion(startLine, endLineInc int, lines []string, groups []*text.Group) buffer.Batch
	CommitPending()
	CommitUserEdits() bool
	ClearDiffHistory()
	IsModified() bool
	SkipHistory() bool
	ShowCursorTarget(line int) error
	ClearUI() error
	MoveCursor(line int, center, mark bool) error
	RegisterEventHandler(handler func(event string, payload map[string]any)) error
	InsertText(line, col int, text string, keepUI bool) error
	ReplaceLine(line int, content string, keepUI bool) error
	InsertLine(line int, content string, keepUI bool) error
}

// Provider is the engine boundary for completion providers. The engine reads
// CompletionKind before gating, collects RequiredMaterials through ctx.Collect,
// then calls Complete. Concrete providers implement this contract through
// their own methods or a real shared implementation.
type Provider interface {
	CompletionKind() CompletionKind
	RequiredMaterials() ctx.Materials
	// MaterialsBudgetChars reports the byte budget cross-file materials may
	// add to the prompt, or -1 when unbounded.
	MaterialsBudgetChars() int
	Complete(ctx context.Context, input ctx.CompletionInput) (*types.CompletionResponse, error)
}

type StreamingProvider interface {
	StreamCompletion(ctx context.Context, input ctx.CompletionInput) (CompletionStream, error)
}

// CompletionKind describes the editing shape a provider can produce. Engine
// gating uses it to decide whether the current cursor position is a valid
// request input.
type CompletionKind int

const (
	// CompletionFIM fills between prefix and suffix supplied by the engine.
	CompletionFIM CompletionKind = iota
	// CompletionEdit may rewrite a nearby region and can drive cursor targets.
	CompletionEdit
)

// Role identifies which provider served a request or produced a display.
type Role int

const (
	// RoleType is the type provider (FIM, asked while typing).
	RoleType Role = iota
	// RoleEdit is the edit provider (next-edit, asked on pause in dual mode).
	RoleEdit
)

// Source identifies why a request was made: typing proof, or a pause.
type Source int

const (
	// SourceTyping is a debounce-fired request: the buffer edit proves intent.
	SourceTyping Source = iota
	// SourceIdle is a pause-fired request, the only one gated by no-edits.
	SourceIdle
)

const (
	defaultFileChunkLines     = 30
	defaultMaxRecentSnapshots = 3
	defaultMaxRecentFileBytes = 4096
	defaultMaxDiffBytes       = 4096
	defaultMaxChangedSymbols  = 50
	defaultMaxSiblings        = 50
)

// CompletionStream is the engine-visible runtime for line streaming.
// Provider prompt details, stop rules, cursor markers, and final parsing stay
// behind Finish, engine owns only UI lifecycle.
type CompletionStream interface {
	Lines() <-chan string
	Window() (windowStart int, oldLines []string)
	Cancel()
	Finish() (*types.CompletionResponse, error)
}

// displayedCompletion is the completion state currently rendered in the
// buffer. It is the source for accept, partial accept, typing-match rerender,
// and Esc rejection caching. gen/origin/bufferTick record which request
// produced it and whether the buffer moved since.
type displayedCompletion struct {
	completion      *types.Completion
	batch           buffer.Batch
	originalLines   []string
	groups          []*text.Group
	rejectCandidate *rejectedCompletion
	gen             uint64
	origin          Role
	bufferTick      uint64
}

func (d *displayedCompletion) advanceLine(wasInsertion bool) bool {
	if d.completion == nil || len(d.completion.Lines) <= 1 {
		return false
	}
	d.completion.Lines = d.completion.Lines[1:]
	d.completion.StartLine++
	d.groups = advanceGroupsAfterAccept(d.groups, wasInsertion)
	return len(d.groups) > 0
}

type streamingState struct {
	StageBuilder *text.IncrementalStageBuilder
	Manual       bool

	PendingLine    string // Buffer for last line (drop if truncated)
	HasPendingLine bool

	FirstStageRendered bool
}

type state int

const (
	stateIdle state = iota
	statePendingCompletion
	stateHasCompletion
	stateHasCursorTarget
	stateStreamingCompletion
)

func (s state) String() string {
	switch s {
	case stateIdle:
		return "Idle"
	case statePendingCompletion:
		return "PendingCompletion"
	case stateHasCompletion:
		return "HasCompletion"
	case stateHasCursorTarget:
		return "HasCursorTarget"
	case stateStreamingCompletion:
		return "StreamingCompletion"
	default:
		return "Unknown"
	}
}

// CursorPredictionConfig holds cursor prediction settings
type CursorPredictionConfig struct {
	Enabled            bool // Show jump indicators (default: true)
	AutoAdvance        bool // On no-op, jump to last line + retrigger (default: true)
	ProximityThreshold int  // Lines apart to trigger staging (default: 3)
}

// FileState holds per-file context that persists across file switches
type FileState struct {
	PreviousLines []string           // Content before user started editing this file
	DiffHistories []*types.DiffEntry // Cumulative diffs for this file
	OriginalLines []string           // Checkpoint for granular diffs (resets on CommitUserEdits)
	DiskLines     []string           // File content as last written to disk (resets only on save)
	LastAccessNs  int64              // Monotonic timestamp for LRU eviction
	FirstLines    []string           // First 30 lines for FileChunks context
}

// EngineConfig holds engine configuration
type EngineConfig struct {
	NsID                int
	CompletionTimeout   time.Duration // type role
	NextEditTimeout     time.Duration // edit role
	IdleCompletionDelay time.Duration // pause before edit-role consult AND idle retrigger
	TextChangeDebounce  time.Duration
	CursorPrediction    CursorPredictionConfig
	MaxDiffTokens       int      // Maximum tokens for diff history per file (0 = no limit)
	MaxVisibleLines     int      // Maximum lines per stage (0 = no limit)
	MaxRetrievalChunks  int      // Maximum retrieved code chunks per prompt (0 = index default)
	MinConfidence       float64  // Drop completions below this mean token logprob (0 = off)
	CompleteInInsert    bool     // Show completions in insert mode
	CompleteInNormal    bool     // Show completions in normal mode
	DisabledIn          []string // Treesitter scopes where completions are suppressed

	// Retriever answers workspace code lookups for the retrieval material.
	// Nil disables retrieval.
	Retriever ctx.Retriever

	// NextEditProvider is the edit-kind provider asked when the user pauses.
	// Nil keeps the engine in single mode where idle requests go to the type
	// provider.
	NextEditProvider Provider
}
