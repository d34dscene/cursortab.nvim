package provider

import (
	"fmt"
	"strings"

	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/types"
)

const (
	broadContextLinesBefore = 150
	broadContextLinesAfter  = 150
)

var sweepStopTokens = []string{"<|file_sep|>", "<|endoftext|>"}

// buildSweep constructs the Sweep next-edit request: broad file context,
// cross-file sections, then the original/current/updated triplet with a
// cursor marker and prefill.
func buildSweep(p *providerBase, ctx *RequestState) (*openai.CompletionRequest, error) {
	input := ctx.Input
	current := input.Current
	lines := current.File.Lines
	var promptBuilder strings.Builder

	if len(lines) == 0 {
		promptBuilder.WriteString("<|file_sep|>original/")
		promptBuilder.WriteString(current.File.Path)
		promptBuilder.WriteString("\n\n")
		promptBuilder.WriteString("<|file_sep|>current/")
		promptBuilder.WriteString(current.File.Path)
		promptBuilder.WriteString("\n\n")
		promptBuilder.WriteString("<|file_sep|>updated/")
		promptBuilder.WriteString(current.File.Path)
		promptBuilder.WriteString("\n")

		req := p.Request(promptBuilder.String(), sweepStopTokens)
		p.LogRequest(req, ctx.Window.MaxLines)
		return req, nil
	}

	// Broad file context (initial_file): ~300 lines around the cursor.
	initialFile := getBroadFileContext(current)
	if initialFile != "" {
		promptBuilder.WriteString("<|file_sep|>")
		promptBuilder.WriteString(current.File.Path)
		promptBuilder.WriteString("\n")
		promptBuilder.WriteString(initialFile)
		promptBuilder.WriteString("\n")
	}

	// Cross-file context (retrieval chunks from recent files).
	if recent, ok := sourcectx.Find[sourcectx.RecentFiles](input.Materials); ok {
		section{header: "<|file_sep|>context/retrieval\n"}.write(&promptBuilder, recentFileBlocks("<|file_sep|>", recent.Files))
	}

	// Treesitter context. Sweep keeps the header whenever the material
	// exists, even when the scope itself is empty.
	if treesitter, ok := sourcectx.Find[sourcectx.Treesitter](input.Materials); ok && treesitter.Data != nil {
		promptBuilder.WriteString("<|file_sep|>context/treesitter\n")
		promptBuilder.WriteString(treesitterScope(treesitter.Data))
	}

	// Diagnostics context.
	if diagnostics, ok := sourcectx.Find[sourcectx.Diagnostics](input.Materials); ok {
		section{header: "<|file_sep|>context/diagnostics\n"}.write(&promptBuilder, formatDiagnosticsCompact(diagnostics.Data))
	}

	// Diff history section (recent_changes).
	if editHistory, ok := sourcectx.Find[sourcectx.EditHistory](input.Materials); ok {
		if diffSection := diffHistoryOriginalUpdated(editHistory.Files, "<|file_sep|>%s.diff\n"); diffSection != "" {
			promptBuilder.WriteString(diffSection)
		}
	}

	// Git diff context.
	if gitDiff, ok := sourcectx.Find[sourcectx.GitDiff](input.Materials); ok {
		section{header: "<|file_sep|>context/staged_diff\n"}.write(&promptBuilder, gitDiffBody(gitDiff.Data))
	}

	cursorLineInWindow := ctx.Window.CursorLine
	codeBlock := strings.Join(ctx.Window.Lines, "\n")
	relativeCursor := min(computeRelativeCursor(ctx.Window.Lines, cursorLineInWindow, current.Cursor.Col), len(codeBlock))

	startLine := ctx.Window.Start + 1
	endLine := ctx.Window.Start + len(ctx.Window.Lines)

	promptBuilder.WriteString("<|file_sep|>original/")
	promptBuilder.WriteString(current.File.Path)
	promptBuilder.WriteString(":")
	fmt.Fprintf(&promptBuilder, "%d:%d", startLine, endLine)
	promptBuilder.WriteString("\n")
	promptBuilder.WriteString(codeBlock)
	promptBuilder.WriteString("\n")

	// Current section (with cursor marker).
	codeBlockWithCursor := codeBlock[:relativeCursor] + "<|cursor|>" + codeBlock[relativeCursor:]
	promptBuilder.WriteString("<|file_sep|>current/")
	promptBuilder.WriteString(current.File.Path)
	promptBuilder.WriteString(":")
	fmt.Fprintf(&promptBuilder, "%d:%d", startLine, endLine)
	promptBuilder.WriteString("\n")
	promptBuilder.WriteString(codeBlockWithCursor)
	promptBuilder.WriteString("\n")

	// Updated section (with prefill).
	promptBuilder.WriteString("<|file_sep|>updated/")
	promptBuilder.WriteString(current.File.Path)
	promptBuilder.WriteString(":")
	fmt.Fprintf(&promptBuilder, "%d:%d", startLine, endLine)
	promptBuilder.WriteString("\n")

	promptBuilder.WriteString(computePrefill(codeBlock, relativeCursor))

	req := p.Request(promptBuilder.String(), sweepStopTokens)
	p.LogRequest(req, ctx.Window.MaxLines)
	return req, nil
}

func parseSweep(p *providerBase, ctx *RequestState, result *openai.CompletionResult) (*types.CompletionResponse, error) {
	text := prefillForState(ctx) + result.Text
	text, endLineInc, resp, done := anchorPipeline(p.name, ctx, text, result.FinishReason, result.StoppedEarly)
	if done {
		return resp, nil
	}
	return parseCompletion(ctx, text, endLineInc), nil
}

func sweepStreamSpec(state *RequestState) streamSpec {
	windowStart, oldLines := defaultStreamWindow(state)
	return streamSpec{
		WindowStart:        windowStart,
		OldLines:           oldLines,
		Prefill:            prefillForState(state),
		FirstLineValidator: firstLineAnchorChecker(anchorPositionRatio),
	}
}

func defaultStreamWindow(state *RequestState) (int, []string) {
	oldLines := state.Window.Lines
	if len(oldLines) == 0 {
		oldLines = state.Input.Current.File.Lines
	}
	return state.Window.Start, oldLines
}

func computeRelativeCursor(lines []string, cursorLine, cursorCol int) int {
	offset := 0
	for i := 0; i < cursorLine && i < len(lines); i++ {
		offset += len(lines[i]) + 1
	}
	return offset + cursorCol
}

func prefillForState(ctx *RequestState) string {
	if len(ctx.Window.Lines) == 0 {
		return ""
	}
	codeBlock := strings.Join(ctx.Window.Lines, "\n")
	relativeCursor := min(computeRelativeCursor(ctx.Window.Lines, ctx.Window.CursorLine, ctx.Input.Current.Cursor.Col), len(codeBlock))
	return computePrefill(codeBlock, relativeCursor)
}

// computePrefill returns the prefix of the updated section fed to the model
// so it only generates from the edit point: everything before the cursor
// line, or nothing when the cursor sits on the first visible line.
func computePrefill(codeBlock string, relativeCursor int) string {
	prefixBeforeCursor := codeBlock[:relativeCursor]
	if !strings.Contains(prefixBeforeCursor, "\n") {
		return ""
	}
	prefillEnd := strings.LastIndex(prefixBeforeCursor, "\n") + 1
	return codeBlock[:prefillEnd]
}

// getBroadFileContext returns ~300 lines of context around the cursor.
func getBroadFileContext(current sourcectx.CurrentSnapshot) string {
	lines := current.File.Lines
	if len(lines) == 0 {
		return ""
	}

	cursorLine := current.Cursor.Row - 1
	contextStart := max(cursorLine-broadContextLinesBefore, 0)
	contextEnd := min(cursorLine+broadContextLinesAfter+1, len(lines))

	return strings.Join(lines[contextStart:contextEnd], "\n")
}

// parseCompletion turns cleaned sweep output into a window replacement.
func parseCompletion(ctx *RequestState, completionText string, endLineInc int) *types.CompletionResponse {
	lines := ctx.Input.Current.File.Lines

	completionText = strings.TrimSuffix(completionText, "<|endoftext|>")
	completionText = strings.TrimSuffix(completionText, "<|file_sep|>")
	completionText = strings.TrimRight(completionText, " \t\n\r")

	windowStart := ctx.Window.Start
	windowEnd := ctx.Window.Start + len(ctx.Window.Lines)
	if windowStart < 0 {
		windowStart = 0
	}
	if windowEnd > len(lines) {
		windowEnd = len(lines)
	}
	if windowStart >= windowEnd || windowStart >= len(lines) {
		return emptyResponse()
	}

	oldLines := lines[windowStart:windowEnd]
	oldText := strings.TrimRight(strings.Join(oldLines, "\n"), " \t\n\r")

	if completionText == oldText {
		return emptyResponse()
	}

	newLines := strings.Split(completionText, "\n")

	if endLineInc == 0 {
		endLineInc = min(windowStart+len(newLines), windowEnd)
	}

	return buildCompletion(ctx, windowStart+1, endLineInc, newLines)
}
