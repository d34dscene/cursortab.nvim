package engine

import (
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"cursortab/logger"
	"cursortab/text"
	"cursortab/types"
)

// SuppressReason names why a completion did not reach the provider or the
// buffer. "" means allowed.
type SuppressReason string

const (
	SuppressNone           SuppressReason = ""
	SuppressNoEdits        SuppressReason = "no-edits"
	SuppressDisabledScope  SuppressReason = "disabled-scope"
	SuppressSingleDeletion SuppressReason = "single-deletion"
	SuppressLowConfidence  SuppressReason = "low-confidence"
	SuppressRejectionCache SuppressReason = "rejection-cache"
	SuppressStale          SuppressReason = "stale"
	SuppressMode           SuppressReason = "mode"
)

const (
	// consecutiveDeletionThreshold is the number of consecutive deletion
	// changes after which completions are re-enabled (user is rewriting, not
	// correcting).
	consecutiveDeletionThreshold = 3

	rejectedCompletionTTL = 30 * time.Second
)

// logSuppressed is the single Info-level sink for suppression outcomes.
func logSuppressed(reason SuppressReason, detail string) {
	if detail != "" {
		logger.Info("suppressed: %s (%s)", reason, detail)
		return
	}
	logger.Info("suppressed: %s", reason)
}

// suppressRequest gates a request before the provider is called. Manual
// triggers bypass every gate.
func (e *Engine) suppressRequest(source Source, manual bool) (SuppressReason, string) {
	if manual {
		return SuppressNone, ""
	}
	if !e.isModeEnabled() {
		return SuppressMode, ""
	}
	if source == SourceIdle && e.suppressForNoEdits() {
		return SuppressNoEdits, ""
	}
	if scope := e.suppressForDisabledScope(); scope != "" {
		return SuppressDisabledScope, scope
	}
	if source == SourceTyping && e.suppressForSingleDeletion() {
		return SuppressSingleDeletion, ""
	}
	return SuppressNone, ""
}

// suppressForNoEdits reports whether the buffer has not changed since the
// last save or open. Files that skip history (e.g. COMMIT_EDITMSG) are never
// suppressed.
func (e *Engine) suppressForNoEdits() bool {
	if e.buffer.SkipHistory() {
		return false
	}
	return !e.buffer.IsModified()
}

// suppressForDisabledScope returns the matched scope name when the cursor sits
// inside a treesitter scope listed in DisabledIn.
func (e *Engine) suppressForDisabledScope() string {
	if len(e.config.DisabledIn) == 0 {
		return ""
	}
	scopes := e.buffer.CursorScopes()
	if len(scopes) == 0 {
		return ""
	}
	disabled := make(map[string]bool, len(e.config.DisabledIn))
	for _, s := range e.config.DisabledIn {
		disabled[s] = true
	}
	for _, scope := range scopes {
		if disabled[scope] {
			return scope
		}
	}
	return ""
}

// suppressForSingleDeletion reports whether the last text change was a
// single-deletion correction (a streak shorter than the rewrite threshold).
func (e *Engine) suppressForSingleDeletion() bool {
	return e.deletionStreak > 0 && e.deletionStreak < consecutiveDeletionThreshold
}

// updateDeletionStreak classifies an incoming text_changed payload into the
// deletion streak. Deltas carry no old line content, so a same-line change is
// classified by cursor movement: a cursor that stayed or moved left deleted
// text, a cursor that moved right inserted it. A cursor move without a text
// change resets the streak, matching "the last action was a deletion".
func (e *Engine) updateDeletionStreak(payload map[string]any) {
	// The classifier consumes the previous cursor position, then adopts this
	// payload's position as the previous one for the next change.
	defer func() {
		if row, col, ok := payloadPosition(payload); ok {
			e.prevRow, e.prevCol, e.hasCursorPos = row, col, true
		}
	}()

	changed, ok := payload["changed"].(map[string]any)
	if !ok {
		e.deletionStreak = 0
		return
	}
	first, okFirst := payloadInt(changed["first"])
	lastOld, okOld := payloadInt(changed["last_old"])
	lastNew, okNew := payloadInt(changed["last_new"])
	if !okFirst || !okOld || !okNew {
		e.deletionStreak = 0
		return
	}
	oldCount := lastOld - first
	newCount := lastNew - first
	switch {
	case newCount > oldCount:
		e.deletionStreak = 0
	case newCount < oldCount:
		e.deletionStreak++
	case oldCount == 1:
		row, col, hasPos := payloadPosition(payload)
		if hasPos && e.hasCursorPos && row == e.prevRow && col <= e.prevCol {
			e.deletionStreak++
		} else {
			e.deletionStreak = 0
		}
	default:
		e.deletionStreak = 0
	}
}

// suppressLowConfidence drops a completion whose provider-reported mean token
// logprob is below the configured floor. Mean logprobs are always <= 0, so a
// floor that gates is negative: 0 disables. A provider without logprobs leaves
// Confidence nil and is never dropped.
func (e *Engine) suppressLowConfidence(response *types.CompletionResponse, manual bool) bool {
	if manual || e.config.MinConfidence >= 0 || response.Confidence == nil {
		return false
	}
	if *response.Confidence >= e.config.MinConfidence {
		return false
	}
	logSuppressed(SuppressLowConfidence,
		fmt.Sprintf("%.2f < %.2f", *response.Confidence, e.config.MinConfidence))
	return true
}

// rejectedCompletion is a cached rejection key: the normalized content hash of
// what the user saw, scoped to one file with a TTL.
type rejectedCompletion struct {
	filePath    string
	contentHash uint64
	expiresAt   time.Time
}

// rejectedCompletionFor builds a cache candidate from a completion.
func (e *Engine) rejectedCompletionFor(comp *types.Completion) *rejectedCompletion {
	if comp == nil {
		return nil
	}
	return &rejectedCompletion{
		filePath:    e.buffer.Path(),
		contentHash: completionContentHash(comp.Lines),
	}
}

// rejectedCompletionForStage builds a candidate from a stage, used by
// cursor-target-only render paths that did not show ghost text.
func (e *Engine) rejectedCompletionForStage(stage *text.Stage) *rejectedCompletion {
	if stage == nil {
		return nil
	}
	return e.rejectedCompletionFor(&types.Completion{Lines: stage.Lines})
}

// suppressRejectedCompletionForStage reports whether the stage repeats content
// the user rejected in this file within the TTL.
func (e *Engine) suppressRejectedCompletionForStage(stage *text.Stage, manual bool) bool {
	if manual || stage == nil {
		return false
	}
	candidate := e.rejectedCompletionForStage(stage)
	if candidate == nil {
		return false
	}
	e.pruneRejections(candidate.filePath)
	if _, rejected := e.rejectedCompletions[candidate.filePath][candidate.contentHash]; rejected {
		logSuppressed(SuppressRejectionCache, "")
		return true
	}
	return false
}

// rememberRejectedCompletion caches the displayed completion so future
// identical completions are suppressed. Called only when the user rejects.
func (e *Engine) rememberRejectedCompletion() {
	candidate := e.display.rejectCandidate
	if candidate == nil {
		return
	}
	file := e.rejectedCompletions[candidate.filePath]
	if file == nil {
		file = make(map[uint64]time.Time)
		e.rejectedCompletions[candidate.filePath] = file
	}
	e.pruneRejections(candidate.filePath)
	file[candidate.contentHash] = e.clock.Now().Add(rejectedCompletionTTL)
	e.display.rejectCandidate = nil
}

// forgetRejectedCompletions drops the rejection cache for a file. Called on
// accept: the user moved forward, cached keys are stale.
func (e *Engine) forgetRejectedCompletions(filePath string) {
	if filePath == "" {
		return
	}
	delete(e.rejectedCompletions, filePath)
}

func (e *Engine) pruneRejections(filePath string) {
	file := e.rejectedCompletions[filePath]
	if len(file) == 0 {
		return
	}
	now := e.clock.Now()
	for hash, expiresAt := range file {
		if !now.Before(expiresAt) {
			delete(file, hash)
		}
	}
}

// completionContentHash hashes the normalized content of completion lines.
// Trailing whitespace and trailing blank lines do not change the hash.
func completionContentHash(lines []string) uint64 {
	h := fnv.New64a()
	for _, line := range normalizeCompletionLines(lines) {
		h.Write([]byte(line))
		h.Write([]byte{'\n'})
	}
	return h.Sum64()
}

func normalizeCompletionLines(lines []string) []string {
	normalized := make([]string, len(lines))
	allEmpty := true
	for i, line := range lines {
		normalized[i] = strings.TrimRight(line, " \t")
		if normalized[i] != "" {
			allEmpty = false
		}
	}
	if allEmpty {
		return normalized
	}
	for len(normalized) > 0 && normalized[len(normalized)-1] == "" {
		normalized = normalized[:len(normalized)-1]
	}
	return normalized
}

func payloadInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

func payloadPosition(payload map[string]any) (row, col int, ok bool) {
	row, okRow := payloadInt(payload["row"])
	col, okCol := payloadInt(payload["col"])
	return row, col, okRow && okCol
}
