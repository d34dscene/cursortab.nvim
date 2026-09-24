package provider

import (
	"strings"

	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/text"
	"cursortab/types"
	"cursortab/utils"
)

// SeedCoder format tokens. Match Zed's crates/zeta_prompt/src/zeta_prompt.rs
// lines 3119-3128 exactly.
const (
	fimSuffix     = "<[fim-suffix]>"
	fimPrefix     = "<[fim-prefix]>"
	fimMiddle     = "<[fim-middle]>"
	fileMarker    = "<filename>"
	currentMarker = "<<<<<<< CURRENT\n"
	separator     = "=======\n"
	endMarker     = ">>>>>>> UPDATED\n"
	noEditsMarker = "NO_EDITS"
	cursorMarker  = "<|user_cursor|>"
)

// Editable region sizing. Zed uses token budgets (350 editable, 150 context)
// for the cloud endpoint. We approximate with line counts inside the shared
// request window bounded by Endpoint.MaxTokens.
const (
	editableLinesBefore = 15
	editableLinesAfter  = 15
	maxEditableChars    = 3000 // ~1000 tokens; upper bound when snapping to AST
)

var zeta2StopTokens = []string{endMarker, strings.TrimSuffix(endMarker, "\n")}

func buildZeta2(p *providerBase, ctx *RequestState) (*openai.CompletionRequest, error) {
	req := p.Request(assembleZeta2Prompt(ctx), zeta2StopTokens)
	p.LogRequest(req, ctx.Window.MaxLines)
	return req, nil
}

func assembleZeta2Prompt(ctx *RequestState) string {
	trimmed := ctx.Window.Lines
	input := ctx.Input
	current := input.Current
	if len(trimmed) == 0 {
		var b strings.Builder
		b.WriteString(fimSuffix)
		b.WriteString("\n")
		b.WriteString(fimPrefix)
		b.WriteString(fileMarker)
		b.WriteString(current.File.Path)
		b.WriteString("\n")
		b.WriteString(currentMarker)
		b.WriteString(cursorMarker)
		b.WriteString("\n")
		b.WriteString(separator)
		b.WriteString(fimMiddle)
		return b.String()
	}

	editableStart, editableEnd := computeEditableRange(trimmed, ctx.Window.CursorLine, ctx.Window.Start, treesitterRanges(input.Materials))

	beforeLines := trimmed[:editableStart]
	editLines := trimmed[editableStart:editableEnd]
	suffixLines := trimmed[editableEnd:]

	var b strings.Builder

	b.WriteString(fimSuffix)
	suffixText := ""
	if len(suffixLines) > 0 {
		suffixText = strings.Join(suffixLines, "\n")
		b.WriteString(suffixText)
	}
	ensureTrailingNewline(&b, suffixText)

	b.WriteString(fimPrefix)

	if recentFiles, ok := sourcectx.Find[sourcectx.RecentFiles](input.Materials); ok {
		writeRecentFilesPseudoFiles(&b, recentFiles.Files)
	}
	if diagnostics, ok := sourcectx.Find[sourcectx.Diagnostics](input.Materials); ok {
		writeDiagnosticsPseudoFile(&b, diagnostics.Data)
	}
	if treesitter, ok := sourcectx.Find[sourcectx.Treesitter](input.Materials); ok {
		writeTreesitterPseudoFile(&b, treesitter.Data)
	}
	if gitDiff, ok := sourcectx.Find[sourcectx.GitDiff](input.Materials); ok {
		writeGitDiffPseudoFile(&b, gitDiff.Data)
	}
	if editHistory, ok := sourcectx.Find[sourcectx.EditHistory](input.Materials); ok {
		writePseudoFile(&b, "edit_history", buildEditHistory(editHistory.Files))
	}

	b.WriteString(fileMarker)
	b.WriteString(current.File.Path)
	b.WriteString("\n")

	if len(beforeLines) > 0 {
		b.WriteString(strings.Join(beforeLines, "\n"))
		b.WriteString("\n")
	}

	b.WriteString(currentMarker)
	editableText := formatEditableWithCursor(editLines, ctx.Window.CursorLine-editableStart, current.Cursor.Col)
	b.WriteString(editableText)
	ensureTrailingNewline(&b, editableText)
	b.WriteString(separator)
	b.WriteString(fimMiddle)

	return b.String()
}

func zeta2StreamSpec(ctx *RequestState) streamSpec {
	windowStart, oldLines := streamWindow(ctx)
	return streamSpec{
		WindowStart:   windowStart,
		OldLines:      oldLines,
		LineTransform: visibleStreamLine,
	}
}

func streamWindow(ctx *RequestState) (int, []string) {
	if len(ctx.Window.Lines) == 0 {
		return 0, nil
	}
	editableStart, editableEnd := computeEditableRange(ctx.Window.Lines, ctx.Window.CursorLine, ctx.Window.Start, treesitterRanges(ctx.Input.Materials))
	oldLines := ctx.Window.Lines[editableStart:editableEnd]
	for len(oldLines) > 0 && strings.TrimSpace(oldLines[len(oldLines)-1]) == "" {
		oldLines = oldLines[:len(oldLines)-1]
	}
	return ctx.Window.Start + editableStart, oldLines
}

// computeEditableRange returns [start, end) line indices within trimmed lines
// for the editable region centered on cursorLine. When syntaxRanges is
// non-empty, the range is snapped to AST node boundaries (within a char
// budget) so the editable region lands on complete syntactic units rather
// than mid-expression. windowStart is the offset of trimmed within the full
// buffer, used to translate syntax ranges into trimmed-window coordinates.
func computeEditableRange(trimmed []string, cursorLine, windowStart int, syntaxRanges []*types.LineRange) (int, int) {
	if len(trimmed) == 0 {
		return 0, 0
	}
	if cursorLine < 0 {
		cursorLine = 0
	}
	if cursorLine >= len(trimmed) {
		cursorLine = len(trimmed) - 1
	}

	start := max(cursorLine-editableLinesBefore, 0)
	end := min(cursorLine+1+editableLinesAfter, len(trimmed))

	if len(syntaxRanges) > 0 {
		shifted := make([]*types.LineRange, 0, len(syntaxRanges))
		for _, sr := range syntaxRanges {
			shifted = append(shifted, &types.LineRange{
				StartLine: sr.StartLine - windowStart,
				EndLine:   sr.EndLine - windowStart,
			})
		}
		// SnapToSyntaxBoundaries takes inclusive end; convert and back.
		snapStart, snapEnd := utils.SnapToSyntaxBoundaries(trimmed, start, end-1, maxEditableChars, shifted)
		if snapStart < 0 {
			snapStart = 0
		}
		if snapEnd >= len(trimmed) {
			snapEnd = len(trimmed) - 1
		}
		start = snapStart
		end = snapEnd + 1
	}

	return start, end
}

// treesitterRanges extracts syntax ranges from collected context, returning
// nil when treesitter context is unavailable.
func treesitterRanges(materials sourcectx.Materials) []*types.LineRange {
	if treesitter, ok := sourcectx.Find[sourcectx.Treesitter](materials); ok && treesitter.Data != nil {
		return treesitter.Data.SyntaxRanges
	}
	return nil
}

// formatEditableWithCursor renders the editable region with the cursor
// marker inserted at the cursor position.
func formatEditableWithCursor(editLines []string, cursorRelLine, cursorCol int) string {
	if len(editLines) == 0 {
		return cursorMarker
	}
	if cursorRelLine < 0 {
		cursorRelLine = 0
	}
	if cursorRelLine >= len(editLines) {
		cursorRelLine = len(editLines) - 1
		cursorCol = len(editLines[cursorRelLine])
	}

	lines := make([]string, len(editLines))
	copy(lines, editLines)
	line := lines[cursorRelLine]
	col := max(min(cursorCol, len(line)), 0)
	lines[cursorRelLine] = line[:col] + cursorMarker + line[col:]

	return strings.Join(lines, "\n")
}

func parseZeta2(p *providerBase, ctx *RequestState, result *openai.CompletionResult) (*types.CompletionResponse, error) {
	text := result.Text
	if resp, done := rejectEmptyText(p.name, text); done {
		return resp, nil
	}
	if stripped, resp, done := stripRepetitionText(text); done {
		return resp, nil
	} else {
		text = stripped
	}
	return parseResultText(ctx, text), nil
}

func parseResultText(ctx *RequestState, text string) *types.CompletionResponse {
	cursorMarkerLine, cursorMarkerSeen := cursorMarkerPosition(text)
	return parseCompletionWithCursorMarker(ctx, text, cursorMarkerSeen, cursorMarkerLine)
}

func parseCompletionWithCursorMarker(
	ctx *RequestState,
	rawText string,
	cursorMarkerSeen bool,
	cursorMarkerLine int,
) *types.CompletionResponse {
	raw := rawText

	raw = strings.TrimSuffix(raw, endMarker)
	raw = strings.TrimSuffix(raw, strings.TrimSuffix(endMarker, "\n"))

	if strings.HasPrefix(strings.TrimSpace(raw), noEditsMarker) {
		return emptyResponse()
	}

	raw = stripCursorMarker(raw, cursorMarker)

	if raw == "" {
		return emptyResponse()
	}

	newLines := text.SplitLines(raw)
	if len(newLines) == 0 {
		return emptyResponse()
	}

	editableStart, editableEnd := computeEditableRange(ctx.Window.Lines, ctx.Window.CursorLine, ctx.Window.Start, treesitterRanges(ctx.Input.Materials))
	startLine := ctx.Window.Start + editableStart + 1
	endLineInc := ctx.Window.Start + editableEnd

	resp := buildCompletion(ctx, startLine, endLineInc, newLines)
	if resp != nil && resp.Completion != nil && cursorMarkerSeen {
		resp.CursorTarget = buildCursorTarget(ctx, editableStart, cursorMarkerLine, newLines)
	}
	return resp
}

func visibleStreamLine(line string) (string, bool, error) {
	if !strings.Contains(line, cursorMarker) {
		return line, true, nil
	}
	stripped := strings.ReplaceAll(line, cursorMarker, "")
	return stripped, strings.TrimSpace(stripped) != "", nil
}
