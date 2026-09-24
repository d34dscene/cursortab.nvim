package ctx

import (
	"bufio"
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"cursortab/buffer"
	"cursortab/logger"
	"cursortab/types"
	"cursortab/utils"
)

type Diagnostics struct {
	Data *types.Diagnostics
}

// Per-item cost covers severity, range, and separator formatting overhead.
const diagnosticItemOverheadBytes = 32

func (Diagnostics) collect(_ context.Context, input ContextSourceInput) (material, error) {
	data := input.Buffer.Diagnostics()
	if data == nil || len(data.Items) == 0 {
		return Diagnostics{Data: data}, nil
	}
	if input.Budget == nil {
		return Diagnostics{Data: data}, nil
	}

	kept := 0
	for kept < len(data.Items) {
		item := data.Items[kept]
		if item == nil {
			kept++
			continue
		}
		cost := len(item.Message) + len(item.Source) + diagnosticItemOverheadBytes
		if cost > input.Budget.Remaining() {
			break
		}
		input.Budget.Take(cost)
		kept++
	}
	if dropped := len(data.Items) - kept; dropped > 0 {
		logger.Debug("context: diagnostics budget dropped %d of %d items", dropped, len(data.Items))
	}
	if kept == 0 {
		return Diagnostics{}, nil
	}
	return Diagnostics{Data: &types.Diagnostics{FilePath: data.FilePath, Items: data.Items[:kept]}}, nil
}

type Treesitter struct {
	Data *types.TreesitterContext
}

func (Treesitter) collect(_ context.Context, input ContextSourceInput) (material, error) {
	return Treesitter{Data: input.Buffer.TreesitterSymbols(input.Current.Cursor.Row, input.Current.Cursor.Col, input.Limits.MaxSiblings)}, nil
}

type GitDiff struct {
	Data *types.GitDiffContext
}

func (GitDiff) collect(ctx context.Context, input ContextSourceInput) (material, error) {
	result := GitDiff{}
	if !strings.HasSuffix(input.Current.File.Path, "COMMIT_EDITMSG") {
		return result, nil
	}
	workDir := input.Current.WorkspacePath
	if workDir == "" {
		return result, nil
	}

	fullDiff := runGit(ctx, workDir, "diff", "--cached")
	if fullDiff == "" {
		return result, nil
	}
	if len(fullDiff) <= input.Limits.MaxDiffBytes {
		result.Data = &types.GitDiffContext{Diff: fullDiff}
		return result, nil
	}

	minimalDiff := runGit(ctx, workDir, "diff", "--cached", "-U0")
	if minimalDiff == "" {
		return result, nil
	}
	symbols := extractChangedSymbols(minimalDiff, input.Limits.MaxChangedSymbols)
	if len(symbols) == 0 {
		return result, nil
	}
	result.Data = &types.GitDiffContext{Diff: strings.Join(symbols, "\n")}
	return result, nil
}

// gitTimeout bounds one git invocation so staged-diff collection never stalls
// a completion.
const gitTimeout = 200 * time.Millisecond

// runGit executes a git command bounded by gitTimeout and returns its stdout,
// or "" on failure.
func runGit(ctx context.Context, dir string, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		logger.Debug("gitdiff: git %s failed: %v", args[0], err)
		return ""
	}
	return string(out)
}

// extractChangedSymbols parses a unified diff (-U0) and extracts function/type
// signatures from added/removed declaration lines in git diff format.
func extractChangedSymbols(diff string, maxSymbols int) []string {
	if diff == "" {
		return nil
	}

	seen := make(map[string]struct{})
	var symbols []string

	scanner := bufio.NewScanner(strings.NewReader(diff))

	for scanner.Scan() {
		line := scanner.Text()

		// Skip metadata lines
		if strings.HasPrefix(line, "diff --git ") ||
			strings.HasPrefix(line, "---") ||
			strings.HasPrefix(line, "+++") ||
			strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "@@") {
			continue
		}

		// Extract added declaration lines
		if strings.HasPrefix(line, "+") {
			content := strings.TrimSpace(line[1:])
			if isDeclarationLine(content) {
				sym := "+" + content
				if _, ok := seen[sym]; !ok && len(symbols) < maxSymbols {
					seen[sym] = struct{}{}
					symbols = append(symbols, sym)
				}
			}
			continue
		}

		// Extract removed declaration lines
		if strings.HasPrefix(line, "-") {
			content := strings.TrimSpace(line[1:])
			if isDeclarationLine(content) {
				sym := "-" + content
				if _, ok := seen[sym]; !ok && len(symbols) < maxSymbols {
					seen[sym] = struct{}{}
					symbols = append(symbols, sym)
				}
			}
		}
	}

	return symbols
}

// isDeclarationLine checks if a line looks like a function/type/class declaration
// across common languages (Go, Python, Rust, JS/TS, C/C++, Java).
func isDeclarationLine(line string) bool {
	prefixes := []string{
		"func ", "func(", // Go
		"def ",                     // Python
		"class ",                   // Python, JS/TS, Java, C++
		"type ",                    // Go, TS
		"struct ",                  // Go, Rust, C/C++
		"fn ",                      // Rust
		"impl ",                    // Rust
		"trait ",                   // Rust
		"enum ",                    // Rust, Java, TS
		"interface ",               // Go, TS, Java
		"export function ",         // JS/TS
		"export default function ", // JS/TS
		"export const ",            // JS/TS
		"export class ",            // JS/TS
		"async function ",          // JS/TS
		"export async function ",   // JS/TS
		"public ",                  // Java, C#
		"private ",                 // Java, C#
		"protected ",               // Java, C#
		"static ",                  // Java, C/C++
	}

	for _, prefix := range prefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}

	return false
}

type RecentFiles struct {
	Files []*types.RecentBufferSnapshot
}

func (RecentFiles) collect(_ context.Context, input ContextSourceInput) (material, error) {
	result := RecentFiles{}
	for i, file := range input.Snapshot.RecentFiles {
		if input.Limits.MaxRecentSnapshots > 0 && len(result.Files) >= input.Limits.MaxRecentSnapshots {
			break
		}
		if len(file.FirstLines) == 0 {
			continue
		}
		maxBytes := input.Limits.MaxRecentFileBytes
		if input.Budget != nil {
			remaining := input.Budget.Remaining()
			if remaining <= 0 {
				dropped := 0
				for _, f := range input.Snapshot.RecentFiles[i:] {
					if len(f.FirstLines) > 0 {
						dropped++
					}
				}
				if dropped > 0 {
					logger.Debug("context: recent files budget dropped %d files", dropped)
				}
				break
			}
			if maxBytes <= 0 || maxBytes > remaining {
				maxBytes = remaining
			}
		}
		lines := truncateLinesByBytes(file.FirstLines, maxBytes)
		used := len(file.Path) + 1
		for _, line := range lines {
			used += len(line) + 1
		}
		if input.Budget != nil {
			if used > input.Budget.Remaining() {
				logger.Debug("context: recent files budget dropped %s", file.Path)
				break
			}
			input.Budget.Take(used)
		}
		result.Files = append(result.Files, &types.RecentBufferSnapshot{
			FilePath:    file.Path,
			Lines:       lines,
			TimestampMs: file.LastAccessNs / 1e6,
		})
	}
	return result, nil
}

// truncateLinesByBytes keeps the longest prefix of lines whose bytes
// (line lengths plus newlines) fit within maxBytes. A single oversized line
// is cut to maxBytes. maxBytes <= 0 disables truncation.
func truncateLinesByBytes(lines []string, maxBytes int) []string {
	if maxBytes <= 0 || len(lines) == 0 {
		return slices.Clone(lines)
	}
	total := 0
	end := 0
	for end < len(lines) {
		cost := len(lines[end]) + 1
		if total+cost > maxBytes {
			break
		}
		total += cost
		end++
	}
	if end == 0 {
		cut := max(
			// leave room for the newline
			maxBytes-1, 0)
		return []string{lines[0][:cut]}
	}
	return slices.Clone(lines[:end])
}

type EditHistory struct {
	Files []*types.FileDiffHistory
}

func (EditHistory) collect(_ context.Context, input ContextSourceInput) (material, error) {
	var result EditHistory
	appendFile := func(path string, diffs []*types.DiffEntry) {
		if len(diffs) == 0 {
			return
		}
		used := len(path) + 1
		for _, diff := range diffs {
			used += len(diff.Original) + len(diff.Updated)
		}
		if input.Budget != nil && used > input.Budget.Remaining() {
			logger.Debug("context: edit history budget dropped diffs for %s", path)
			return
		}
		if input.Budget != nil {
			input.Budget.Take(used)
		}
		result.Files = append(result.Files, &types.FileDiffHistory{FileName: path, DiffHistory: diffs})
	}

	for _, file := range input.Snapshot.RecentFiles {
		appendFile(file.Path, budgetedDiffEntries(
			buffer.ProcessDiffHistory(file.DiffHistories, input.Snapshot.NowNs),
			file.Path,
			input,
		))
	}

	if input.Current.File.Path != "" {
		appendFile(input.Current.File.Path, budgetedDiffEntries(
			buffer.ProcessDiffHistory(input.Snapshot.CurrentDiffHistories, input.Snapshot.NowNs),
			input.Current.File.Path,
			input,
		))
	}
	return result, nil
}

// budgetedDiffEntries trims diff entries against the per-request token cap
// and the shared byte budget. The budget is global across all files: each
// file keeps its newest entries from whatever budget the previous files left.
func budgetedDiffEntries(diffs []*types.DiffEntry, path string, input ContextSourceInput) []*types.DiffEntry {
	if len(diffs) == 0 {
		return nil
	}
	if input.Budget == nil {
		if input.Limits.MaxDiffTokens > 0 {
			return utils.TrimDiffEntries(diffs, input.Limits.MaxDiffTokens)
		}
		return diffs
	}

	remaining := input.Budget.Remaining()
	if remaining <= 0 {
		return nil
	}
	newest := diffs[len(diffs)-1]
	if len(newest.Original)+len(newest.Updated) > remaining {
		logger.Debug("context: edit history budget dropped all diffs for %s", path)
		return nil
	}
	tokenCap := remaining / utils.AvgCharsPerToken
	if tokenCap <= 0 {
		return nil
	}
	if input.Limits.MaxDiffTokens > 0 {
		tokenCap = min(tokenCap, input.Limits.MaxDiffTokens)
	}
	return utils.TrimDiffEntries(diffs, tokenCap)
}

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
