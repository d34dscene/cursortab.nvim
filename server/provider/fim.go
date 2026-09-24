package provider

import (
	"errors"
	"path/filepath"
	"strings"

	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/logger"
	"cursortab/types"
)

// buildFIM constructs requests for the three fim dialects: tokenized FIM when
// tokens are configured (qwen/mellum preset or user override), plain
// prompt+suffix mode otherwise. Prompt bytes follow each model's training
// format.
func buildFIM(p *providerBase, ctx *RequestState) (*openai.CompletionRequest, error) {
	var prefixContent strings.Builder
	var suffixContent strings.Builder

	if len(ctx.Window.Lines) > 0 {
		for i := range ctx.Window.CursorLine {
			prefixContent.WriteString(ctx.Window.Lines[i])
			prefixContent.WriteString("\n")
		}

		if ctx.Window.CursorLine < len(ctx.Window.Lines) {
			currentLine := ctx.Window.Lines[ctx.Window.CursorLine]
			cursorCol := min(ctx.Input.Current.Cursor.Col, len(currentLine))
			prefixContent.WriteString(currentLine[:cursorCol])
			suffixContent.WriteString(currentLine[cursorCol:])
		}

		for i := ctx.Window.CursorLine + 1; i < len(ctx.Window.Lines); i++ {
			suffixContent.WriteString("\n")
			suffixContent.WriteString(ctx.Window.Lines[i])
		}
	}

	tokens := p.config.FIMTokens

	// Prompt+suffix mode (OpenAI completions API style): no FIM tokens
	// configured.
	if tokens == nil {
		var prefixBuilder strings.Builder
		renderRetrievedPlain(&prefixBuilder, retrievalChunks(ctx.Input))
		prefixBuilder.WriteString(prefixContent.String())

		req := p.Request(prefixBuilder.String(), nil)
		req.Suffix = suffixContent.String()
		p.LogRequest(req, ctx.Window.MaxLines)
		return req, nil
	}

	var prompt strings.Builder

	if tokens.RepoName != "" || tokens.Filename != "" {
		buildRepoContext(&prompt, ctx, tokens)
	} else if chunks := retrievalChunks(ctx.Input); len(chunks) > 0 {
		renderRetrievedPlain(&prompt, chunks)
	}

	if tokens.SuffixFirst {
		prompt.WriteString(tokens.Suffix)
		prompt.WriteString(suffixContent.String())
		prompt.WriteString(tokens.Prefix)
		prompt.WriteString(prefixContent.String())
		prompt.WriteString(tokens.Middle)
	} else {
		prompt.WriteString(tokens.Prefix)
		prompt.WriteString(prefixContent.String())
		prompt.WriteString(tokens.Suffix)
		prompt.WriteString(suffixContent.String())
		prompt.WriteString(tokens.Middle)
	}

	stop := []string{tokens.Prefix, tokens.Suffix, tokens.Middle}
	if tokens.FileSep != "" {
		stop = append(stop, tokens.FileSep)
	}

	req := p.Request(prompt.String(), stop)
	p.LogRequest(req, ctx.Window.MaxLines)
	return req, nil
}

// buildRepoContext prepends cross-file context using repo-level FIM tokens.
// Qwen style (repo_name+file_sep) supports rich sections (diagnostics,
// treesitter, diffs). Mellum style (filename headers) was verified to work
// best with plain per-file content blocks before the FIM tokens.
func buildRepoContext(b *strings.Builder, ctx *RequestState, tokens *types.FIMTokenConfig) {
	input := ctx.Input
	current := input.Current
	if tokens.Filename != "" {
		buildFilenameContext(b, ctx, tokens)
		return
	}

	fileSep := tokens.FileSep
	repoName := tokens.RepoName

	workspace := filepath.Base(current.WorkspacePath)
	if workspace == "" || workspace == "." {
		workspace = "repo"
	}
	b.WriteString(repoName)
	b.WriteString(workspace)
	b.WriteString("\n")

	if recent, ok := sourcectx.Find[sourcectx.RecentFiles](input.Materials); ok {
		b.WriteString(recentFileBlocks(fileSep, recent.Files))
	}

	if diagnostics, ok := sourcectx.Find[sourcectx.Diagnostics](input.Materials); ok {
		section{header: fileSep + "context/diagnostics\n"}.write(b, formatDiagnosticsText(diagnostics.Data))
	}

	if treesitter, ok := sourcectx.Find[sourcectx.Treesitter](input.Materials); ok {
		section{header: fileSep + "context/treesitter\n"}.write(b, treesitterScope(treesitter.Data))
	}

	if editHistory, ok := sourcectx.Find[sourcectx.EditHistory](input.Materials); ok {
		if diffSection := formatDiffHistory(editHistory.Files, fileSep+"%s.diff\n"); diffSection != "" {
			b.WriteString(diffSection)
		}
	}

	if gitDiff, ok := sourcectx.Find[sourcectx.GitDiff](input.Materials); ok {
		section{header: fileSep + "context/staged_diff\n", trailer: "\n"}.write(b, gitDiffBody(gitDiff.Data))
	}

	// Retrieved workspace code sits just before the current file so the most
	// relevant declarations are closest to the FIM tokens.
	sectionRetrieval(b, fileSep, retrievalChunks(ctx.Input))

	b.WriteString(fileSep)
	b.WriteString(current.File.Path)
	b.WriteString("\n")
}

// buildFilenameContext emits cross-file context in Mellum's format: one
// "<filename>path\n<content>" block per recent file, then the current file
// header right before the FIM tokens. The rich qwen-style sections have no
// verified counterpart in Mellum's training format.
func buildFilenameContext(b *strings.Builder, ctx *RequestState, tokens *types.FIMTokenConfig) {
	if recent, ok := sourcectx.Find[sourcectx.RecentFiles](ctx.Input.Materials); ok {
		b.WriteString(recentFileBlocks(tokens.Filename, recent.Files))
	}
	filenameRetrieval(b, tokens, retrievalChunks(ctx.Input))
	b.WriteString(tokens.Filename)
	b.WriteString(ctx.Input.Current.File.Path)
	b.WriteString("\n")
}

// filenameRetrieval emits retrieved chunks in Mellum's per-file format, one
// "<filename>path\n<content>" block per chunk.
func filenameRetrieval(b *strings.Builder, tokens *types.FIMTokenConfig, chunks []types.RetrievalChunk) {
	for _, chunk := range chunks {
		section{header: tokens.Filename + chunk.Path + "\n", trailer: "\n"}.write(b, promptForm(chunk))
	}
}

// sectionRetrieval emits retrieved chunks as file_sep sections (qwen style),
// tagged with their real workspace-relative path so the model reads them the
// same way it reads any other repo file.
func sectionRetrieval(b *strings.Builder, fileSep string, chunks []types.RetrievalChunk) {
	for _, chunk := range chunks {
		section{header: fileSep + chunk.Path + "\n", trailer: "\n"}.write(b, promptForm(chunk))
	}
}

func parseFIM(p *providerBase, ctx *RequestState, result *openai.CompletionResult) (*types.CompletionResponse, error) {
	text := result.Text
	if resp, done := rejectEmptyText(p.name, text); done {
		return resp, nil
	}
	if stripped, resp, done := stripRepetitionText(text); done {
		return resp, nil
	} else {
		text = stripped
	}
	if trimmed, resp, done := dropLastLineIfTruncatedText(p.name, text, result.FinishReason, result.StoppedEarly); done {
		return resp, nil
	} else {
		text = trimmed
	}
	if resp, done := rejectLeadingNewlineWithSuffixText(ctx, text); done {
		return resp, nil
	}

	completionText := text
	current := ctx.Input.Current

	currentLine := ""
	if current.Cursor.Row >= 1 && current.Cursor.Row <= len(current.File.Lines) {
		currentLine = current.File.Lines[current.Cursor.Row-1]
	}
	cursorCol := min(current.Cursor.Col, len(currentLine))

	// Build the suffix text (everything after cursor in the file) so we can
	// detect when the model just regenerates it.
	afterCursor := currentLine[cursorCol:]

	var suffixBuilder strings.Builder
	suffixBuilder.WriteString(afterCursor)
	for i := current.Cursor.Row; i < len(current.File.Lines); i++ {
		suffixBuilder.WriteString("\n")
		suffixBuilder.WriteString(current.File.Lines[i])
	}
	suffix := suffixBuilder.String()

	// Strip suffix overlap: if the completion ends with text that matches
	// the beginning of the suffix, trim it. FIM models commonly regenerate
	// the suffix verbatim when there is nothing to insert.
	completionText = stripSuffixOverlap(completionText, suffix)
	completionLines := strings.Split(completionText, "\n")

	beforeCursor := currentLine[:cursorCol]

	resultLines := make([]string, len(completionLines))
	resultLines[0] = beforeCursor + completionLines[0]

	for i := 1; i < len(completionLines); i++ {
		resultLines[i] = completionLines[i]
	}

	// Attach afterCursor (suffix text like ")") to the appropriate line.
	// When the first completion line has content the model continues the
	// cursor line, so the suffix belongs on the first line. When it is empty
	// the model starts with \n, so the suffix belongs on the last line.
	if completionLines[0] != "" {
		resultLines[0] += afterCursor
	} else {
		resultLines[len(resultLines)-1] += afterCursor
	}

	// FIM inserts content at the cursor position: replace only the cursor line.
	return buildCompletion(ctx, current.Cursor.Row, current.Cursor.Row, resultLines), nil
}

func dropLastLineIfTruncatedText(name, text, finishReason string, stoppedEarly bool) (string, *types.CompletionResponse, bool) {
	if finishReason != "length" && !stoppedEarly {
		return text, nil, false
	}

	lines := strings.Split(text, "\n")
	originalLineCount := len(lines)
	if len(lines) <= 1 {
		logger.Info("%s: rejected, truncated single line", name)
		return text, emptyResponse(), true
	}

	lines = lines[:len(lines)-1]
	text = strings.Join(lines, "\n")
	if strings.TrimSpace(text) == "" {
		logger.Info("%s: rejected, empty after dropping truncated line", name)
		return text, emptyResponse(), true
	}

	logger.Info("%s: truncated, dropped last line (%d -> %d lines)",
		name, originalLineCount, len(lines))
	return text, nil, false
}

func rejectLeadingNewlineWithSuffixText(ctx *RequestState, text string) (*types.CompletionResponse, bool) {
	current := ctx.Input.Current
	if current.Cursor.Row < 1 || current.Cursor.Row > len(current.File.Lines) {
		return nil, false
	}

	currentLine := current.File.Lines[current.Cursor.Row-1]
	cursorCol := min(current.Cursor.Col, len(currentLine))
	atEOL := cursorCol >= len(strings.TrimRight(currentLine, " \t"))
	if !atEOL || !strings.HasPrefix(text, "\n") {
		return nil, false
	}

	afterCursor := currentLine[cursorCol:]
	var suffixBuilder strings.Builder
	suffixBuilder.WriteString(afterCursor)
	for i := current.Cursor.Row; i < len(current.File.Lines); i++ {
		suffixBuilder.WriteString("\n")
		suffixBuilder.WriteString(current.File.Lines[i])
	}
	if strings.TrimSpace(suffixBuilder.String()) == "" {
		return nil, false
	}

	return emptyResponse(), true
}

// stripSuffixOverlap removes the longest suffix of completion that matches a
// prefix of the file suffix. This catches the common FIM no-op pattern where
// the model regenerates text that already exists after the cursor.
func stripSuffixOverlap(completion, suffix string) string {
	if completion == "" || suffix == "" {
		return completion
	}
	maxK := min(len(completion), len(suffix))
	best := 0
	for k := 1; k <= maxK; k++ {
		if completion[len(completion)-k:] == suffix[:k] {
			best = k
		}
	}
	if best > 0 {
		return completion[:len(completion)-best]
	}
	return completion
}

func fimStreamSpec(state *RequestState) streamSpec {
	windowStart, oldLines, transform := fimStreamWindow(state)
	return streamSpec{
		WindowStart:   windowStart,
		OldLines:      oldLines,
		LineTransform: transform.emit,
		FinalLine:     transform.finalLine,
	}
}

// fimStreamWindow scopes the stream to the cursor line. Raw FIM output lines
// are insertion content; the transform attaches the text before the cursor to
// the first line and defers the text after the cursor to the last line, so
// the streamed lines match what parse produces for the accumulated text.
func fimStreamWindow(state *RequestState) (int, []string, *fimStreamTransform) {
	current := state.Input.Current
	lines := state.Window.Lines
	cursorLineIdx := state.Window.CursorLine
	if len(lines) == 0 {
		lines = current.File.Lines
		cursorLineIdx = current.Cursor.Row - 1
	}

	cursorLine := ""
	if cursorLineIdx >= 0 && cursorLineIdx < len(lines) {
		cursorLine = lines[cursorLineIdx]
	}
	col := max(0, min(current.Cursor.Col, len(cursorLine)))

	row := current.Cursor.Row
	fileLines := current.File.Lines
	currentFileLine := ""
	if row >= 1 && row <= len(fileLines) {
		currentFileLine = fileLines[row-1]
	}
	fileCol := max(0, min(current.Cursor.Col, len(currentFileLine)))

	return state.Window.Start + cursorLineIdx, []string{cursorLine}, &fimStreamTransform{
		before:           cursorLine[:col],
		after:            cursorLine[col:],
		atEOL:            fileCol >= len(strings.TrimRight(currentFileLine, " \t")),
		suffixHasContent: suffixAfterCursorHasContent(currentFileLine, fileCol, fileLines, row),
	}
}

func suffixAfterCursorHasContent(currentLine string, col int, fileLines []string, row int) bool {
	var b strings.Builder
	b.WriteString(currentLine[col:])
	for i := row; i < len(fileLines); i++ {
		b.WriteString("\n")
		b.WriteString(fileLines[i])
	}
	return strings.TrimSpace(b.String()) != ""
}

var errLeadingNewline = errors.New("fim: completion starts a new line although content follows the cursor")

// fimStreamTransform converts raw FIM insertion lines into the replacement
// lines for the cursor line. Parse (used by Finish) attaches the after-cursor
// text to the first generated line when it has content, otherwise to the last
// line, and strips a trailing overlap with the text after the cursor from
// wherever the completion ends. The first two are decidable at the first
// line; the strip only applies to the completion's end, so lines that might
// be regenerating the after-cursor text are held back until the stream ends.
type fimStreamTransform struct {
	before string
	after  string

	atEOL            bool
	suffixHasContent bool

	firstSeen    bool
	firstEmitted bool
	afterPlaced  bool
	held         string
	isFirstHeld  bool
	hasHeld      bool
}

func (t *fimStreamTransform) emit(line string) (string, bool, error) {
	if !t.firstSeen {
		t.firstSeen = true
		if t.atEOL && line == "" && t.suffixHasContent {
			// Mirrors rejectLeadingNewlineWithSuffixText for batch requests.
			return "", false, errLeadingNewline
		}
		if line != "" && !endsWithAfterPrefix(line, t.after) {
			t.firstEmitted = true
			t.afterPlaced = true
			return t.before + line + t.after, true, nil
		}
		t.hold(line, true)
		return "", false, nil
	}

	if t.hasHeld {
		emitted := t.release()
		t.hold(line, false)
		return emitted, true, nil
	}

	t.hold(line, false)
	return "", false, nil
}

// endsWithAfterPrefix reports whether the line ends with a non-empty prefix
// of after. Such a line may be regenerating text that already follows the
// cursor; whether to strip it depends on whether more lines follow.
func endsWithAfterPrefix(line, after string) bool {
	maxK := min(len(line), len(after))
	for k := 1; k <= maxK; k++ {
		if line[len(line)-k:] == after[:k] {
			return true
		}
	}
	return false
}

func (t *fimStreamTransform) hold(line string, isFirst bool) {
	t.held = line
	t.isFirstHeld = isFirst
	t.hasHeld = true
}

func (t *fimStreamTransform) release() string {
	var line string
	if t.isFirstHeld {
		line = t.before + t.held
		if t.held != "" {
			line += t.after
			t.afterPlaced = true
		}
	} else {
		line = t.held
	}
	t.firstEmitted = true
	t.hasHeld = false
	return line
}

func (t *fimStreamTransform) finalLine() (string, bool) {
	if !t.hasHeld {
		return "", false
	}
	line := stripSuffixOverlap(t.held, t.after)
	if !t.firstEmitted {
		line = t.before + line
	}
	if !t.afterPlaced {
		line += t.after
	}
	return line, true
}
