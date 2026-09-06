package ctx

import (
	"context"
	"slices"
	"strings"

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

type UserActions struct {
	Actions []*types.UserAction
}

func (UserActions) collect(_ context.Context, input ContextSourceInput) (material, error) {
	var result UserActions
	for _, action := range input.Snapshot.UserActions {
		if action == nil || action.FilePath != input.Current.File.Path {
			continue
		}
		clone := *action
		result.Actions = append(result.Actions, &clone)
	}
	if input.Limits.MaxUserActions > 0 && len(result.Actions) > input.Limits.MaxUserActions {
		result.Actions = result.Actions[len(result.Actions)-input.Limits.MaxUserActions:]
	}
	return result, nil
}
