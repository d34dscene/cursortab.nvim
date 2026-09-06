package ctx

import (
	"context"
	"strings"
	"testing"
	"time"

	"cursortab/assert"
	"cursortab/types"
)

type materialBuffer struct {
	diagnostics *types.Diagnostics
	treesitter  *types.TreesitterContext
	row         int
	col         int
	maxSiblings int
}

func (b *materialBuffer) Diagnostics() *types.Diagnostics {
	return b.diagnostics
}

func (b *materialBuffer) TreesitterSymbols(row int, col int, maxSiblings int) *types.TreesitterContext {
	b.row = row
	b.col = col
	b.maxSiblings = maxSiblings
	return b.treesitter
}

func TestFileContextNeedsReportsMaterialInputs(t *testing.T) {
	needs := Materials{RecentFiles{}, EditHistory{}, UserActions{}}.FileContextNeeds()

	assert.True(t, needs.RecentFileLines, "recent file lines")
	assert.True(t, needs.RecentFileDiffHistories, "recent file diff histories")
	assert.True(t, needs.CurrentDiffHistories, "current diff histories")
	assert.True(t, needs.UserActions, "user actions")
}

func TestDiagnosticsAndTreesitterCollectFromBuffer(t *testing.T) {
	diagnostics := &types.Diagnostics{FilePath: "main.go"}
	treesitter := &types.TreesitterContext{EnclosingSignature: "func main()"}
	buffer := &materialBuffer{diagnostics: diagnostics, treesitter: treesitter}
	input := ContextSourceInput{
		Current: CurrentSnapshot{
			Cursor: CursorPosition{Row: 7, Col: 11},
		},
		Buffer: buffer,
		Limits: CollectionLimits{MaxSiblings: 4},
	}

	diagnosticsMaterial, err := Diagnostics{}.collect(context.Background(), input)
	assert.NoError(t, err, "diagnostics collect")
	assert.Equal(t, diagnostics, diagnosticsMaterial.(Diagnostics).Data, "diagnostics data")

	treesitterMaterial, err := Treesitter{}.collect(context.Background(), input)
	assert.NoError(t, err, "treesitter collect")
	assert.Equal(t, treesitter, treesitterMaterial.(Treesitter).Data, "treesitter data")
	assert.Equal(t, 7, buffer.row, "treesitter row")
	assert.Equal(t, 11, buffer.col, "treesitter col")
	assert.Equal(t, 4, buffer.maxSiblings, "treesitter max siblings")
}

func TestUserActionsCollectFiltersCurrentFileAppliesLimitAndClones(t *testing.T) {
	actions := []*types.UserAction{
		{FilePath: "current.go", Offset: 1},
		{FilePath: "other.go", Offset: 10},
		{FilePath: "current.go", Offset: 2},
		{FilePath: "current.go", Offset: 3},
	}
	input := ContextSourceInput{
		Current:  CurrentSnapshot{File: FileSnapshot{Path: "current.go"}},
		Snapshot: FileContextSnapshot{UserActions: actions},
		Limits:   CollectionLimits{MaxUserActions: 2},
	}

	material, err := UserActions{}.collect(context.Background(), input)
	assert.NoError(t, err, "user actions collect")
	result := material.(UserActions)

	assert.Len(t, 2, result.Actions, "filtered actions")
	assert.Equal(t, 2, result.Actions[0].Offset, "first retained action")
	assert.Equal(t, 3, result.Actions[1].Offset, "second retained action")

	actions[2].Offset = 99
	assert.Equal(t, 2, result.Actions[0].Offset, "actions are cloned")
}

func TestRecentFilesCollectTruncatesOversizedSnapshots(t *testing.T) {
	input := ContextSourceInput{
		Snapshot: FileContextSnapshot{
			RecentFiles: []RecentFileSnapshot{
				{
					Path: "minified.js",
					FirstLines: []string{
						strings.Repeat("x", 100_000),
						"second line",
					},
				},
				{
					Path:       "normal.go",
					FirstLines: []string{"package main", "func main() {}"},
				},
			},
		},
		Limits: CollectionLimits{MaxRecentFileBytes: 4096},
	}

	material, err := RecentFiles{}.collect(context.Background(), input)
	assert.NoError(t, err, "recent files collect")
	result := material.(RecentFiles)

	assert.Len(t, 2, result.Files, "snapshot count")
	total := 0
	for _, line := range result.Files[0].Lines {
		total += len(line) + 1
	}
	assert.True(t, total <= 4096, "oversized snapshot truncated to budget")
	assert.Equal(t, []string{"package main", "func main() {}"}, result.Files[1].Lines, "small snapshot untouched")
}

func TestRecentFilesCollectRespectsSharedBudget(t *testing.T) {
	line := strings.Repeat("x", 999)
	file := func(path string) RecentFileSnapshot {
		lines := make([]string, 3)
		for i := range lines {
			lines[i] = line
		}
		return RecentFileSnapshot{Path: path, FirstLines: lines}
	}
	input := ContextSourceInput{
		Snapshot: FileContextSnapshot{
			RecentFiles: []RecentFileSnapshot{file("newest.go"), file("middle.go"), file("oldest.go")},
		},
		Limits: CollectionLimits{MaxRecentFileBytes: 4096, ContextChars: 4096},
		Budget: NewBudget(4096),
	}

	material, err := RecentFiles{}.collect(context.Background(), input)
	assert.NoError(t, err, "recent files collect")
	result := material.(RecentFiles)

	assert.Equal(t, "newest.go", result.Files[0].FilePath, "most recent file first")
	used := 0
	for _, snap := range result.Files {
		used += len(snap.FilePath) + 1
		for _, l := range snap.Lines {
			used += len(l) + 1
		}
	}
	assert.True(t, used <= 4096, "collected files stay within budget")
	assert.Equal(t, used, input.Budget.Used(), "budget tracks consumed bytes")
	assert.True(t, len(result.Files) < 3, "budget drops files that no longer fit")
}

func TestEditHistoryBudgetIsGlobalAcrossFiles(t *testing.T) {
	now := time.Now().UnixNano()
	diff := func(original, updated string) *types.DiffEntry {
		return &types.DiffEntry{
			Original:    original,
			Updated:     updated,
			Source:      types.DiffSourceManual,
			TimestampNs: now,
			StartLine:   1,
		}
	}
	input := ContextSourceInput{
		Current: CurrentSnapshot{File: FileSnapshot{Path: "current.go"}},
		Snapshot: FileContextSnapshot{
			RecentFiles: []RecentFileSnapshot{
				// Engine order: most recent file first.
				{Path: "newer.go", DiffHistories: []*types.DiffEntry{diff(strings.Repeat("b", 1200), strings.Repeat("B", 1200))}},
				{Path: "older.go", DiffHistories: []*types.DiffEntry{diff(strings.Repeat("a", 1200), strings.Repeat("A", 1200))}},
			},
			CurrentDiffHistories: []*types.DiffEntry{diff("small", "small changed")},
			NowNs:                now,
		},
		Limits: CollectionLimits{ContextChars: 4096},
		Budget: NewBudget(4096),
	}

	material, err := EditHistory{}.collect(context.Background(), input)
	assert.NoError(t, err, "edit history collect")
	result := material.(EditHistory)

	// The newest file consumes most of the budget, the older one is dropped,
	// and the current file's small diffs still fit what is left.
	assert.Len(t, 2, result.Files, "budget drops files that no longer fit")
	assert.Equal(t, "newer.go", result.Files[0].FileName, "newest cross-file kept")
	assert.Equal(t, "current.go", result.Files[1].FileName, "current file diffs survive")
	assert.True(t, input.Budget.Used() <= 4096, "total stays within budget")
}

func TestEditHistoryBudgetDropsFilesWhoseNewestDiffCannotFit(t *testing.T) {
	now := time.Now().UnixNano()
	huge := &types.DiffEntry{
		Original:    strings.Repeat("a", 10_000),
		Updated:     strings.Repeat("b", 10_000),
		Source:      types.DiffSourceManual,
		TimestampNs: now,
		StartLine:   1,
	}
	input := ContextSourceInput{
		Current: CurrentSnapshot{File: FileSnapshot{Path: "current.go"}},
		Snapshot: FileContextSnapshot{
			RecentFiles: []RecentFileSnapshot{
				{Path: "big.go", DiffHistories: []*types.DiffEntry{huge}},
			},
			NowNs: now,
		},
		Limits: CollectionLimits{ContextChars: 100},
		Budget: NewBudget(100),
	}

	material, err := EditHistory{}.collect(context.Background(), input)
	assert.NoError(t, err, "edit history collect")
	assert.Len(t, 0, material.(EditHistory).Files, "oversized diff dropped entirely")
}

func TestDiagnosticsCollectTrimsToBudget(t *testing.T) {
	items := make([]*types.Diagnostic, 5)
	for i := range items {
		items[i] = &types.Diagnostic{Message: strings.Repeat("m", 100), Source: "gopls"}
	}
	input := ContextSourceInput{
		Buffer: &materialBuffer{diagnostics: &types.Diagnostics{FilePath: "main.go", Items: items}},
		Limits: CollectionLimits{ContextChars: 300},
		Budget: NewBudget(300),
	}

	material, err := Diagnostics{}.collect(context.Background(), input)
	assert.NoError(t, err, "diagnostics collect")
	result := material.(Diagnostics)

	assert.True(t, len(result.Data.Items) < 5, "diagnostics trimmed to budget")
	assert.True(t, input.Budget.Used() <= 300, "total stays within budget")
}

func TestCollectReportsContextChars(t *testing.T) {
	file := RecentFileSnapshot{Path: "a.go", FirstLines: []string{"package main"}}
	input := ContextSourceInput{
		Snapshot: FileContextSnapshot{RecentFiles: []RecentFileSnapshot{file}},
		Limits:   CollectionLimits{ContextChars: 4096},
	}

	_, used, err := Collect(context.Background(), input, Materials{RecentFiles{}})
	assert.NoError(t, err, "collect")
	assert.Equal(t, len("a.go")+1+len("package main")+1, used, "used chars reported")
}

func TestCollectWithoutBudgetLimitCollectsUnbounded(t *testing.T) {
	file := RecentFileSnapshot{Path: "a.go", FirstLines: []string{"package main"}}
	input := ContextSourceInput{
		Snapshot: FileContextSnapshot{RecentFiles: []RecentFileSnapshot{file}},
		Limits:   CollectionLimits{ContextChars: -1},
	}

	materials, used, err := Collect(context.Background(), input, Materials{RecentFiles{}})
	assert.NoError(t, err, "collect")
	assert.Equal(t, 0, used, "no budget means no usage tracking")
	recent, ok := Find[RecentFiles](materials)
	assert.True(t, ok, "recent files material")
	assert.Len(t, 1, recent.Files, "file collected without budget")
}

func TestEditHistoryCollectTrimsRecentFileDiffs(t *testing.T) {
	now := time.Now().UnixNano()
	diff := func(original, updated string) *types.DiffEntry {
		return &types.DiffEntry{
			Original:    original,
			Updated:     updated,
			Source:      types.DiffSourceManual,
			TimestampNs: now,
			StartLine:   1,
		}
	}
	big := &types.DiffEntry{
		Original:    strings.Repeat("a", 10_000),
		Updated:     strings.Repeat("b", 10_000),
		Source:      types.DiffSourceManual,
		TimestampNs: now,
		StartLine:   1,
	}
	input := ContextSourceInput{
		Current: CurrentSnapshot{File: FileSnapshot{Path: "current.go"}},
		Snapshot: FileContextSnapshot{
			RecentFiles: []RecentFileSnapshot{
				{Path: "old.go", DiffHistories: []*types.DiffEntry{big, diff("small", "small changed")}},
			},
			NowNs: now,
		},
		Limits: CollectionLimits{MaxDiffTokens: 64},
	}

	material, err := EditHistory{}.collect(context.Background(), input)
	assert.NoError(t, err, "edit history collect")
	result := material.(EditHistory)

	assert.Len(t, 1, result.Files, "edit history files")
	assert.Len(t, 1, result.Files[0].DiffHistory, "oversized diff dropped, newest kept")
	assert.Equal(t, "small", result.Files[0].DiffHistory[0].Original, "kept entry is the small one")
}

func TestEditHistoryCollectsCrossFileBeforeCurrent(t *testing.T) {
	now := time.Now().UnixNano()
	diff := func(original, updated string) *types.DiffEntry {
		return &types.DiffEntry{
			Original:    original,
			Updated:     updated,
			Source:      types.DiffSourceManual,
			TimestampNs: now,
			StartLine:   1,
		}
	}
	input := ContextSourceInput{
		Current: CurrentSnapshot{File: FileSnapshot{Path: "current.go"}},
		Snapshot: FileContextSnapshot{
			// Engine order: most recent file first.
			RecentFiles: []RecentFileSnapshot{
				{Path: "new.go", DiffHistories: []*types.DiffEntry{diff("new", "new changed")}},
				{Path: "old.go", DiffHistories: []*types.DiffEntry{diff("old", "old changed")}},
			},
			CurrentDiffHistories: []*types.DiffEntry{diff("current", "current changed")},
			NowNs:                now,
		},
	}

	material, err := EditHistory{}.collect(context.Background(), input)
	assert.NoError(t, err, "edit history collect")
	result := material.(EditHistory)

	assert.Len(t, 3, result.Files, "edit history files")
	assert.Equal(t, "new.go", result.Files[0].FileName, "most recent cross-file first")
	assert.Equal(t, "old.go", result.Files[1].FileName, "older cross-file second")
	assert.Equal(t, "current.go", result.Files[2].FileName, "current file last")
}
