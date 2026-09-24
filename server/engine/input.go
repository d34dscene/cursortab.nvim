package engine

import (
	"slices"

	"cursortab/ctx"
	"cursortab/types"
)

func (e *Engine) buildContextSourceInput(requirements ctx.Materials, materialsBudgetChars int) ctx.ContextSourceInput {
	current := e.buildCurrentSnapshot()
	snapshot := e.buildFileContextSnapshot(requirements)
	return ctx.ContextSourceInput{
		Current:  current,
		Snapshot: snapshot,
		Buffer:   e.buffer,
		Limits: ctx.CollectionLimits{
			MaxSiblings:        defaultMaxSiblings,
			MaxDiffBytes:       defaultMaxDiffBytes,
			MaxChangedSymbols:  defaultMaxChangedSymbols,
			MaxRecentSnapshots: defaultMaxRecentSnapshots,
			MaxRecentFileBytes: defaultMaxRecentFileBytes,
			MaxDiffTokens:      e.config.MaxDiffTokens,
			MaxRetrievalChunks: e.config.MaxRetrievalChunks,
			ContextChars:       materialsBudgetChars,
		},
		Retriever: e.retriever,
	}
}

func (e *Engine) buildCurrentSnapshot() ctx.CurrentSnapshot {
	return ctx.CurrentSnapshot{
		WorkspacePath: e.WorkspacePath,
		File: ctx.FileSnapshot{
			Path:  e.buffer.Path(),
			Lines: slices.Clone(e.buffer.Lines()),
		},
		Cursor: ctx.CursorPosition{
			Row: e.buffer.Row(),
			Col: e.buffer.Col(),
		},
		ViewportHeight: e.getViewportHeightConstraint(),
	}
}

func (e *Engine) buildFileContextSnapshot(requirements ctx.Materials) ctx.FileContextSnapshot {
	needs := requirements.FileContextNeeds()
	if !needs.RecentFileLines && !needs.RecentFileDiffHistories && !needs.CurrentDiffHistories {
		return ctx.FileContextSnapshot{}
	}

	currentPath := e.buffer.Path()

	var recentFiles []ctx.RecentFileSnapshot
	if needs.RecentFileLines || needs.RecentFileDiffHistories {
		recentFiles = make([]ctx.RecentFileSnapshot, 0, len(e.fileStateStore))
		for _, entry := range e.fileStatesByRecency(func(path string, _ *FileState) bool {
			return path != currentPath
		}) {
			recent := ctx.RecentFileSnapshot{
				Path:         entry.path,
				LastAccessNs: entry.state.LastAccessNs,
			}
			if needs.RecentFileLines {
				recent.FirstLines = slices.Clone(entry.state.FirstLines)
			}
			if needs.RecentFileDiffHistories {
				recent.DiffHistories = clonePtrSlice(entry.state.DiffHistories)
			}
			recentFiles = append(recentFiles, recent)
		}
	}

	var currentDiffHistories []*types.DiffEntry
	if needs.CurrentDiffHistories {
		currentDiffHistories = clonePtrSlice(e.buffer.DiffHistories())
	}

	return ctx.FileContextSnapshot{
		CurrentDiffHistories: currentDiffHistories,
		RecentFiles:          recentFiles,
		NowNs:                e.clock.Now().UnixNano(),
	}
}

// getViewportHeightConstraint limits the request window to what is visible
// when cursor prediction is off.
func (e *Engine) getViewportHeightConstraint() int {
	if e.config.CursorPrediction.Enabled {
		return 0
	}
	_, viewportBottom := e.buffer.ViewportBounds()
	if viewportBottom > 0 && e.buffer.Row() > 0 {
		// +1 because both cursor and viewport bottom are inclusive (cursor on
		// last visible line means 1 visible line remaining, not 0).
		if constraint := viewportBottom - e.buffer.Row() + 1; constraint > 0 {
			return constraint
		}
	}
	return 0
}

func clonePtrSlice[T any](items []*T) []*T {
	if items == nil {
		return nil
	}
	cloned := make([]*T, len(items))
	for i, item := range items {
		if item == nil {
			continue
		}
		clone := *item
		cloned[i] = &clone
	}
	return cloned
}
