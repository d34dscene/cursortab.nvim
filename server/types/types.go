package types

// Completion represents a code completion with line range and content
type Completion struct {
	StartLine  int // 1-indexed
	EndLineInc int // 1-indexed, inclusive
	Lines      []string
}

// CursorPredictionTarget represents the target line for a cursor jump.
type CursorPredictionTarget struct {
	LineNumber      int32 // 1-indexed
	ShouldRetrigger bool
}

// CompletionResponse contains one completion and optional follow-up metadata.
type CompletionResponse struct {
	Completion   *Completion
	CursorTarget *CursorPredictionTarget
	Confidence   *float64
}

// DiagnosticSeverity matches Neovim's vim.diagnostic.severity values.
type DiagnosticSeverity int

const (
	SeverityError       DiagnosticSeverity = 1
	SeverityWarning     DiagnosticSeverity = 2
	SeverityInformation DiagnosticSeverity = 3
	SeverityHint        DiagnosticSeverity = 4
)

// String returns the short uppercase label for the severity.
func (s DiagnosticSeverity) String() string {
	switch s {
	case SeverityError:
		return "ERROR"
	case SeverityWarning:
		return "WARNING"
	case SeverityInformation:
		return "INFORMATION"
	case SeverityHint:
		return "HINT"
	default:
		return "ERROR"
	}
}

// Diagnostics holds LSP diagnostics for a buffer.
type Diagnostics struct {
	FilePath string        // Workspace-relative path
	Items    []*Diagnostic // Individual diagnostic entries
}

// Diagnostic represents a single LSP diagnostic from Neovim.
type Diagnostic struct {
	Message  string
	Source   string
	Severity DiagnosticSeverity
	Range    *CursorRange
}

// TreesitterContext holds treesitter-derived scope information around the cursor
type TreesitterContext struct {
	EnclosingSignature string
	Siblings           []*TreesitterSymbol
	Imports            []string
	// SyntaxRanges contains ancestor AST node line ranges around the cursor,
	// ordered innermost to outermost. Used to snap editable/context regions
	// to meaningful syntax boundaries (e.g. function, class, block).
	SyntaxRanges []*LineRange
}

// LineRange represents a 1-indexed inclusive line range
type LineRange struct {
	StartLine int // 1-indexed
	EndLine   int // 1-indexed, inclusive
}

// TreesitterSymbol represents a named symbol extracted from treesitter
type TreesitterSymbol struct {
	Name      string
	Signature string
	Line      int // 1-indexed
}

// GitDiffContext holds staged git diff information for commit message editing.
// Contains either the full unified diff (when small) or extracted symbol lines.
type GitDiffContext struct {
	Diff string // Full unified diff or symbol summary in git diff format
}

// RetrievalQuery is a cursor-derived lookup against the workspace code index.
// Identifiers carry the strongest signal and are scored above plain tokens.
type RetrievalQuery struct {
	Root        string   // Workspace root the index is built from
	CurrentPath string   // File the cursor is in, down-weighted and never self-first
	Identifiers []string // Names near the cursor and in the enclosing scope
	Tokens      []string // Lower-weight lexical tokens from the surrounding lines
	Limit       int      // Max chunks to return
}

// RetrievalChunk is one indexed code block selected for a prompt.
type RetrievalChunk struct {
	Path      string // Workspace-relative path
	Kind      string // func, method, type, const, var, class, ...
	Name      string // Declared name, best effort
	Signature string // First line of the declaration
	Content   string // Full source of the block
	Score     float64
}

// RetrievalContext holds cross-file code blocks selected for a prompt.
type RetrievalContext struct {
	Chunks []RetrievalChunk
}

// FileDiffHistory represents cumulative diffs for a specific file in the workspace
type FileDiffHistory struct {
	FileName    string
	DiffHistory []*DiffEntry
}

// DiffSource indicates the origin of a diff entry
type DiffSource string

const (
	DiffSourceManual    DiffSource = "manual"
	DiffSourcePredicted DiffSource = "predicted"
)

// DiffEntry represents a single diff operation with structured before/after content
// This allows providers to format the diff in their required format
type DiffEntry struct {
	// Original is the content before the change (the text that was replaced/deleted)
	Original string
	// Updated is the content after the change (the new text)
	Updated string
	// Source indicates whether this change was manual (user) or predicted (AI)
	Source DiffSource
	// TimestampNs is when the change was recorded (UnixNano)
	TimestampNs int64
	// StartLine is the approximate 1-indexed line in the buffer where the change starts
	StartLine int
}

// GetOriginal returns the original content (implements utils.DiffEntry interface)
func (d *DiffEntry) GetOriginal() string { return d.Original }

// GetUpdated returns the updated content (implements utils.DiffEntry interface)
func (d *DiffEntry) GetUpdated() string { return d.Updated }

// CursorRange represents a range in the file (follows LSP conventions)
type CursorRange struct {
	StartLine      int // 1-indexed
	StartCharacter int // 0-indexed
	EndLine        int // 1-indexed
	EndCharacter   int // 0-indexed
}

// RecentBufferSnapshot represents a snapshot of another open file for context
type RecentBufferSnapshot struct {
	FilePath    string   // Full file path
	Lines       []string // First N lines of the file
	TimestampMs int64    // Unix epoch milliseconds when file was last accessed
}

// FIMTokenConfig holds FIM (Fill-in-the-Middle) token configuration.
// When the provider's FIMTokens is non-nil, tokenized FIM mode is used and
// Prefix/Suffix/Middle must all be set. When nil, the FIM provider uses the
// OpenAI completions API prompt+suffix format (e.g. DeepSeek).
type FIMTokenConfig struct {
	Prefix      string `json:"prefix"`       // Token before the prefix content (e.g., "<|fim_prefix|>")
	Suffix      string `json:"suffix"`       // Token before the suffix content (e.g., "<|fim_suffix|>")
	Middle      string `json:"middle"`       // Token before the middle/completion (e.g., "<|fim_middle|>")
	RepoName    string `json:"repo_name"`    // Optional repo-level FIM token (e.g., "<|file_sep|>")
	FileSep     string `json:"file_sep"`     // Optional file separator token (e.g., "<|file_sep|>")
	Filename    string `json:"filename"`     // Optional per-file context header (e.g., "<filename>"), Mellum style
	SuffixFirst bool   `json:"suffix_first"` // Emit suffix content before prefix content (Mellum, SeedCoder style)
}

// EndpointConfig describes one model endpoint (type model, or the optional
// edit model in dual mode).
type EndpointConfig struct {
	URL       string `json:"url"` // server base URL
	APIKey    string `json:"api_key"`
	Model     string `json:"model"`                // model id, "" or "auto" = pick best for the role via probe + family table
	MaxTokens int    `json:"max_tokens"`           // generation cap, 0 = role default (type: 64, edit: 256)
	TimeoutMs int    `json:"timeout_ms,omitempty"` // 0 = role default (type: 6000, edit: 20000), Lua omits this key
}

// ProviderConfig holds configuration for providers
type ProviderConfig struct {
	Endpoint           EndpointConfig  `json:"endpoint"`     // type model (autocomplete)
	NextEdit           *EndpointConfig `json:"next_edit"`    // nil = single mode
	ContextSize        int             `json:"context_size"` // 0 = auto: probe /props or /v1/models meta, else 8192
	Temperature        float64         `json:"temperature"`
	FIMTokens          *FIMTokenConfig `json:"fim_tokens"`      // nil = probe /tokenize or family preset
	CompletionPath     string          `json:"completion_path"` // "" = /v1/completions
	RetrievalEnabled   bool            `json:"retrieval_enabled"`
	RetrievalMaxChunks int             `json:"retrieval_max_chunks"`
	Logprobs           bool            `json:"logprobs"`
	MinConfidence      float64         `json:"min_confidence"` // must be <= 0, 0 disables
}
