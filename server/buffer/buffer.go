package buffer

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"cursortab/logger"
	"cursortab/text"
	"cursortab/types"

	"github.com/neovim/go-client/nvim"
	"github.com/sergi/go-diff/diffmatchpatch"
)

// Batch represents deferred editor operations
type Batch interface {
	Execute() error
}

// SyncResult contains state after syncing with editor
type SyncResult struct {
	BufferChanged bool
	OldPath       string
	NewPath       string
}

type Config struct {
	NsID int
}

type NvimBuffer struct {
	client *nvim.Nvim // stored internally, set via SetClient

	mu    sync.Mutex
	inbox []map[string]any // payloads queued by the notification goroutine

	// Mirror state maintained from event payloads
	lines          []string
	tick           int // changedtick of the last applied text payload
	row            int // 1-indexed
	col            int // 0-indexed
	path           string
	viewportTop    int // First visible line (1-indexed)
	viewportBottom int // Last visible line (1-indexed)

	needFull   bool // mirror text is untrustworthy until a full payload arrives
	resyncSent bool // one on_resync request is outstanding
	textWidth  int  // window text width from payloads (win width minus textoff)

	diffHistories []*types.DiffEntry // Structured diff history for provider consumption
	previousLines []string           // Buffer content before the most recent edit (for sweep provider)

	originalLines []string // Checkpoint for extracting granular diffs (reset on each commit)
	diskLines     []string // File content as last written to disk (reset only on ClearDiffHistory)

	config Config

	// Pending completion state (committed only on accept)
	pending *PendingEdit
}

// PendingEdit holds pending completion state committed only on accept
type PendingEdit struct {
	StartLine        int
	EndLineInclusive int
	Lines            []string
}

func New(config Config) *NvimBuffer {
	return &NvimBuffer{
		lines:         []string{},
		row:           1,
		needFull:      true,
		diffHistories: []*types.DiffEntry{},
		previousLines: []string{},
		originalLines: []string{},
		config:        config,
	}
}

// SetClient stores the nvim client for all buffer operations
func (b *NvimBuffer) SetClient(n *nvim.Nvim) {
	b.client = n
	b.mu.Lock()
	defer b.mu.Unlock()
	b.inbox = nil
	b.needFull = true
	b.resyncSent = false
}

// Accessor methods implementing engine.Buffer interface

func (b *NvimBuffer) Lines() []string { return b.lines }

func (b *NvimBuffer) Row() int { return b.row }

func (b *NvimBuffer) Col() int { return b.col }

func (b *NvimBuffer) Path() string { return b.path }

func (b *NvimBuffer) ViewportBounds() (top, bottom int) {
	return b.viewportTop, b.viewportBottom
}

// AvailableWidth reports the window text width from event payloads (0 = unknown).
func (b *NvimBuffer) AvailableWidth() int {
	return b.textWidth
}

func (b *NvimBuffer) PreviousLines() []string { return b.previousLines }

func (b *NvimBuffer) OriginalLines() []string { return b.originalLines }

func (b *NvimBuffer) DiskLines() []string { return b.diskLines }

func (b *NvimBuffer) DiffHistories() []*types.DiffEntry { return b.diffHistories }

// ClearDiffHistory resets the diff history and checkpoint to current state.
// Called on file save to establish a clean baseline.
func (b *NvimBuffer) ClearDiffHistory() {
	b.diffHistories = []*types.DiffEntry{}
	b.originalLines = make([]string, len(b.lines))
	copy(b.originalLines, b.lines)
	b.diskLines = make([]string, len(b.lines))
	copy(b.diskLines, b.lines)
}

// IsModified returns true if the buffer content differs from what's on disk.
func (b *NvimBuffer) IsModified() bool {
	if len(b.lines) != len(b.diskLines) {
		return true
	}
	for i := range b.lines {
		if b.lines[i] != b.diskLines[i] {
			return true
		}
	}
	return false
}

// noHistoryFiles is the list of filenames for which diff history is not recorded.
var noHistoryFiles = []string{
	"COMMIT_EDITMSG",
}

// SkipHistory returns true for files where diff history should not be recorded.
func (b *NvimBuffer) SkipHistory() bool {
	base := filepath.Base(b.path)
	return slices.Contains(noHistoryFiles, base)
}

// FileContext holds the state to restore when switching to a file.
type FileContext struct {
	PreviousLines []string
	OriginalLines []string
	DiskLines     []string
	DiffHistories []*types.DiffEntry
}

// SetFileContext restores file-specific state when switching to a file.
func (b *NvimBuffer) SetFileContext(ctx FileContext) {
	b.previousLines = copySlice(ctx.PreviousLines)
	b.originalLines = copySlice(ctx.OriginalLines)
	b.diskLines = copySlice(ctx.DiskLines)

	if ctx.DiffHistories != nil {
		b.diffHistories = make([]*types.DiffEntry, len(ctx.DiffHistories))
		copy(b.diffHistories, ctx.DiffHistories)
	} else {
		b.diffHistories = []*types.DiffEntry{}
	}
}

func copySlice(s []string) []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

// Sync drains queued event payloads into the mirror and reports a file switch.
// It never pulls buffer lines over RPC.
func (b *NvimBuffer) Sync(workspacePath string) (*SyncResult, error) {
	defer logger.Trace("buffer.Sync")()

	b.mu.Lock()
	inbox := b.inbox
	b.inbox = nil
	oldPath := b.path
	for _, payload := range inbox {
		b.applyPayload(payload, workspacePath)
	}
	resync := b.needFull && !b.resyncSent
	if resync {
		b.resyncSent = true
	}
	bufferChanged := b.path != oldPath
	newPath := b.path
	b.mu.Unlock()

	if resync {
		b.requestResync()
	}

	return &SyncResult{
		BufferChanged: bufferChanged,
		OldPath:       oldPath,
		NewPath:       newPath,
	}, nil
}

// pushEvent queues one payload from the go-client notification goroutine.
func (b *NvimBuffer) pushEvent(payload map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.inbox = append(b.inbox, payload)
}

// applyPayload merges one event payload into the mirror in arrival order.
// Caller holds b.mu.
func (b *NvimBuffer) applyPayload(payload map[string]any, workspacePath string) {
	if len(payload) == 0 {
		return
	}

	if path, ok := payload["path"].(string); ok {
		if rel := makeRelativeToWorkspace(path, workspacePath); rel != b.path {
			b.path = rel
			b.lines = []string{}
			b.needFull = true
		}
	}

	if v, ok := payloadInt(payload, "row"); ok {
		b.row = v
	}
	if v, ok := payloadInt(payload, "col"); ok {
		b.col = v
	}
	if v, ok := payloadInt(payload, "top"); ok {
		b.viewportTop = v
	}
	if v, ok := payloadInt(payload, "bot"); ok {
		b.viewportBottom = v
	}
	if v, ok := payloadInt(payload, "width"); ok {
		b.textWidth = v
	}

	if full, ok := payload["full"]; ok {
		b.applyFull(payload, full)
		return
	}
	if _, ok := payload["changed"]; ok {
		b.applyChanged(payload)
	}
}

// applyFull replaces the mirror text wholesale and re-anchors the tick.
// Caller holds b.mu.
func (b *NvimBuffer) applyFull(payload map[string]any, full any) {
	table, ok := full.(map[string]any)
	tick, hasTick := payloadInt(payload, "tick")
	lines, hasLines := decodeLines(table["lines"])
	if !ok || !hasTick || !hasLines {
		return
	}
	b.lines = lines
	b.tick = tick
	b.needFull = false
	b.resyncSent = false
}

// applyChanged replaces lines[first:last_old] with the payload lines, the
// on_lines recipe. A non-contiguous tick or a malformed range marks the mirror
// unsynced instead of applying, so Sync requests a full payload.
// Caller holds b.mu.
func (b *NvimBuffer) applyChanged(payload map[string]any) {
	if b.needFull {
		return
	}
	tick, hasTick := payloadInt(payload, "tick")
	if !hasTick || tick != b.tick+1 {
		b.needFull = true
		return
	}
	changed, ok := payload["changed"].(map[string]any)
	first, hasFirst := payloadInt(changed, "first")
	lastOld, hasLastOld := payloadInt(changed, "last_old")
	lastNew, hasLastNew := payloadInt(changed, "last_new")
	lines, hasLines := decodeLines(changed["lines"])
	if !ok || !hasFirst || !hasLastOld || !hasLastNew || !hasLines ||
		first < 0 || first > lastOld || lastOld > len(b.lines) || lastNew != first+len(lines) {
		b.needFull = true
		return
	}
	b.lines = slices.Concat(b.lines[:first], lines, b.lines[lastOld:])
	b.tick = tick
}

// requestResync asks Lua for a full payload via the cursortab_resync request.
// A failed request clears the outstanding flag so the next Sync retries.
func (b *NvimBuffer) requestResync() {
	if b.client == nil {
		b.clearResyncSent()
		return
	}
	if err := b.client.ExecLua(`require('cursortab').on_resync()`, nil); err != nil {
		logger.Error("error requesting buffer resync: %v", err)
		b.clearResyncSent()
	}
}

func (b *NvimBuffer) clearResyncSent() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resyncSent = false
}

// Helper function to convert absolute path to relative workspace path
func makeRelativeToWorkspace(absolutePath, workspacePath string) string {
	if absolutePath == "" {
		return ""
	}
	absolutePath = filepath.Clean(absolutePath)
	workspacePath = filepath.Clean(workspacePath)

	// If the file is within the workspace, make it relative
	if relativePath, found := strings.CutPrefix(absolutePath, workspacePath); found {
		relativePath = strings.TrimPrefix(relativePath, string(filepath.Separator))
		return relativePath
	}

	return absolutePath
}

// HasChanges checks if the proposed completion would introduce actual changes
func (b *NvimBuffer) HasChanges(startLine, endLineInclusive int, lines []string) bool {
	// Check the original replacement range for changes
	for i := startLine; i <= endLineInclusive; i++ {
		relativeLineIdx := i - startLine

		var l *string
		var realL *string

		if i-1 >= 0 && i-1 < len(b.lines) {
			realL = &b.lines[i-1]
		}

		if relativeLineIdx < len(lines) {
			l = &lines[relativeLineIdx]
		}

		if (l != nil && realL != nil && *l != *realL) ||
			(l != nil && realL == nil) ||
			(l == nil && realL != nil) {
			return true
		}
	}

	// Check if there are additional lines beyond the replacement range (insertions)
	if startLine+len(lines)-1 > endLineInclusive {
		return true
	}

	return false
}

// nvimBatch wraps nvim.Batch to implement the Batch interface
type nvimBatch struct {
	batch *nvim.Batch
}

func (nb *nvimBatch) Execute() error {
	if nb.batch == nil {
		return nil
	}
	return nb.batch.Execute()
}

// PrepareCompletion prepares a completion for display and returns a batch to apply it
func (b *NvimBuffer) PrepareCompletion(startLine, endLineInc int, lines []string, groups []*text.Group) Batch {
	if b.client == nil {
		return &nvimBatch{batch: nil}
	}

	diffResult := b.getDiffResult(startLine, endLineInc, lines)
	replaceEnd := computeReplaceEnd(startLine, endLineInc, lines, groups)
	cursorLine, cursorCol := text.CalculateCursorPosition(diffResult.ChangesMap(), lines)
	applyBatch := b.getApplyBatch(startLine, replaceEnd, lines, groups, cursorLine, cursorCol)

	luaDiffResult := text.ToLuaFormat(&text.Stage{
		Groups:     groups,
		Lines:      lines,
		CursorLine: cursorLine,
		CursorCol:  cursorCol,
	}, startLine)

	// Debug logging for data sent to Lua
	if jsonData, err := json.Marshal(luaDiffResult); err == nil {
		logger.Debug("sending to lua on_completion_ready:\n  startLine: %d\n  endLineInclusive: %d\n  lines: %d\n  diffResult: %s",
			startLine, endLineInc, len(lines), string(jsonData))
	}

	b.executeLuaFunction("require('cursortab').on_completion_ready(...)", luaDiffResult)

	return &nvimBatch{batch: applyBatch}
}

// CommitPending applies the pending edit to buffer state and appends
// structured diff entries showing before/after content. No-op if no pending edit.
func (b *NvimBuffer) CommitPending() {
	if b.pending == nil {
		return
	}

	startLine := b.pending.StartLine
	endLineInclusive := b.pending.EndLineInclusive
	lines := b.pending.Lines

	// Extract only the affected original lines (the range being replaced)
	var originalRangeLines []string
	for i := startLine; i <= endLineInclusive && i-1 < len(b.originalLines); i++ {
		originalRangeLines = append(originalRangeLines, b.originalLines[i-1])
	}

	// Extract granular diffs - one DiffEntry per contiguous changed region
	diffEntries := extractGranularDiffs(originalRangeLines, lines, startLine)
	stampEntries(diffEntries, types.DiffSourcePredicted, time.Now().UnixNano())
	if !b.SkipHistory() {
		b.diffHistories = appendAndCoalesce(b.diffHistories, diffEntries)
	}

	// Compute the final buffer state after applying the completion
	newLines := make([]string, 0, len(b.lines)-((endLineInclusive-startLine)+1)+len(lines))
	if startLine-1 > 0 && startLine-1 <= len(b.lines) {
		newLines = append(newLines, b.lines[:startLine-1]...)
	}
	newLines = append(newLines, lines...)
	if endLineInclusive < len(b.lines) {
		newLines = append(newLines, b.lines[endLineInclusive:]...)
	}

	// Reset checkpoint to current state for next working diff
	b.originalLines = make([]string, len(newLines))
	copy(b.originalLines, newLines)

	// Save current lines as previous state BEFORE updating (for sweep provider)
	b.previousLines = make([]string, len(b.lines))
	copy(b.previousLines, b.lines)

	// Commit the new content
	b.lines = make([]string, len(newLines))
	copy(b.lines, newLines)

	b.pending = nil
}

// CommitUserEdits extracts diffs between originalLines checkpoint and current lines,
// appends them to diffHistories, and resets the checkpoint.
// Call this when leaving insert mode to capture manual edits.
// Returns true if any changes were committed, false if no changes.
func (b *NvimBuffer) CommitUserEdits() bool {
	// Quick check: if lengths differ, there are changes
	if len(b.lines) != len(b.originalLines) {
		return b.commitUserEditsInternal()
	}

	// Check for content differences
	for i := range b.lines {
		if b.lines[i] != b.originalLines[i] {
			return b.commitUserEditsInternal()
		}
	}

	return false // No changes
}

func (b *NvimBuffer) commitUserEditsInternal() bool {
	// Extract granular diffs between checkpoint and current state
	diffEntries := extractGranularDiffs(b.originalLines, b.lines, 1)
	stampEntries(diffEntries, types.DiffSourceManual, time.Now().UnixNano())
	if len(diffEntries) == 0 {
		return false
	}

	if !b.SkipHistory() {
		b.diffHistories = appendAndCoalesce(b.diffHistories, diffEntries)
	}

	// Save checkpoint as previous state (for sweep provider)
	b.previousLines = make([]string, len(b.originalLines))
	copy(b.previousLines, b.originalLines)

	// Reset checkpoint to current state
	b.originalLines = make([]string, len(b.lines))
	copy(b.originalLines, b.lines)

	return true
}

// ShowCursorTarget displays a cursor prediction indicator at the given line
func (b *NvimBuffer) ShowCursorTarget(line int) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}
	logger.Debug("sending to lua on_cursor_prediction_ready: line=%d", line)
	b.executeLuaFunction("require('cursortab').on_cursor_prediction_ready(...)", line)
	return nil
}

// ClearUI clears the completion UI
func (b *NvimBuffer) ClearUI() error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}

	// Clear pending state to prevent stale data from being committed
	b.pending = nil

	logger.Debug("sending to lua on_reject")
	b.executeLuaFunction("require('cursortab').on_reject()")
	return nil
}

// MoveCursor moves the cursor to the start of the specified line
func (b *NvimBuffer) MoveCursor(line int, center bool, mark bool) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}

	batch := b.client.NewBatch()
	applyCursorMove(batch, line, 0, center, mark)
	batch.ExecLua("vim.cmd('normal! ^')", nil, nil) // Move cursor to start of line
	return batch.Execute()
}

// InsertText inserts text at the specified position (1-indexed line, 0-indexed col)
func (b *NvimBuffer) InsertText(line, col int, text string, keepUI bool) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}

	// Get current line content
	batch := b.client.NewBatch()
	var lines [][]byte
	batch.BufferLines(0, line-1, line, true, &lines)
	if err := batch.Execute(); err != nil {
		return err
	}

	if len(lines) == 0 {
		return nil
	}

	currentLine := string(lines[0])
	if col > len(currentLine) {
		col = len(currentLine)
	}

	// Build new line with inserted text
	newLine := currentLine[:col] + text + currentLine[col:]

	batch = b.client.NewBatch()
	if !keepUI {
		b.clearNamespace(batch, b.config.NsID)
	}
	batch.SetBufferLines(0, line-1, line, false, [][]byte{[]byte(newLine)})

	// Move cursor to end of inserted text
	newCol := col + len(text)
	applyCursorMove(batch, line, newCol, false, true)

	return batch.Execute()
}

// ReplaceLine replaces a single line (1-indexed)
func (b *NvimBuffer) ReplaceLine(line int, content string, keepUI bool) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}

	batch := b.client.NewBatch()
	if !keepUI {
		b.clearNamespace(batch, b.config.NsID)
	}
	batch.SetBufferLines(0, line-1, line, false, [][]byte{[]byte(content)})

	// Move cursor to end of line
	applyCursorMove(batch, line, len(content), false, true)

	return batch.Execute()
}

// InsertLine inserts a new line at the given position (1-indexed), pushing existing lines down
func (b *NvimBuffer) InsertLine(line int, content string, keepUI bool) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}

	batch := b.client.NewBatch()
	if !keepUI {
		b.clearNamespace(batch, b.config.NsID)
	}
	// Insert at line-1 without removing any lines (start == end)
	batch.SetBufferLines(0, line-1, line-1, false, [][]byte{[]byte(content)})
	if err := batch.Execute(); err != nil {
		return err
	}

	// Second batch: move cursor (must be after line is inserted)
	cursorBatch := b.client.NewBatch()
	applyCursorMove(cursorBatch, line, len(content), false, true)
	return cursorBatch.Execute()
}

// Diagnostics retrieves LSP diagnostics for the current buffer.
// Returns raw Neovim diagnostic data; providers handle formatting.
func (b *NvimBuffer) Diagnostics() *types.Diagnostics {
	if b.client == nil {
		return nil
	}

	batch := b.client.NewBatch()
	var hasLsp bool

	batch.ExecLua(`
		local clients = vim.lsp.get_clients and vim.lsp.get_clients({bufnr = 0}) or vim.lsp.get_active_clients({bufnr = 0})
		return #clients > 0
	`, &hasLsp, nil)

	if err := batch.Execute(); err != nil {
		logger.Error("error checking LSP availability: %v", err)
		return nil
	}

	if !hasLsp {
		return nil
	}

	batch = b.client.NewBatch()
	var rawDiags []map[string]any

	batch.ExecLua(`return vim.diagnostic.get(0)`, &rawDiags, nil)

	if err := batch.Execute(); err != nil {
		logger.Error("error getting diagnostics: %v", err)
		return nil
	}

	if len(rawDiags) == 0 {
		return nil
	}

	items := make([]*types.Diagnostic, 0, len(rawDiags))
	for _, diag := range rawDiags {
		d := &types.Diagnostic{
			Message:  getString(diag, "message"),
			Source:   getString(diag, "source"),
			Severity: types.DiagnosticSeverity(max(1, getNumber(diag, "severity"))),
		}

		if lnum := getNumber(diag, "lnum"); lnum != -1 {
			if col := getNumber(diag, "col"); col != -1 {
				endLnum := lnum
				endCol := col
				if v := getNumber(diag, "end_lnum"); v != -1 {
					endLnum = v
				}
				if v := getNumber(diag, "end_col"); v != -1 {
					endCol = v
				}
				d.Range = &types.CursorRange{
					StartLine:      lnum,
					StartCharacter: col,
					EndLine:        endLnum,
					EndCharacter:   endCol,
				}
			}
		}

		items = append(items, d)
	}

	return &types.Diagnostics{
		FilePath: b.path,
		Items:    items,
	}
}

// CursorScopes returns treesitter node types from the cursor position to the root.
func (b *NvimBuffer) CursorScopes() []string {
	if b.client == nil {
		return nil
	}

	var result []string
	batch := b.client.NewBatch()
	batch.ExecLua(
		`return require('cursortab.treesitter').cursor_scopes(vim.api.nvim_get_current_buf(), ...)`,
		&result, b.row, b.col,
	)

	if err := batch.Execute(); err != nil {
		logger.Debug("error getting cursor scopes: %v", err)
		return nil
	}

	return result
}

// TreesitterSymbols retrieves treesitter scope context around the cursor position.
// Returns nil gracefully if no treesitter parser is available for the buffer.
func (b *NvimBuffer) TreesitterSymbols(row, col, maxSiblings int) *types.TreesitterContext {
	if b.client == nil {
		return nil
	}

	var result map[string]any
	batch := b.client.NewBatch()
	batch.ExecLua(
		`return require('cursortab.treesitter').get_context(vim.api.nvim_get_current_buf(), ...)`,
		&result, row, col, maxSiblings,
	)

	if err := batch.Execute(); err != nil {
		logger.Error("error getting treesitter symbols: %v", err)
		return nil
	}

	if result == nil {
		return nil
	}

	ctx := &types.TreesitterContext{
		EnclosingSignature: getString(result, "enclosing_signature"),
	}

	// Parse siblings
	if sibs, ok := result["siblings"].([]any); ok {
		for _, s := range sibs {
			if sm, ok := s.(map[string]any); ok {
				ctx.Siblings = append(ctx.Siblings, &types.TreesitterSymbol{
					Name:      getString(sm, "name"),
					Signature: getString(sm, "signature"),
					Line:      getNumber(sm, "line"),
				})
			}
		}
	}

	// Parse imports
	if imps, ok := result["imports"].([]any); ok {
		for _, imp := range imps {
			if s, ok := imp.(string); ok {
				ctx.Imports = append(ctx.Imports, s)
			}
		}
	}

	// Parse syntax ranges (ancestor AST node line ranges, innermost to outermost)
	if ranges, ok := result["syntax_ranges"].([]any); ok {
		for _, r := range ranges {
			if rm, ok := r.(map[string]any); ok {
				ctx.SyntaxRanges = append(ctx.SyntaxRanges, &types.LineRange{
					StartLine: getNumber(rm, "start_line"),
					EndLine:   getNumber(rm, "end_line"),
				})
			}
		}
	}

	// Return nil if we got nothing useful
	if ctx.EnclosingSignature == "" && len(ctx.Siblings) == 0 && len(ctx.Imports) == 0 && len(ctx.SyntaxRanges) == 0 {
		return nil
	}

	return ctx
}

// RegisterEventHandler registers a handler for nvim RPC events. Payloads are
// queued into the mirror inbox before the handler runs so Sync sees them.
func (b *NvimBuffer) RegisterEventHandler(handler func(event string, payload map[string]any)) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}
	if err := b.client.RegisterHandler("cursortab_event", func(_ *nvim.Nvim, event string, payload map[string]any) {
		b.pushEvent(payload)
		handler(event, payload)
	}); err != nil {
		return err
	}

	b.mu.Lock()
	b.needFull = true
	b.resyncSent = true
	b.mu.Unlock()
	// Async: ExecLua replies are read by Serve, which the caller only starts
	// after this returns. A synchronous call here deadlocks the connection.
	go b.requestResync()
	return nil
}

// Internal helper methods

func (b *NvimBuffer) executeLuaFunction(luaCode string, args ...any) {
	if b.client == nil {
		return
	}
	batch := b.client.NewBatch()
	if len(args) > 0 {
		batch.ExecLua(luaCode, nil, args...)
	} else {
		batch.ExecLua(luaCode, nil, nil)
	}
	if err := batch.Execute(); err != nil {
		logger.Error("error executing lua function: %v", err)
	}
}

func applyCursorMove(batch *nvim.Batch, line, col int, center bool, mark bool) {
	if mark {
		// Use vim.fn.setpos to set the ' mark without triggering mode changes
		// (normal! m' would exit insert mode and cause ModeChanged events)
		// The mark name "''" means: ' prefix for marks + ' as the mark name
		batch.ExecLua("vim.fn.setpos(\"''\", vim.fn.getpos('.'))", nil, nil)
	}
	batch.SetWindowCursor(0, [2]int{line, col})
	if center {
		batch.ExecLua("vim.cmd('normal! zz')", nil, nil)
	}
}

func (b *NvimBuffer) getDiffResult(startLine, endLineInclusive int, lines []string) *text.DiffResult {
	originalLines := []string{}
	for i := startLine; i <= endLineInclusive && i-1 < len(b.lines); i++ {
		originalLines = append(originalLines, b.lines[i-1])
	}
	oldText := text.JoinLines(originalLines)
	newText := text.JoinLines(lines)
	return text.ComputeDiff(oldText, newText)
}

// isPureInsertion returns true if all groups are additions (no modifications or deletions).
// Pure insertion stages insert content without replacing existing lines.
func isPureInsertion(groups []*text.Group) bool {
	if len(groups) == 0 {
		return false
	}
	for _, g := range groups {
		if g.Type != "addition" {
			return false
		}
	}
	return true
}

// computeReplaceEnd returns the end line for nvim_buf_set_lines. For pure
// insertions (all addition groups whose total line count matches the stage
// lines, on a single old line), it returns startLine-1 so nvim inserts
// without replacing. Otherwise it returns endLineInc to replace the old
// line range.
func computeReplaceEnd(startLine, endLineInc int, lines []string, groups []*text.Group) int {
	if isPureInsertion(groups) && startLine == endLineInc {
		// Verify that all lines are accounted for by addition groups.
		// When the stage absorbs unchanged old lines between additions,
		// len(lines) exceeds the group line count and it's a replacement.
		groupLines := 0
		for _, g := range groups {
			groupLines += g.EndLine - g.StartLine + 1
		}
		if len(lines) == groupLines {
			return startLine - 1
		}
	}
	return endLineInc
}

type bufferTextEdit struct {
	row         int
	startCol    int
	endCol      int
	replacement []byte
}

func textEditForCharGroup(group *text.Group) (bufferTextEdit, bool) {
	if group == nil || group.StartLine != group.EndLine || group.BufferLine <= 0 {
		return bufferTextEdit{}, false
	}
	switch group.RenderHint {
	case "append_chars", "replace_chars", "delete_chars":
	default:
		return bufferTextEdit{}, false
	}
	if len(group.Lines) != 1 || len(group.OldLines) != 1 {
		return bufferTextEdit{}, false
	}

	oldLine := group.OldLines[0]
	newLine := group.Lines[0]
	if oldLine == newLine {
		return bufferTextEdit{}, false
	}

	startCol, oldEnd, newEnd := text.ChangedByteSpan(oldLine, newLine)
	if oldEnd < startCol || newEnd < startCol {
		return bufferTextEdit{}, false
	}

	return bufferTextEdit{
		row:         group.BufferLine - 1,
		startCol:    startCol,
		endCol:      oldEnd,
		replacement: []byte(newLine[startCol:newEnd]),
	}, true
}

func charLevelTextEdits(groups []*text.Group) ([]bufferTextEdit, bool) {
	if len(groups) == 0 {
		return nil, false
	}

	edits := make([]bufferTextEdit, 0, len(groups))
	for _, group := range groups {
		edit, ok := textEditForCharGroup(group)
		if !ok {
			return nil, false
		}
		edits = append(edits, edit)
	}

	slices.SortFunc(edits, func(a, b bufferTextEdit) int {
		if a.row != b.row {
			return b.row - a.row
		}
		return a.startCol - b.startCol
	})

	return edits, true
}

func setBufferLines(batch *nvim.Batch, buf nvim.Buffer, startLine, replaceEnd int, lines []string) {
	placeBytes := make([][]byte, len(lines))
	for i, line := range lines {
		placeBytes[i] = []byte(line)
	}

	// nvim_buf_set_lines uses 0-indexed [start, end) range.
	// Replacement: 1-indexed inclusive [startLine, replaceEnd] maps to
	// 0-indexed exclusive [startLine-1, replaceEnd) by indexing coincidence.
	// Pure insertion: replaceEnd = startLine-1, so start == end inserts without replacing.
	batch.SetBufferLines(buf, startLine-1, replaceEnd, false, placeBytes)
}

func (b *NvimBuffer) getApplyBatch(startLine, replaceEnd int, lines []string, groups []*text.Group, cursorLine, cursorCol int) *nvim.Batch {
	applyBatch := b.client.NewBatch()

	b.clearNamespace(applyBatch, b.config.NsID)

	if edits, ok := charLevelTextEdits(groups); ok {
		for _, edit := range edits {
			applyBatch.SetBufferText(0, edit.row, edit.startCol, edit.row, edit.endCol, [][]byte{edit.replacement})
		}
	} else {
		setBufferLines(applyBatch, 0, startLine, replaceEnd, lines)
	}

	b.pending = &PendingEdit{
		StartLine:        startLine,
		EndLineInclusive: replaceEnd,
		Lines:            slices.Clone(lines),
	}

	if cursorLine >= 0 && cursorCol >= 0 {
		bufferLine := startLine + cursorLine - 1
		applyCursorMove(applyBatch, bufferLine, cursorCol, false, true)
	}

	return applyBatch
}

func (b *NvimBuffer) clearNamespace(batch *nvim.Batch, nsID int) {
	batch.ClearBufferNamespace(0, nsID, 0, -1)
}

// extractGranularDiffs analyzes old and new lines and returns DiffEntry records
// for each contiguous region that changed. baseLine is the 1-indexed buffer line
// where the old content starts. Returned entries have StartLine set but no
// Source or TimestampNs, callers stamp those via stampEntries.
func extractGranularDiffs(oldLines, newLines []string, baseLine int) []*types.DiffEntry {
	oldText := text.JoinLines(oldLines)
	newText := text.JoinLines(newLines)

	if oldText == newText {
		return nil
	}

	dmp := diffmatchpatch.New()
	chars1, chars2, lineArray := dmp.DiffLinesToChars(oldText, newText)
	diffs := dmp.DiffMain(chars1, chars2, false)
	lineDiffs := dmp.DiffCharsToLines(diffs, lineArray)

	var entries []*types.DiffEntry
	currentLine := baseLine

	for i := 0; i < len(lineDiffs); i++ {
		diff := lineDiffs[i]
		lineCount := strings.Count(diff.Text, "\n")

		switch diff.Type {
		case diffmatchpatch.DiffEqual:
			currentLine += lineCount
			continue

		case diffmatchpatch.DiffDelete:
			startLine := currentLine
			deletedText := strings.TrimSuffix(diff.Text, "\n")
			insertedText := ""

			// Check if followed by an insert (modification pattern)
			if i+1 < len(lineDiffs) && lineDiffs[i+1].Type == diffmatchpatch.DiffInsert {
				insertedText = strings.TrimSuffix(lineDiffs[i+1].Text, "\n")
				i++ // Skip the insert in next iteration
			}

			entries = append(entries, &types.DiffEntry{
				Original:  deletedText,
				Updated:   insertedText,
				StartLine: startLine,
			})
			currentLine += lineCount

		case diffmatchpatch.DiffInsert:
			insertedText := strings.TrimSuffix(diff.Text, "\n")
			entries = append(entries, &types.DiffEntry{
				Original:  "",
				Updated:   insertedText,
				StartLine: currentLine,
			})
		}
	}

	return entries
}

// stampEntries sets Source and TimestampNs on all entries.
func stampEntries(entries []*types.DiffEntry, source types.DiffSource, timestampNs int64) {
	for _, e := range entries {
		e.Source = source
		e.TimestampNs = timestampNs
	}
}

// Helper function to safely get string from map
func getString(m map[string]any, key string) string {
	if val, ok := m[key].(string); ok {
		return val
	}
	return ""
}

// payloadInt reads a number from an event payload, reporting whether the key
// was present at all.
func payloadInt(payload map[string]any, key string) (int, bool) {
	if _, ok := payload[key]; !ok {
		return 0, false
	}
	return getNumber(payload, key), true
}

// decodeLines reads a msgpack string array as the go-client decodes it into
// generic values.
func decodeLines(v any) ([]string, bool) {
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	lines := make([]string, len(items))
	for i, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		lines[i] = s
	}
	return lines, true
}

// Helper function to safely get number from map, handling common msgpack number types
func getNumber(m map[string]any, key string) int {
	if val, ok := m[key].(int); ok {
		return val
	}
	if val, ok := m[key].(float64); ok {
		return int(val)
	}
	if val, ok := m[key].(int32); ok {
		return int(val)
	}
	if val, ok := m[key].(int64); ok {
		return int(val)
	}
	if val, ok := m[key].(uint64); ok {
		return int(val)
	}
	return -1
}
