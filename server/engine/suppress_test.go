package engine

import (
	"fmt"
	"testing"
	"time"

	"cursortab/assert"
	"cursortab/types"
)

// textChangePayload builds a text_changed payload for a same-line replacement
// with the given post-change cursor column.
func textChangePayload(row, col int) map[string]any {
	return map[string]any{
		"changed": map[string]any{
			"first":    int64(row - 1),
			"last_old": int64(row),
			"last_new": int64(row),
		},
		"row": int64(row),
		"col": int64(col),
	}
}

func lineCountChangePayload(row, oldCount, newCount int) map[string]any {
	return map[string]any{
		"changed": map[string]any{
			"first":    int64(row - 1),
			"last_old": int64(row - 1 + oldCount),
			"last_new": int64(row - 1 + newCount),
		},
		"row": int64(row),
		"col": int64(0),
	}
}

func TestSuppressForSingleDeletion(t *testing.T) {
	e := &Engine{config: EngineConfig{}}
	e.prevRow, e.prevCol, e.hasCursorPos = 1, 5, true

	// No text change → no suppress
	e.updateDeletionStreak(map[string]any{"row": int64(1), "col": int64(5)})
	assert.False(t, e.suppressForSingleDeletion(), "no text change")

	// Insertion: cursor advanced → streak resets
	e.updateDeletionStreak(textChangePayload(1, 6))
	assert.False(t, e.suppressForSingleDeletion(), "insertion")

	// Single deletion: backspace, cursor moved left → suppress
	e.updateDeletionStreak(textChangePayload(1, 5))
	assert.True(t, e.suppressForSingleDeletion(), "single backspace")

	// Second consecutive deletion still suppresses
	e.updateDeletionStreak(textChangePayload(1, 4))
	assert.True(t, e.suppressForSingleDeletion(), "two backspaces")

	// Third consecutive deletion means rewriting → allow
	e.updateDeletionStreak(textChangePayload(1, 3))
	assert.False(t, e.suppressForSingleDeletion(), "three backspaces = rewrite")

	// An insertion resets the streak before the next scenario
	e.updateDeletionStreak(textChangePayload(1, 4))
	assert.False(t, e.suppressForSingleDeletion(), "insertion resets the streak")

	// Newline removal (line count shrinks) counts as a deletion
	e.updateDeletionStreak(lineCountChangePayload(1, 2, 1))
	assert.True(t, e.suppressForSingleDeletion(), "line merge deletion")

	// Newline insertion (line count grows) is an insertion
	e.updateDeletionStreak(lineCountChangePayload(1, 1, 2))
	assert.False(t, e.suppressForSingleDeletion(), "newline insertion")

	// A full resync payload without a delta clears the streak
	e.updateDeletionStreak(map[string]any{"full": map[string]any{"lines": []any{"x"}}})
	assert.False(t, e.suppressForSingleDeletion(), "full resync")
}

func TestRejectedCompletion_ExactContentHashKeysSuppression(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	comp := &types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}

	assert.Equal(t, completionShown, eng.processCompletionWithManual(completionResponse(comp), false), "initial completion shown")
	assert.Equal(t, 1, buf.prepareCompletionCalls, "initial render count")

	eng.doReject()

	// Identical normalized content → suppressed
	assert.Equal(t, completionSuppressed, eng.processCompletionWithManual(completionResponse(comp), false),
		"identical rejected completion suppressed")
	assert.Equal(t, 1, buf.prepareCompletionCalls, "suppressed completion should not render")
	assert.Equal(t, stateIdle, eng.state, "state after suppression")

	// Different content is a different hash → allowed again
	different := &types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world!"},
	}
	assert.Equal(t, completionShown, eng.processCompletionWithManual(completionResponse(different), false),
		"changed content is not suppressed by an unrelated rejection")
}

func TestRejectedCompletion_ManualTriggerBypassesCache(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	comp := &types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}

	assert.Equal(t, completionShown, eng.processCompletionWithManual(completionResponse(comp), false), "initial completion shown")
	eng.doReject()

	assert.Equal(t, completionShown, eng.processCompletionWithManual(completionResponse(comp), true),
		"manual trigger bypasses rejection cache")
	assert.Equal(t, 2, buf.prepareCompletionCalls, "manual trigger should render completion")
}

func TestRejectedCompletion_ExpiresAfterTTL(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	comp := &types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}

	assert.Equal(t, completionShown, eng.processCompletionWithManual(completionResponse(comp), false), "initial completion shown")
	eng.doReject()
	clock.Advance(rejectedCompletionTTL + time.Second)

	assert.Equal(t, completionShown, eng.processCompletionWithManual(completionResponse(comp), false),
		"expired rejection should not suppress completion")
	assert.Equal(t, 2, buf.prepareCompletionCalls, "completion should render after ttl")
}

func TestRejectedCompletion_TypingMismatchCachesRejection(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	comp := &types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}

	assert.Equal(t, completionShown, eng.processCompletionWithManual(completionResponse(comp), false), "initial completion shown")

	buf.lines = []string{"hello x"}
	buf.col = 7
	eng.handleTextChangeImpl()

	buf.lines = []string{"hello"}
	buf.col = 5
	assert.Equal(t, completionSuppressed, eng.processCompletionWithManual(completionResponse(comp), false),
		"typed-over completion should be cached as rejected")
	assert.Equal(t, 1, buf.prepareCompletionCalls, "typed-over completion should not rerender")
}

func TestRejectedCompletion_PureInsertionSuppresses(t *testing.T) {
	buf := newMockBuffer()
	// Empty line inside a scope: cursor sitting on a blank line.
	buf.lines = []string{"def foo():", "", "bar = 1"}
	buf.row = 2
	buf.col = 0
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	comp := &types.Completion{
		StartLine:  2,
		EndLineInc: 2,
		Lines:      []string{`    print("hi")`},
	}

	assert.Equal(t, completionShown, eng.processCompletionWithManual(completionResponse(comp), false), "initial completion shown")
	eng.doReject()

	assert.Equal(t, completionSuppressed, eng.processCompletionWithManual(completionResponse(comp), false),
		"same completion into empty line should be suppressed")
}

func TestRejectedCompletion_AcceptClearsCache(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	comp := &types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}

	assert.Equal(t, completionShown, eng.processCompletionWithManual(completionResponse(comp), false), "initial completion shown")
	eng.doReject()

	// Simulate an accept in the same file (unrelated completion).
	eng.forgetRejectedCompletions(buf.Path())

	assert.Equal(t, completionShown, eng.processCompletionWithManual(completionResponse(comp), false),
		"accept should clear rejection cache so identical completion is shown again")
}

// Multi-stage completions are stored at stage granularity: suppression is
// checked against the first stage, which is what the user actually saw.
func TestRejectedCompletion_MultiStageMatchesOnFirstStage(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{
		"function a() {",
		"  return 1;",
		"}",
		"",
		"function b() {",
		"  return 2;",
		"}",
	}
	buf.row = 2
	buf.col = 0
	buf.viewportTop = 1
	buf.viewportBottom = 20
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	multiRegion := func() *types.Completion {
		return &types.Completion{
			StartLine:  1,
			EndLineInc: 7,
			Lines: []string{
				"function a() {",
				"  return 10;",
				"}",
				"",
				"function b() {",
				"  return 20;",
				"}",
			},
		}
	}

	assert.Equal(t, completionShown, eng.processCompletionWithManual(completionResponse(multiRegion()), false), "initial multi-stage shown")
	assert.Equal(t, 2, len(eng.stagedCompletion.Stages), "produces two stages")

	eng.doReject()

	assert.Equal(t, completionSuppressed, eng.processCompletionWithManual(completionResponse(multiRegion()), false),
		"identical multi-stage completion suppressed via first-stage match")
}

// A pure-deletion completion (Lines is empty, oldLines carries the text being
// removed) is still cached on rejection.
func TestRejectedCompletion_PureDeletionCached(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"keep this", "drop this", "and keep this"}
	buf.row = 2
	buf.col = 0
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	deletion := func() *types.Completion {
		return &types.Completion{
			StartLine:  2,
			EndLineInc: 2,
			Lines:      []string{},
		}
	}

	assert.Equal(t, completionShown, eng.processCompletionWithManual(completionResponse(deletion()), false), "initial deletion shown")
	eng.doReject()

	assert.Equal(t, completionSuppressed, eng.processCompletionWithManual(completionResponse(deletion()), false),
		"pure-deletion completion is suppressed after rejection")
}

func TestRejectedCompletion_NewlineDeletionCached(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"if condition:", "    pass"}
	buf.row = 1
	buf.col = len("if condition:")
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	removeNewline := func() *types.Completion {
		return &types.Completion{
			StartLine:  1,
			EndLineInc: 2,
			Lines:      []string{"if condition:    pass"},
		}
	}

	assert.Equal(t, completionShown, eng.processCompletionWithManual(completionResponse(removeNewline()), false), "initial newline-deletion shown")
	eng.doReject()

	assert.Equal(t, completionSuppressed, eng.processCompletionWithManual(completionResponse(removeNewline()), false),
		"newline deletion should be suppressed after rejection")
}

// Typing while a cursor target is shown is the equivalent of pressing Esc on
// a regular completion: the candidate captured at the cursor target is cached
// so the same prediction doesn't immediately re-pop.
func TestRejectedCompletion_CursorTargetTypingCachesRejection(t *testing.T) {
	buf := newMockBuffer()
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	buf.lines = lines
	buf.row = 1
	buf.col = 0
	buf.viewportTop = 1
	buf.viewportBottom = 30
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	comp := &types.Completion{
		StartLine:  10,
		EndLineInc: 10,
		Lines:      []string{"line 10 modified"},
	}

	assert.Equal(t, completionShown, eng.processCompletionWithManual(completionResponse(comp), false), "initial cursor target shown")
	assert.Equal(t, stateHasCursorTarget, eng.state, "should be in cursor target state")
	assert.NotNil(t, eng.display.rejectCandidate, "candidate captured for cursor target")

	// EventTextChanged from stateHasCursorTarget is dispatched as
	// doRejectAndDebounce by the state machine.
	eng.doRejectAndDebounce()

	assert.Equal(t, completionSuppressed, eng.processCompletionWithManual(completionResponse(comp), false),
		"typing during cursor target should cache the rejection")
}

// Tab on a cursor target is forward progress like accepting a regular
// completion: the file's rejection cache is invalidated.
func TestRejectedCompletion_AcceptCursorTargetClearsCache(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 0
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.rejectedCompletions[buf.Path()] = map[uint64]time.Time{
		completionContentHash([]string{"hello world"}): clock.Now().Add(time.Minute),
	}

	eng.state = stateHasCursorTarget
	eng.cursorTarget = &types.CursorPredictionTarget{
		LineNumber: 1,
	}

	eng.acceptCursorTarget()

	assert.Nil(t, eng.rejectedCompletions[buf.Path()],
		"Tab on cursor target should clear the file's rejection cache")
}
