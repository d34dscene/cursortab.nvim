package provider

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"cursortab/logger"
	"cursortab/text"
	"cursortab/types"
)

const (
	anchorSimilarityThreshold   = 0.7
	minLinesForAnchorValidation = 10
	anchorSearchBefore          = 2
	anchorSearchAfter           = 5

	// anchorPositionRatio caps where the first output line may re-anchor
	// inside the window; beyond it the model hallucinated its location.
	anchorPositionRatio = 0.25
	// anchorTruncationRatio is the share of window lines a length-truncated
	// completion must keep before the anchor-matched replacement is trusted.
	anchorTruncationRatio = 0.75
)

// maxEditHistoryEvents bounds the zeta edit-history events in one prompt.
const maxEditHistoryEvents = 6

// anchorPipeline is the shared edit-dialect parse tail: reject empty output,
// strip repetition, validate the first-line anchor position, then repair a
// length-truncated tail against the window. It returns the cleaned text, the
// replacement end line when a truncation was repaired, and a response when
// the output was rejected.
func anchorPipeline(name string, state *RequestState, text, finishReason string, stoppedEarly bool) (string, int, *types.CompletionResponse, bool) {
	if resp, done := rejectEmptyText(name, text); done {
		return text, 0, resp, true
	}
	stripped, resp, done := stripRepetitionText(text)
	if done {
		return text, 0, resp, true
	}
	text = stripped
	if resp, done := validateAnchorPositionText(name, state, text, anchorPositionRatio); done {
		return text, 0, resp, true
	}
	return anchorTruncationText(name, state, text, finishReason, stoppedEarly, anchorTruncationRatio)
}

func rejectEmptyText(providerName, text string) (*types.CompletionResponse, bool) {
	if strings.TrimSpace(text) == "" {
		logger.Debug("%s: rejected, empty or whitespace-only", providerName)
		return emptyResponse(), true
	}
	return nil, false
}

func stripRepetitionText(text string) (string, *types.CompletionResponse, bool) {
	lines := strings.Split(text, "\n")
	cutIdx := -1
	for i := 2; i < len(lines); i++ {
		if lines[i] == lines[i-1] && lines[i] == lines[i-2] && strings.TrimSpace(lines[i]) != "" {
			cutIdx = i - 2
			break
		}
	}
	if cutIdx < 0 {
		return text, nil, false
	}
	if cutIdx == 0 {
		return text, emptyResponse(), true
	}
	return strings.Join(lines[:cutIdx], "\n"), nil, false
}

func anchorTruncationText(providerName string, state *RequestState, text, finishReason string, stoppedEarly bool, threshold float64) (string, int, *types.CompletionResponse, bool) {
	if finishReason != "length" && !stoppedEarly {
		return text, 0, nil, false
	}

	if stoppedEarly {
		finishReason = "length"
	}

	newLines := strings.Split(text, "\n")
	originalLineCount := len(newLines)
	windowEnd := state.Window.Start + len(state.Window.Lines)
	oldLines := state.Input.Current.File.Lines[state.Window.Start:windowEnd]

	processedLines, endLineInc, shouldReject := handleTruncatedCompletionWithAnchor(
		newLines, oldLines, finishReason, state.Window.Start, windowEnd,
	)
	if shouldReject {
		logger.Debug("%s: rejected, truncation handling failed", providerName)
		return text, 0, emptyResponse(), true
	}

	if len(oldLines) > minLinesForAnchorValidation {
		minAllowedLines := int(float64(len(oldLines)) * threshold)
		if len(processedLines) < minAllowedLines {
			logger.Debug("%s: rejected, too few lines (%d < %d min)",
				providerName, len(processedLines), minAllowedLines)
			return text, 0, emptyResponse(), true
		}
	}

	logger.Info("%s: truncated, replacing lines %d-%d (%d -> %d lines)",
		providerName, state.Window.Start+1, endLineInc, originalLineCount, len(processedLines))
	return strings.Join(processedLines, "\n"), endLineInc, nil, false
}

func validateAnchorPositionText(providerName string, state *RequestState, text string, maxAnchorRatio float64) (*types.CompletionResponse, bool) {
	newLines := strings.Split(text, "\n")
	if len(newLines) == 0 {
		return nil, false
	}
	oldLines := state.Input.Current.File.Lines[state.Window.Start : state.Window.Start+len(state.Window.Lines)]
	anchorIdx, maxAllowed, reject := checkAnchorPosition(newLines[0], oldLines, maxAnchorRatio)
	if reject {
		logger.Debug("%s: rejected, first line anchors at %d (max allowed %d)",
			providerName, anchorIdx, maxAllowed)
		return emptyResponse(), true
	}
	return nil, false
}

// firstLineAnchorChecker validates the first streamed line as soon as it
// arrives so a mislocated stream dies before it reaches the UI.
func firstLineAnchorChecker(maxAnchorRatio float64) func(*RequestState, string) error {
	return func(state *RequestState, firstLine string) error {
		oldLines := state.Input.Current.File.Lines[state.Window.Start : state.Window.Start+len(state.Window.Lines)]
		_, _, reject := checkAnchorPosition(firstLine, oldLines, maxAnchorRatio)
		if reject {
			return errors.New("first line anchor position too far from start")
		}
		return nil
	}
}

// checkAnchorPosition validates that a first line anchors within acceptable
// range. Returns (anchorIdx, maxAllowed, shouldReject).
func checkAnchorPosition(firstLine string, oldLines []string, maxRatio float64) (int, int, bool) {
	if len(oldLines) <= minLinesForAnchorValidation {
		return -1, 0, false
	}
	anchorIdx := findAnchorLineFullSearch(firstLine, oldLines)
	maxAllowed := int(float64(len(oldLines)) * maxRatio)
	return anchorIdx, maxAllowed, anchorIdx > maxAllowed
}

// findAnchorLine searches for the best matching line in oldLines around
// expectedPos to handle structural changes. Returns -1 when no good match
// exists.
func findAnchorLine(needle string, oldLines []string, expectedPos int) int {
	if len(oldLines) == 0 {
		return -1
	}

	bestIdx := -1
	bestSimilarity := anchorSimilarityThreshold

	searchStart := max(0, expectedPos-anchorSearchBefore)
	searchEnd := min(len(oldLines), expectedPos+anchorSearchAfter)

	for i := searchStart; i < searchEnd; i++ {
		similarity := text.LineSimilarity(needle, oldLines[i])
		if similarity > bestSimilarity {
			bestSimilarity = similarity
			bestIdx = i
		}
	}

	return bestIdx
}

// findAnchorLineFullSearch searches the entire oldLines array, used to detect
// output misaligned with the expected window. Returns -1 when no good match
// exists.
func findAnchorLineFullSearch(needle string, oldLines []string) int {
	if len(oldLines) == 0 {
		return -1
	}

	bestIdx := -1
	bestSimilarity := anchorSimilarityThreshold

	for i, line := range oldLines {
		similarity := text.LineSimilarity(needle, line)
		if similarity > bestSimilarity {
			bestSimilarity = similarity
			bestIdx = i
		}
	}

	return bestIdx
}

// handleTruncatedCompletionWithAnchor processes completion lines when the
// model hit max_tokens, using anchor matching to find the replacement range.
func handleTruncatedCompletionWithAnchor(
	newLines []string,
	oldLines []string,
	finishReason string,
	windowStart, windowEnd int,
) ([]string, int, bool) {
	endLineInc := windowEnd

	if finishReason == "length" && len(newLines) > 0 {
		newLines = newLines[:len(newLines)-1]

		if len(newLines) == 0 {
			return nil, 0, true
		}

		lastModelLine := newLines[len(newLines)-1]
		expectedPos := len(newLines) - 1
		anchorIdx := findAnchorLine(lastModelLine, oldLines, expectedPos)

		if anchorIdx != -1 {
			endLineInc = windowStart + anchorIdx + 1
		} else {
			endLineInc = windowStart + len(newLines)
		}
	}

	return newLines, endLineInc, false
}

// section frames one cross-file context body under a dialect's header
// template. Empty bodies write nothing.
type section struct {
	header  string
	trailer string
}

func (s section) write(b *strings.Builder, body string) {
	if body == "" {
		return
	}
	b.WriteString(s.header)
	b.WriteString(body)
	b.WriteString(s.trailer)
}

// formatDiagnosticsText renders diagnostics as plain text lines:
//
//	line 10: [ERROR] undefined: foo (source: gopls)
//
// Shared by the qwen repo context and both zeta framings.
func formatDiagnosticsText(diag *types.Diagnostics) string {
	if diag == nil || len(diag.Items) == 0 {
		return ""
	}

	var b strings.Builder
	for _, d := range diag.Items {
		if d.Range != nil {
			fmt.Fprintf(&b, "line %d: ", d.Range.StartLine)
		}
		fmt.Fprintf(&b, "[%s] %s", d.Severity, d.Message)
		if d.Source != "" {
			fmt.Fprintf(&b, " (source: %s)", d.Source)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// formatDiagnosticsCompact is sweep's shorter diagnostic line:
//
//	Line 10: [gopls] undefined: foo
func formatDiagnosticsCompact(diag *types.Diagnostics) string {
	if diag == nil || len(diag.Items) == 0 {
		return ""
	}

	var b strings.Builder
	for _, d := range diag.Items {
		if d.Range != nil {
			b.WriteString("Line ")
			b.WriteString(strconv.Itoa(d.Range.StartLine))
			b.WriteString(": ")
		}
		if d.Source != "" {
			b.WriteString("[")
			b.WriteString(d.Source)
			b.WriteString("] ")
		}
		b.WriteString(d.Message)
		b.WriteString("\n")
	}
	return b.String()
}

// treesitterScope renders enclosing scope, siblings, and imports. Shared by
// the qwen repo context and sweep framings.
func treesitterScope(ts *types.TreesitterContext) string {
	if ts == nil {
		return ""
	}
	var b strings.Builder
	if ts.EnclosingSignature != "" {
		fmt.Fprintf(&b, "Enclosing scope: %s\n", ts.EnclosingSignature)
	}
	for _, s := range ts.Siblings {
		fmt.Fprintf(&b, "Sibling: %s\n", s.Signature)
	}
	for _, imp := range ts.Imports {
		fmt.Fprintf(&b, "Import: %s\n", imp)
	}
	return b.String()
}

// treesitterScopeZeta is the zeta variant that keeps sibling line numbers.
func treesitterScopeZeta(ts *types.TreesitterContext) string {
	if ts == nil {
		return ""
	}
	var b strings.Builder
	if ts.EnclosingSignature != "" {
		fmt.Fprintf(&b, "Enclosing scope: %s\n", ts.EnclosingSignature)
	}
	for _, s := range ts.Siblings {
		fmt.Fprintf(&b, "Sibling: line %d: %s\n", s.Line, s.Signature)
	}
	for _, imp := range ts.Imports {
		fmt.Fprintf(&b, "Import: %s\n", imp)
	}
	return b.String()
}

// recentFileBlocks renders {token}{path}\n{lines}\n per snapshot. Shared by
// the qwen file_sep, mellum filename, and sweep context/retrieval framings.
func recentFileBlocks(token string, snapshots []*types.RecentBufferSnapshot) string {
	var b strings.Builder
	for _, snap := range snapshots {
		b.WriteString(token)
		b.WriteString(snap.FilePath)
		b.WriteString("\n")
		b.WriteString(strings.Join(snap.Lines, "\n"))
		b.WriteString("\n")
	}
	return b.String()
}

func gitDiffBody(gd *types.GitDiffContext) string {
	if gd == nil {
		return ""
	}
	return gd.Diff
}

// diffEntryToUnifiedDiff converts a DiffEntry to a unified diff format.
func diffEntryToUnifiedDiff(entry *types.DiffEntry) string {
	if entry.Original == entry.Updated {
		return ""
	}

	originalLines := strings.Split(entry.Original, "\n")
	updatedLines := strings.Split(entry.Updated, "\n")

	var b strings.Builder

	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n",
		1, len(originalLines), 1, len(updatedLines))

	for _, line := range originalLines {
		b.WriteString("-")
		b.WriteString(line)
		b.WriteString("\n")
	}

	for _, line := range updatedLines {
		b.WriteString("+")
		b.WriteString(line)
		b.WriteString("\n")
	}

	return strings.TrimSuffix(b.String(), "\n")
}

// formatDiffHistory renders qwen's diff sections: one header per file, then
// the unified diff. headerTemplate takes the file name as %s.
func formatDiffHistory(history []*types.FileDiffHistory, headerTemplate string) string {
	var b strings.Builder
	for _, fileHistory := range history {
		if len(fileHistory.DiffHistory) == 0 {
			continue
		}
		for _, diffEntry := range fileHistory.DiffHistory {
			unified := diffEntryToUnifiedDiff(diffEntry)
			if unified == "" {
				continue
			}
			fmt.Fprintf(&b, headerTemplate, fileHistory.FileName)
			b.WriteString(unified)
		}
	}
	return b.String()
}

// diffHistoryOriginalUpdated renders sweep's original/updated diff sections.
// headerTemplate takes the file name as %s.
func diffHistoryOriginalUpdated(history []*types.FileDiffHistory, headerTemplate string) string {
	var b strings.Builder
	for _, fileHistory := range history {
		if len(fileHistory.DiffHistory) == 0 {
			continue
		}
		for _, diffEntry := range fileHistory.DiffHistory {
			if diffEntry.Original == diffEntry.Updated {
				continue
			}
			if headerTemplate != "" {
				fmt.Fprintf(&b, headerTemplate, fileHistory.FileName)
			}
			b.WriteString("original:\n")
			b.WriteString(diffEntry.Original)
			b.WriteString("\nupdated:\n")
			b.WriteString(diffEntry.Updated)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// buildEditHistory formats file diff histories as git-style unified diffs,
// newest-first by timestamp, capped at maxEditHistoryEvents, with predicted
// edits annotated. Matches Zed's write_event in zeta_prompt.rs.
func buildEditHistory(history []*types.FileDiffHistory) string {
	if len(history) == 0 {
		return ""
	}

	type event struct {
		path      string
		diff      string
		predicted bool
		tsNs      int64
	}
	var events []event
	for _, fh := range history {
		for _, de := range fh.DiffHistory {
			unified := diffEntryToUnifiedDiff(de)
			if unified == "" {
				continue
			}
			events = append(events, event{
				path:      fh.FileName,
				diff:      unified,
				predicted: de.Source == types.DiffSourcePredicted,
				tsNs:      de.TimestampNs,
			})
		}
	}
	if len(events) == 0 {
		return ""
	}

	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && events[j].tsNs > events[j-1].tsNs; j-- {
			events[j], events[j-1] = events[j-1], events[j]
		}
	}

	if len(events) > maxEditHistoryEvents {
		events = events[:maxEditHistoryEvents]
	}

	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	var b strings.Builder
	for i, ev := range events {
		if i > 0 {
			b.WriteString("\n")
		}
		if ev.predicted {
			b.WriteString("// User accepted prediction:\n")
		}
		path := strings.ReplaceAll(ev.path, "\\", "/")
		b.WriteString("--- a/")
		b.WriteString(path)
		b.WriteString("\n+++ b/")
		b.WriteString(path)
		b.WriteString("\n")
		b.WriteString(ev.diff)
		if !strings.HasSuffix(ev.diff, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// writePseudoFile writes one <filename>{path}\n{content}\n block followed by
// a blank separator line, matching a Zed V0211SeedCoder related file block.
func writePseudoFile(b *strings.Builder, path, content string) {
	if content == "" {
		return
	}
	b.WriteString(fileMarker)
	b.WriteString(path)
	b.WriteString("\n")
	b.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

// writeRecentFilesPseudoFiles renders each recent buffer snapshot as its own
// <filename>{path} block: the best proxy for Zed's LSP-driven related files.
func writeRecentFilesPseudoFiles(b *strings.Builder, snapshots []*types.RecentBufferSnapshot) {
	for _, snap := range snapshots {
		if len(snap.Lines) == 0 {
			continue
		}
		writePseudoFile(b, snap.FilePath, strings.Join(snap.Lines, "\n"))
	}
}

// writeDiagnosticsPseudoFile renders LSP diagnostics as a
// <filename>diagnostics block, one line per diagnostic. Zed drops diagnostics
// entirely; the self-explanatory format earns its place here anyway.
func writeDiagnosticsPseudoFile(b *strings.Builder, diag *types.Diagnostics) {
	writePseudoFile(b, "diagnostics", formatDiagnosticsText(diag))
}

// writeTreesitterPseudoFile renders enclosing scope, siblings, and imports as
// a <filename>context/treesitter block: the structural context Zed replaces
// with LSP related files.
func writeTreesitterPseudoFile(b *strings.Builder, ts *types.TreesitterContext) {
	writePseudoFile(b, "context/treesitter", treesitterScopeZeta(ts))
}

// writeGitDiffPseudoFile renders the staged git diff as a
// <filename>context/staged_diff block.
func writeGitDiffPseudoFile(b *strings.Builder, gd *types.GitDiffContext) {
	writePseudoFile(b, "context/staged_diff", gitDiffBody(gd))
}

// stripCursorMarker removes the cursor marker from response text. Lines that
// consist solely of the marker (with optional whitespace) are dropped so they
// do not produce phantom empty lines.
func stripCursorMarker(text, marker string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if !strings.Contains(line, marker) {
			out = append(out, line)
			continue
		}
		stripped := strings.ReplaceAll(line, marker, "")
		if strings.TrimSpace(stripped) == "" {
			continue
		}
		out = append(out, stripped)
	}
	return strings.Join(out, "\n")
}

func cursorMarkerPosition(raw string) (int, bool) {
	for i, line := range strings.Split(raw, "\n") {
		if strings.Contains(line, cursorMarker) {
			return i, true
		}
	}
	return 0, false
}

func buildCursorTarget(state *RequestState, editableStart, markerLine int, newLines []string) *types.CursorPredictionTarget {
	lineIdx := max(markerLine, 0)
	if lineIdx >= len(newLines) {
		lineIdx = len(newLines) - 1
	}
	if lineIdx < 0 {
		return nil
	}

	bufferRow := state.Window.Start + editableStart + lineIdx + 1

	return &types.CursorPredictionTarget{
		LineNumber:      int32(bufferRow),
		ShouldRetrigger: true,
	}
}

// ensureTrailingNewline appends a newline only when the builder's last byte
// is not one already.
func ensureTrailingNewline(b *strings.Builder, lastWrite string) {
	if len(lastWrite) == 0 || lastWrite[len(lastWrite)-1] != '\n' {
		b.WriteString("\n")
	}
}

func isNoOpReplacement(newLines, oldLines []string) bool {
	newText := strings.TrimRight(strings.Join(newLines, "\n"), " \t\n\r")
	oldText := strings.TrimRight(strings.Join(oldLines, "\n"), " \t\n\r")
	return newText == oldText
}
