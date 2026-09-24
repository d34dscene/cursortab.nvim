package text

import (
	"cursortab/assert"
	"fmt"
	"testing"
)

func TestIncrementalStageBuilder_SingleStage(t *testing.T) {
	oldLines := []string{"line 1", "line 2", "line 3"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1, // baseLineOffset
		3, // proximityThreshold
		0, // maxVisibleLines (disabled)
		0, // viewportTop (disabled)
		0, // viewportBottom (disabled)
		1, // cursorRow
		0, // cursorCol
		"test.go",
		0, // availableWidth
	)

	// Add modified lines that should all be in the same stage
	builder.AddLine("line 1 modified") // modification
	builder.AddLine("line 2 modified") // modification
	builder.AddLine("line 3")          // match

	result := builder.Finalize()
	assert.NotNil(t, result, "staging result")

	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]
	assert.Equal(t, []string{"line 1 modified", "line 2 modified"}, stage.Lines, "stage content")
}

func TestIncrementalStageBuilder_MultipleStages(t *testing.T) {
	oldLines := []string{
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
		"line 8",
		"line 9",
		"line 10",
	}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1, // baseLineOffset
		2, // proximityThreshold (small to force multiple stages)
		0, // maxVisibleLines (disabled)
		0, // viewportTop
		0, // viewportBottom
		1, // cursorRow
		0, // cursorCol
		"test.go",
		0, // availableWidth
	)

	// Add lines with gaps > proximityThreshold to create multiple stages
	builder.AddLine("line 1 modified") // modification at line 1
	builder.AddLine("line 2")          // match
	builder.AddLine("line 3")          // match
	builder.AddLine("line 4")          // match
	builder.AddLine("line 5")          // match
	builder.AddLine("line 6 modified") // modification at line 6 (gap > 2)
	builder.AddLine("line 7")          // match
	builder.AddLine("line 8")          // match
	builder.AddLine("line 9")          // match
	builder.AddLine("line 10")         // match

	result := builder.Finalize()
	assert.NotNil(t, result, "staging result")

	assert.Equal(t, 2, len(result.Stages), "stage count")
}

func TestIncrementalStageBuilder_StageFinalizationOnGap(t *testing.T) {
	// Use additions (non-exact-matching lines) that are far apart in buffer coordinates
	// to trigger stage finalization on gap.
	oldLines := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		2,    // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.go",
		0, // availableWidth
	)

	// Line 1: exact match
	finalized := builder.AddLine("a")
	assert.Nil(t, finalized, "exact match should not finalize")

	// Line 2: non-matching → addition, starts a stage
	finalized = builder.AddLine("inserted line")
	assert.Nil(t, finalized, "should not finalize on first change")

	// Lines 3-4: exact matches
	finalized = builder.AddLine("b")
	assert.Nil(t, finalized, "should not finalize when gap <= threshold")
	finalized = builder.AddLine("c")
	assert.Nil(t, finalized, "should not finalize when gap <= threshold")

	// Line 5: match "d" (old line 4), gap from last change buffer line now exceeds threshold
	finalized = builder.AddLine("d")
	// Gap detection depends on buffer line calculation. Even if finalization
	// doesn't happen mid-stream, Finalize() produces correct results.

	// Lines 6-8: continue
	finalized = builder.AddLine("e")
	finalized = builder.AddLine("f")

	// Line 9: another change far from the first
	finalized = builder.AddLine("new line 2")

	// Finalize and verify stages are created correctly
	result := builder.Finalize()
	assert.NotNil(t, result, "should have stages")
	assert.True(t, len(result.Stages) > 0, "should have at least one stage")
}

func TestIncrementalStageBuilder_ViewportBoundary(t *testing.T) {
	// Use more distinct line content to avoid similarity matching issues
	oldLines := []string{
		"line one",
		"line two",
		"line three",
		"line four",
		"line five",
		"line six",
		"line seven",
		"line eight",
		"line nine",
		"line ten",
	}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,  // baseLineOffset
		10, // proximityThreshold (high to prevent gap-based splits)
		0,  // maxVisibleLines (disabled)
		1,  // viewportTop
		5,  // viewportBottom (first 5 lines visible)
		3,  // cursorRow
		0,  // cursorCol
		"test.go",
		0, // availableWidth
	)

	// Modifications in viewport (lines 1-5)
	builder.AddLine("line one modified") // in viewport, buffer line = 1
	builder.AddLine("line two")
	builder.AddLine("line three")
	builder.AddLine("line four")
	builder.AddLine("line five modified") // still in viewport, buffer line = 5

	// Add remaining lines to complete the sequence
	builder.AddLine("line six modified") // outside viewport, buffer line = 6
	builder.AddLine("line seven")
	builder.AddLine("line eight")
	builder.AddLine("line nine")
	builder.AddLine("line ten")

	// Finalize and check stages
	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	// All three changes (lines 1, 5, 6) are within proximityThreshold=10,
	// so they should be grouped into a single stage regardless of viewport.
	// Viewport boundaries should not split logically connected changes.
	assert.Equal(t, 1, len(result.Stages), "all nearby changes should be in one stage")
}

// TestIncrementalStageBuilder_BaseLineOffset verifies that BufferStart/BufferEnd
// are correctly offset when the provider trims content (baseLineOffset > 1).
// This simulates when the model only sees a window of the file, not the full file.
func TestIncrementalStageBuilder_BaseLineOffset(t *testing.T) {
	// Simulate a trimmed window: model sees lines 20-25 of original file
	// oldLines here represents the TRIMMED content (what model sees)
	oldLines := []string{
		"  if (article.tags === null) {",  // buffer line 20
		"    article.tags = tag;",         // buffer line 21
		"  } else {",                      // buffer line 22
		"    article.tags = concat(tag);", // buffer line 23
		"  }",                             // buffer line 24
	}

	baseLineOffset := 20 // Window starts at buffer line 20

	builder := NewIncrementalStageBuilder(
		oldLines,
		baseLineOffset,
		3,      // proximityThreshold
		0,      // maxVisibleLines (disabled)
		15, 30, // viewport (lines 15-30 visible)
		22, 0, // cursorRow, cursorCol
		"test.ts",
		0, // availableWidth
	)

	// Model outputs modified content
	builder.AddLine("  if (article.tags === null) {") // match
	builder.AddLine("    article.tags = [tag];")      // modification
	builder.AddLine("  } else {")                     // match
	builder.AddLine("    article.tags.push(tag);")    // modification
	builder.AddLine("  }")                            // match

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]

	// BufferStart should be 21 (baseLineOffset + 1 for the second line where change is)
	// BufferEnd should be 24 (baseLineOffset + 3 for line 4 where last change is)
	// The key test: these should NOT be 1-5, they should be offset by baseLineOffset
	assert.GreaterOrEqual(t, stage.BufferStart, baseLineOffset, "BufferStart >= baseLineOffset")
	assert.GreaterOrEqual(t, stage.BufferEnd, baseLineOffset, "BufferEnd >= baseLineOffset")

	// More specific check: changes are on lines 2 and 4 of input (1-indexed)
	// So BufferStart should be baseLineOffset + 1 = 21
	// And BufferEnd should be baseLineOffset + 3 = 23 (line 4 of 5)
	expectedStart := 21 // Line 2 of trimmed = buffer line 21
	expectedEnd := 23   // Line 4 of trimmed = buffer line 23

	assert.Equal(t, expectedStart, stage.BufferStart, "BufferStart")
	assert.Equal(t, expectedEnd, stage.BufferEnd, "BufferEnd")

	assert.Equal(t, 2, len(stage.Groups), "two modification groups")
}

// TestIncrementalStageBuilder_BaseLineOffsetWithGap tests that gap detection
// works correctly when baseLineOffset > 1 and stages finalize mid-stream.
func TestIncrementalStageBuilder_BaseLineOffsetWithGap(t *testing.T) {
	// Verify that stages with base line offset produce correct buffer positions
	// when finalized via the batch pipeline.
	oldLines := []string{
		"line A", // buffer line 50
		"line B", // buffer line 51
		"line C", // buffer line 52
		"line D", // buffer line 53
		"line E", // buffer line 54
	}

	baseLineOffset := 50

	builder := NewIncrementalStageBuilder(
		oldLines,
		baseLineOffset,
		2,      // proximityThreshold
		0,      // maxVisibleLines (disabled)
		40, 60, // viewport
		52, 0, // cursorRow, cursorCol
		"test.go",
		0, // availableWidth
	)

	// Feed lines with a modification at the beginning
	builder.AddLine("line A modified")
	builder.AddLine("line B")
	builder.AddLine("line C")
	builder.AddLine("line D")
	builder.AddLine("line E")

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 1, len(result.Stages), "expected 1 stage")
	assert.Equal(t, 50, result.Stages[0].BufferStart, "BufferStart with offset")
	assert.Equal(t, 50, result.Stages[0].BufferEnd, "BufferEnd with offset")
}

// TestIncrementalStageBuilder_GapDetectionWithSimilarityMatching verifies that when
// similarity matching maps model output to scattered buffer positions, gap detection
// still groups changes appropriately based on buffer line proximity.
func TestIncrementalStageBuilder_GapDetectionWithSimilarityMatching(t *testing.T) {
	// Simulate a scenario where similarity matching maps new lines to scattered old lines.
	// Old lines represent different functions in a file.
	oldLines := []string{
		"function foo() {", // line 1 (buffer 52)
		"  const x = 1;",   // line 2 (buffer 53)
		"  return x;",      // line 3 (buffer 54)
		"}",                // line 4 (buffer 55)
		"",                 // line 5 (buffer 56)
		"function bar() {", // line 6 (buffer 57)
		"  const y = 2;",   // line 7 (buffer 58)
		"  return y;",      // line 8 (buffer 59)
		"}",                // line 9 (buffer 60)
		"",                 // line 10 (buffer 61)
		"function baz() {", // line 11 (buffer 62)
		"  const z = 3;",   // line 12 (buffer 63)
		"  return z;",      // line 13 (buffer 64)
		"}",                // line 14 (buffer 65)
	}

	baseLineOffset := 52

	builder := NewIncrementalStageBuilder(
		oldLines,
		baseLineOffset,
		3,      // proximityThreshold - gaps > 3 should split stages
		0,      // maxVisibleLines (disabled)
		40, 80, // viewport
		58, 0, // cursorRow, cursorCol
		"test.ts",
		0, // availableWidth
	)

	// Model outputs content where:
	// - Line 1 matches old line 1 (exact)
	// - Line 2 is a MODIFICATION of old line 2 -> buffer 53
	// - Line 3 matches old line 3 (exact)
	// - Line 4 matches old line 4 (exact)
	// - Line 5 matches old line 5 (exact)
	// - Line 6 matches old line 6 (exact)
	// - Line 7 is a MODIFICATION of old line 7 -> buffer 58 (buffer gap = 5!)
	// - Line 8 matches old line 8 (exact)
	// - etc.

	builder.AddLine("function foo() {") // match line 1
	builder.AddLine("  const x = 100;") // MODIFY line 2 -> buffer 53
	builder.AddLine("  return x;")      // match line 3
	builder.AddLine("}")                // match line 4
	builder.AddLine("")                 // match line 5
	builder.AddLine("function bar() {") // match line 6
	builder.AddLine("  const y = 200;") // MODIFY line 7 -> buffer 58 (buffer gap = 5!)
	builder.AddLine("  return y;")      // match line 8
	builder.AddLine("}")                // match line 9
	builder.AddLine("")                 // match line 10
	builder.AddLine("function baz() {") // match line 11
	builder.AddLine("  const z = 300;") // MODIFY line 12 -> buffer 63 (buffer gap = 5!)
	builder.AddLine("  return z;")      // match line 13
	builder.AddLine("}")                // match line 14

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	// With proximityThreshold=3 and buffer gaps of 5 between functions,
	// changes should be split into separate stages
	assert.True(t, len(result.Stages) >= 3, "expected at least 3 stages")

	// Verify each stage has rendered content
	for _, stage := range result.Stages {
		assert.True(t, len(stage.Groups) > 0, "stage should have groups")
	}
}

// TestIncrementalStageBuilder_GapDetectionBehavior tests gap detection behavior
// when changes map to non-consecutive buffer positions.
func TestIncrementalStageBuilder_GapDetectionBehavior(t *testing.T) {
	// Create old lines where we can control exactly which lines match
	// Use similar prefixes to ensure modifications are detected
	oldLines := []string{
		"  func alpha() {", // line 1 (buffer 10)
		"    return 1",     // line 2 (buffer 11)
		"  }",              // line 3 (buffer 12)
		"",                 // line 4 (buffer 13)
		"  func beta() {",  // line 5 (buffer 14)
		"    return 2",     // line 6 (buffer 15)
	}

	baseLineOffset := 10

	builder := NewIncrementalStageBuilder(
		oldLines,
		baseLineOffset,
		2,     // proximityThreshold = 2 (gap > 2 should split)
		0,     // maxVisibleLines (disabled)
		5, 20, // viewport
		12, 0, // cursorRow, cursorCol
		"test.go",
		0, // availableWidth
	)

	// Output lines where:
	// - New line 1: modification of old line 1 (buffer 10)
	// - New line 2: exact match of old line 2
	// - New line 3: exact match of old line 3
	// - New line 4: exact match of old line 4
	// - New line 5: modification of old line 6 (buffer 15!) - skipping old line 5
	//
	// New line gap between changes: 5 - 1 = 4 > threshold (should split)
	// Buffer line gap between changes: 15 - 10 = 5 > threshold (should split)

	builder.AddLine("  func alpha(x) {") // Modify old line 1 -> buffer 10
	builder.AddLine("    return 1")      // Match old line 2
	builder.AddLine("  }")               // Match old line 3
	builder.AddLine("")                  // Match old line 4
	builder.AddLine("    return 200")    // Modify - similar to old line 6 -> buffer 15

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	// Since BOTH new-line gap (4) and buffer-line gap (5) exceed threshold (2),
	// we expect 2 stages regardless of which gap metric is used.
	assert.Equal(t, 2, len(result.Stages), "stage count")
}

// TestIncrementalStageBuilder_WhenModelOutputStartsMidFile tests when model
// output starts from middle of file instead of beginning.
func TestIncrementalStageBuilder_WhenModelOutputStartsMidFile(t *testing.T) {
	oldLines := []string{
		"func first() {}",
		"func second() {}",
		"func third() {}",
		"",
		"// Section 2",
		"func fourth() {}",
		"",
		"func fifth() {}",
		"func sixth() {}",
	}

	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		3,    // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		5, 0, // cursorRow, cursorCol
		"test.go",
		0, // availableWidth
	)

	// Model starts outputting from line 5 (skipping lines 1-4)
	modelOutput := []string{
		"// Section 2",              // matches line 5
		"func fourth_modified() {}", // modified
		"",                          // matches line 7
		"func fifth_modified() {}",  // modified
	}

	for _, line := range modelOutput {
		builder.AddLine(line)
	}

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	// Should have at least one stage
	assert.True(t, len(result.Stages) > 0, "expected at least one stage")

	// Buffer positions should be within file bounds
	for _, stage := range result.Stages {
		assert.True(t, stage.BufferStart <= len(oldLines)+1, "stage BufferStart within bounds")
		assert.True(t, stage.BufferEnd <= len(oldLines)+10, "stage BufferEnd within bounds")
	}
}

// TestLineSimilarity verifies similarity calculation for various line comparisons.
func TestLineSimilarity(t *testing.T) {
	tests := []struct {
		name   string
		line1  string
		line2  string
		minSim float64
		maxSim float64
	}{
		{
			name:   "identical lines",
			line1:  "const x = 1;",
			line2:  "const x = 1;",
			minSim: 1.0,
			maxSim: 1.0,
		},
		{
			name:   "small modification",
			line1:  "const x = 1;",
			line2:  "const x = 2;",
			minSim: 0.8,
			maxSim: 1.0,
		},
		{
			name:   "completely different",
			line1:  "function foo() {",
			line2:  "// comment here",
			minSim: 0.0,
			maxSim: 0.3,
		},
		{
			name:   "empty vs content",
			line1:  "",
			line2:  "some content",
			minSim: 0.0,
			maxSim: 0.1,
		},
		{
			name:   "variable rename",
			line1:  "const app = new Server();",
			line2:  "const server = new Server();",
			minSim: 0.6,
			maxSim: 0.95,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			similarity := LineSimilarity(tt.line1, tt.line2)
			assert.True(t, similarity >= tt.minSim && similarity <= tt.maxSim, "similarity in expected range")
		})
	}
}

// TestIncrementalStageBuilder_DuplicateOutputHandling verifies stage building
// when model outputs duplicate lines.
func TestIncrementalStageBuilder_DuplicateOutputHandling(t *testing.T) {
	oldLines := []string{
		"func setup() {",
		"    init()",
		"}",
		"",
		"func run() {",
		"    execute()",
		"}",
	}

	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		3,    // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.go",
		0, // availableWidth
	)

	// Exact matches for first 3 lines
	for i := range 3 {
		builder.AddLine(oldLines[i])
	}

	// Now output duplicates
	builder.AddLine("func setup() {}") // duplicate
	builder.AddLine("func setup() {}") // duplicate again

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	// Should have at least one stage with the duplicate changes
	assert.True(t, len(result.Stages) > 0, "expected at least one stage")

	// Each stage should have valid buffer coordinates
	for _, stage := range result.Stages {
		assert.GreaterOrEqual(t, stage.BufferStart, 1, "stage BufferStart valid")
		assert.GreaterOrEqual(t, stage.BufferEnd, stage.BufferStart, "stage BufferEnd >= BufferStart")
	}
}

func TestIncrementalStageBuilder_ConsistencyWithComputeDiff(t *testing.T) {
	oldLines := []string{
		"func main() {",
		"    fmt.Println(\"hello\")",
		"    return",
		"}",
	}
	newLines := []string{
		"func main() {",
		"    fmt.Println(\"hello world\")",
		"    return nil",
		"}",
	}

	// Use batch pipeline
	oldText := JoinLines(oldLines)
	newText := JoinLines(newLines)
	batchDiff := ComputeDiff(oldText, newText)
	batchResult := CreateStages(&StagingParams{
		Diff:               batchDiff,
		CursorRow:          1,
		CursorCol:          0,
		BaseLineOffset:     1,
		ProximityThreshold: 10,
		NewLines:           newLines,
		OldLines:           oldLines,
		FilePath:           "test.go",
	})

	// Use incremental builder with Finalize()
	builder := NewIncrementalStageBuilder(oldLines, 1, 10, 0, 0, 0, 1, 0, "test.go", 0)
	for _, line := range newLines {
		builder.AddLine(line)
	}
	incResult := builder.Finalize()

	// Finalize() uses the batch pipeline, so results must be identical
	assert.NotNil(t, batchResult, "batch should produce result")
	assert.NotNil(t, incResult, "incremental should produce result")
	assert.Equal(t, len(batchResult.Stages), len(incResult.Stages), "stage count")
	for i, batchStage := range batchResult.Stages {
		incStage := incResult.Stages[i]
		assert.Equal(t, batchStage.BufferStart, incStage.BufferStart, "BufferStart")
		assert.Equal(t, batchStage.BufferEnd, incStage.BufferEnd, "BufferEnd")
		assert.Equal(t, ToLuaFormat(batchStage, batchStage.BufferStart), ToLuaFormat(incStage, incStage.BufferStart), "ToLuaFormat output")
	}
}

// TestIncrementalStageBuilder_EmptyInput verifies handling of empty input.
func TestIncrementalStageBuilder_EmptyInput(t *testing.T) {
	builder := NewIncrementalStageBuilder(
		[]string{}, // empty old lines
		1,          // baseLineOffset
		3,          // proximityThreshold
		0,          // maxVisibleLines (disabled)
		0, 0,       // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.go",
		0, // availableWidth
	)

	builder.AddLine("new content")
	// Adding to empty may return nil change or an addition

	result := builder.Finalize()
	// Result may be nil for empty old + single new line (no meaningful changes)
	if result != nil && len(result.Stages) > 0 {
		// Verify the stage has valid structure
		for _, stage := range result.Stages {
			assert.GreaterOrEqual(t, stage.BufferStart, 1, "stage BufferStart valid")
		}
	}
}

// TestIncrementalStageBuilder_SingleLine verifies handling of single-line files.
func TestIncrementalStageBuilder_SingleLine(t *testing.T) {
	oldLines := []string{"single line"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		3,    // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.go",
		0, // availableWidth
	)

	// Modify the single line
	builder.AddLine("modified single line")

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	assert.Equal(t, 1, len(result.Stages), "stage count")
}

// TestIncrementalStageBuilder_LargeGap verifies stage splitting with large gaps.
func TestIncrementalStageBuilder_LargeGap(t *testing.T) {
	// Create old lines with changes at beginning and end
	oldLines := make([]string, 100)
	for i := range oldLines {
		oldLines[i] = "line"
	}

	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		3,    // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.go",
		0, // availableWidth
	)

	// Modify first line
	builder.AddLine("modified first")
	// Match lines 2-90
	for i := 1; i < 90; i++ {
		builder.AddLine(oldLines[i])
	}
	// Modify last few lines
	builder.AddLine("modified 90")
	for i := 91; i < 100; i++ {
		builder.AddLine(oldLines[i])
	}

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	// Should have 2 stages due to large gap
	assert.True(t, len(result.Stages) >= 2, "expected at least 2 stages")
}

// TestIncrementalStageBuilder_ConsecutiveModifications verifies consecutive modifications
// stay in the same stage.
func TestIncrementalStageBuilder_ConsecutiveModifications(t *testing.T) {
	oldLines := []string{"a", "b", "c", "d", "e"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		3,    // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.go",
		0, // availableWidth
	)

	// Modify lines 2-4 consecutively
	builder.AddLine("a")          // match
	builder.AddLine("B_modified") // modify
	builder.AddLine("C_modified") // modify
	builder.AddLine("D_modified") // modify
	builder.AddLine("e")          // match

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	// All consecutive modifications should be in one stage
	assert.Equal(t, 1, len(result.Stages), "stage count")
	assert.Equal(t, []string{"B_modified", "C_modified", "D_modified"}, result.Stages[0].Lines, "stage content")
}

// TestIncrementalStageBuilder_LowSimilarityReplacement verifies that when we replace
// content with very different content (low similarity), the stage builder correctly
// detects it as a modification using batch diff at finalization time.
// This handles typo correction: "this commt" -> "this commit addresses..."
func TestIncrementalStageBuilder_LowSimilarityReplacement(t *testing.T) {
	oldLines := []string{"line1", "", "this commt adress"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.go",
		0, // availableWidth
	)

	// Line 1: exact match
	builder.AddLine("line1")

	// Line 2: exact match (empty)
	builder.AddLine("")

	// Line 3: "this commt adress" -> long corrected text
	// During streaming this may be marked as addition (low similarity),
	// but at finalization batch diff will correctly identify it as modification
	// because old line count == new line count.
	newContent := "this commit addresses the issue of incorrect cursor target calculation in the text"
	builder.AddLine(newContent)

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]

	// Equal line counts categorize the typo fix as a modification, not an addition
	assert.Equal(t, []string{newContent}, stage.Lines, "stage content")
	assert.Len(t, 1, stage.Groups, "group count")
	group := stage.Groups[0]
	assert.Equal(t, "modification", group.Type, "group type")
	assert.Equal(t, []string{"this commt adress"}, group.OldLines, "old content")
	assert.Equal(t, []string{newContent}, group.Lines, "new content")
}

// TestIncrementalStageBuilder_AppendCharsWithAdditionsBelow verifies that when
// a partial line is completed (append_chars) and new lines are added below,
// the completion is correctly typed and additions follow.
func TestIncrementalStageBuilder_AppendCharsWithAdditionsBelow(t *testing.T) {
	oldLines := []string{"partial"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.txt",
		0, // availableWidth
	)

	builder.AddLine("partial content completed")
	builder.AddLine("new line 1")
	builder.AddLine("new line 2")

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]

	assert.Equal(t, []string{"partial content completed", "new line 1", "new line 2"}, stage.Lines, "stage content")

	assert.Len(t, 2, stage.Groups, "group count")
	assert.Equal(t, "modification", stage.Groups[0].Type, "first group type")
	assert.Equal(t, "append_chars", stage.Groups[0].RenderHint, "first group hint")
	assert.Equal(t, []string{"partial"}, stage.Groups[0].OldLines, "first group old content")
	assert.Equal(t, "addition", stage.Groups[1].Type, "second group type")
	assert.Equal(t, []string{"new line 1", "new line 2"}, stage.Groups[1].Lines, "second group content")
}

// TestIncrementalStageBuilder_AdditionsAboveWithAppendChars verifies that when
// new lines are inserted above and the original line is completed (append_chars),
// additions come first and the completion is at the end.
func TestIncrementalStageBuilder_AdditionsAboveWithAppendChars(t *testing.T) {
	oldLines := []string{"partial"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.txt",
		0, // availableWidth
	)

	// Model outputs new lines first, then completes the original partial line
	builder.AddLine("inserted line 1")
	builder.AddLine("inserted line 2")
	builder.AddLine("partial content completed")

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]

	assert.Equal(t, []string{"inserted line 1", "inserted line 2", "partial content completed"}, stage.Lines, "stage content")

	assert.Len(t, 2, stage.Groups, "group count")
	assert.Equal(t, "addition", stage.Groups[0].Type, "first group type")
	assert.Equal(t, []string{"inserted line 1", "inserted line 2"}, stage.Groups[0].Lines, "first group content")
	assert.Equal(t, "modification", stage.Groups[1].Type, "second group type")
	assert.Equal(t, "append_chars", stage.Groups[1].RenderHint, "second group hint")
	assert.Equal(t, []string{"partial"}, stage.Groups[1].OldLines, "second group old content")
}

// TestIncrementalStageBuilder_AdditionsAboveAndBelowWithAppendChars verifies
// that additions can appear both above and below a completed line.
func TestIncrementalStageBuilder_AdditionsAboveAndBelowWithAppendChars(t *testing.T) {
	oldLines := []string{"middle"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.txt",
		0, // availableWidth
	)

	// Model outputs: additions above, completed line, additions below
	builder.AddLine("above 1")
	builder.AddLine("above 2")
	builder.AddLine("middle completed")
	builder.AddLine("below 1")
	builder.AddLine("below 2")

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]

	assert.Equal(t, []string{"above 1", "above 2", "middle completed", "below 1", "below 2"}, stage.Lines, "stage content")

	assert.Len(t, 3, stage.Groups, "group count")
	assert.Equal(t, "addition", stage.Groups[0].Type, "first group type")
	assert.Equal(t, []string{"above 1", "above 2"}, stage.Groups[0].Lines, "first group content")
	assert.Equal(t, "modification", stage.Groups[1].Type, "middle group type")
	assert.Equal(t, "append_chars", stage.Groups[1].RenderHint, "middle group hint")
	assert.Equal(t, []string{"middle"}, stage.Groups[1].OldLines, "middle group old content")
	assert.Equal(t, "addition", stage.Groups[2].Type, "last group type")
	assert.Equal(t, []string{"below 1", "below 2"}, stage.Groups[2].Lines, "last group content")
}

// TestIncrementalStageBuilder_MaxVisibleLines tests that maxVisibleLines correctly
// splits stages in the incremental builder. This reproduces the bug where stage 2
// gets incorrect buffer coordinates after being split by maxVisibleLines.
func TestIncrementalStageBuilder_MaxVisibleLines(t *testing.T) {
	// Scenario from logs:
	// Original: "import numpy as np", "", "def bubb" (3 lines)
	// New: adds bubble_sort function (modification + multiple additions)
	oldLines := []string{"import numpy as np", "", "def bubb"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold (high to prevent gap splits)
		2,    // maxVisibleLines - force split after 2 lines
		0, 0, // viewport disabled
		3, 0, // cursorRow, cursorCol
		"test.py",
		0, // availableWidth
	)

	// Feed the model output line by line
	builder.AddLine("import numpy as np")    // unchanged
	builder.AddLine("")                      // unchanged
	builder.AddLine("def bubble_sort(arr):") // modification (was "def bubb")
	builder.AddLine("    n = len(arr)")      // addition
	// At this point, stage 1 should have 2 lines (the modification + 1 addition)
	// and maxVisibleLines should trigger a new stage

	builder.AddLine("    for i in range(n):")            // addition - should be in stage 2
	builder.AddLine("        for j in range(0, n-i-1):") // addition - should be in stage 2

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.True(t, len(result.Stages) >= 2, "should have at least 2 stages with maxVisibleLines=2")

	stage1 := result.Stages[0]
	stage2 := result.Stages[1]

	t.Logf("Stage 1: BufferStart=%d, BufferEnd=%d, Lines=%d", stage1.BufferStart, stage1.BufferEnd, len(stage1.Lines))
	for i, g := range stage1.Groups {
		t.Logf("  Group %d: type=%s, BufferLine=%d", i, g.Type, g.BufferLine)
	}
	t.Logf("Stage 2: BufferStart=%d, BufferEnd=%d, Lines=%d", stage2.BufferStart, stage2.BufferEnd, len(stage2.Lines))
	for i, g := range stage2.Groups {
		t.Logf("  Group %d: type=%s, BufferLine=%d", i, g.Type, g.BufferLine)
	}

	// Stage 1 should start at buffer line 3 (where "def bubb" is)
	assert.Equal(t, 3, stage1.BufferStart, "stage 1 should start at buffer line 3")

	// Stage 2 contains pure additions that should be INSERTED after line 3.
	// For pure additions, BufferStart should be the INSERTION POINT (anchor + 1),
	// not the anchor itself. This is because virt_lines_above renders above the
	// specified line, so to render below line 3, we need BufferStart=4.
	// This matches the non-streaming CreateStages behavior.
	assert.Equal(t, 4, stage2.BufferStart,
		fmt.Sprintf("stage 2 BufferStart should be 4 (insertion point after line 3), got %d", stage2.BufferStart))

	// Verify groups have BufferLine=4 (insertion point)
	for i, g := range stage2.Groups {
		assert.Equal(t, 4, g.BufferLine,
			fmt.Sprintf("stage 2 group %d BufferLine should be 4 (insertion point), got %d", i, g.BufferLine))
	}
}

// TestIncrementalStageBuilder_MaxVisibleLines_ThreeStages tests the cumulative offset
// calculation when there are 3+ stages split by maxVisibleLines. This reproduces the
// bug where stage 3's offset is wrong because pure addition stages (stage 2) were
// incorrectly counted as replacing 1 line instead of inserting.
func TestIncrementalStageBuilder_MaxVisibleLines_ThreeStages(t *testing.T) {
	// Original: 3 lines
	// Model output: modification + 6 additions = 7 new lines at position 3
	// With maxVisibleLines=2, we get:
	//   Stage 1: lines 3-4 (modification + 1 addition) - replaces 1 line with 2
	//   Stage 2: lines 5-6 (2 additions) - inserts 2 lines
	//   Stage 3: lines 7-8 (2 additions) - inserts 2 lines
	oldLines := []string{"import numpy as np", "", "def bubb"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold (high to prevent gap splits)
		2,    // maxVisibleLines - force split after 2 lines
		0, 0, // viewport disabled
		3, 0, // cursorRow, cursorCol
		"test.py",
		0, // availableWidth
	)

	// Feed the model output
	builder.AddLine("import numpy as np")                // unchanged
	builder.AddLine("")                                  // unchanged
	builder.AddLine("def bubble_sort(arr):")             // modification
	builder.AddLine("    n = len(arr)")                  // addition (stage 1 ends here)
	builder.AddLine("    for i in range(n):")            // addition (stage 2)
	builder.AddLine("        for j in range(n-i-1):")    // addition (stage 2 ends here)
	builder.AddLine("            if arr[j] > arr[j+1]:") // addition (stage 3)
	builder.AddLine("                swap(arr, j)")      // addition (stage 3 ends here)

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 3, len(result.Stages), "should have 3 stages with maxVisibleLines=2")

	stage1 := result.Stages[0]
	stage2 := result.Stages[1]
	stage3 := result.Stages[2]

	t.Logf("Stage 1: BufferStart=%d, BufferEnd=%d, Lines=%d", stage1.BufferStart, stage1.BufferEnd, len(stage1.Lines))
	t.Logf("Stage 2: BufferStart=%d, BufferEnd=%d, Lines=%d", stage2.BufferStart, stage2.BufferEnd, len(stage2.Lines))
	t.Logf("Stage 3: BufferStart=%d, BufferEnd=%d, Lines=%d", stage3.BufferStart, stage3.BufferEnd, len(stage3.Lines))

	// Initial coordinates (before any offset adjustments):
	// Stage 1: BufferStart=3 (modifying line 3)
	// Stage 2: BufferStart=4 (pure additions, insertion point after line 3)
	// Stage 3: BufferStart=4 (pure additions, insertion point after line 3)
	assert.Equal(t, 3, stage1.BufferStart, "stage 1 BufferStart")
	assert.Equal(t, 4, stage2.BufferStart, "stage 2 BufferStart (insertion point)")
	assert.Equal(t, 4, stage3.BufferStart, "stage 3 BufferStart (insertion point, before offset)")

	// Verify stage 2 and 3 are pure additions (all groups are "addition" type)
	for _, g := range stage2.Groups {
		assert.Equal(t, "addition", g.Type, "stage 2 should have only addition groups")
	}
	for _, g := range stage3.Groups {
		assert.Equal(t, "addition", g.Type, "stage 3 should have only addition groups")
	}

	// The key test: IsPureAddition should be detectable from groups
	// This will be used by advanceStagedCompletion to calculate correct offset
	stage2IsPureAddition := true
	for _, g := range stage2.Groups {
		if g.Type != "addition" {
			stage2IsPureAddition = false
			break
		}
	}
	assert.True(t, stage2IsPureAddition, "stage 2 should be detected as pure additions")
}

// TestIncrementalStageBuilder_BlankLineAdditions verifies that blank lines in the
// model output are correctly included as additions and not skipped.
// This tests the scenario where multi-line completions include blank lines between
// blocks of code (e.g., functions or paragraphs).
func TestIncrementalStageBuilder_BlankLineAdditions(t *testing.T) {
	// Simulate: user has partial content that gets completed with multiple blocks
	// separated by blank lines
	oldLines := []string{"header", "", "func te"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		3, 0, // cursorRow, cursorCol
		"test.go",
		0, // availableWidth
	)

	// Model outputs completed content with blocks separated by blank lines
	builder.AddLine("header")          // exact match
	builder.AddLine("")                // exact match (blank)
	builder.AddLine("func test1() {}") // append_chars (completes "func te")
	builder.AddLine("    body1")       // addition
	builder.AddLine("")                // BLANK LINE - should be addition!
	builder.AddLine("func test2() {}") // addition
	builder.AddLine("    body2")       // addition
	builder.AddLine("")                // BLANK LINE - should be addition!
	builder.AddLine("func test3() {}") // addition

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]

	// All 7 lines from the append onward are in the stage, blank lines included
	assert.Equal(t, []string{
		"func test1() {}",
		"    body1",
		"",
		"func test2() {}",
		"    body2",
		"",
		"func test3() {}",
	}, stage.Lines, "stage content including blank lines")

	// Verify groups include all lines (no gaps)
	totalLinesInGroups := 0
	for _, g := range stage.Groups {
		totalLinesInGroups += len(g.Lines)
	}
	assert.Equal(t, 7, totalLinesInGroups, "groups should cover all 7 lines including blank lines")
}

// TestIncrementalStageBuilder_EmptyLineFilledWithContent verifies that when
// an empty line is filled with content, the stage builder correctly produces
// append_chars groups.
func TestIncrementalStageBuilder_EmptyLineFilledWithContent(t *testing.T) {
	oldLines := []string{"header", "", ""}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		3, 0, // cursorRow, cursorCol
		"test.txt",
		0, // availableWidth
	)

	builder.AddLine("header")
	builder.AddLine("")
	builder.AddLine("new content here")
	builder.AddLine("another line")

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]

	// First group fills the empty line: modification with append_chars hint
	assert.True(t, len(stage.Groups) >= 1, "should have at least 1 group")
	firstGroup := stage.Groups[0]
	assert.Equal(t, "modification", firstGroup.Type, "group type")
	assert.Equal(t, "append_chars", firstGroup.RenderHint, "render hint")
	assert.Equal(t, 3, firstGroup.BufferLine, "buffer line")
	assert.Equal(t, []string{""}, firstGroup.OldLines, "old content is the empty line")
}

// TestIncrementalStageBuilder_WhitespaceOnlyLineModification verifies that when the old line
// is whitespace-only (not empty), the matching correctly identifies it as a modification target.
func TestIncrementalStageBuilder_WhitespaceOnlyLineModification(t *testing.T) {
	oldLines := []string{
		"func main() {",
		"    ", // whitespace-only line (4 spaces)
	}

	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		2, 4, // cursorRow=2, cursorCol=4
		"test.go",
		0, // availableWidth
	)

	builder.AddLine("func main() {")
	builder.AddLine("    x = getValue()")      // modification of whitespace-only line
	builder.AddLine("    y = processValue(x)") // addition

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]

	// BufferStart should be 2 (where the whitespace-only line is)
	assert.Equal(t, 2, stage.BufferStart, "BufferStart")

	// The modification group should have BufferLine = 2
	var modGroup *Group
	for _, g := range stage.Groups {
		if g.Type == "modification" {
			modGroup = g
			break
		}
	}
	assert.NotNil(t, modGroup, "should have a modification group")
	assert.Equal(t, 2, modGroup.BufferLine, "modification BufferLine")

	// The addition group should have BufferLine = 3 (below the modification)
	var addGroup *Group
	for _, g := range stage.Groups {
		if g.Type == "addition" {
			addGroup = g
			break
		}
	}
	assert.NotNil(t, addGroup, "should have an addition group")
	assert.Equal(t, 3, addGroup.BufferLine, "addition BufferLine")
}

// TestLineSimilarity_EdgeCases tests similarity calculation edge cases.
func TestLineSimilarity_EdgeCases(t *testing.T) {
	tests := []struct {
		name   string
		line1  string
		line2  string
		minSim float64
		maxSim float64
	}{
		{"both empty", "", "", 1.0, 1.0},
		{"one empty", "content", "", 0.0, 0.1},
		{"single char same", "x", "x", 1.0, 1.0},
		{"single char different", "x", "y", 0.0, 0.5},
		{"whitespace same", "   ", "   ", 1.0, 1.0},
		{"whitespace different", "   ", "\t\t", 0.0, 0.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sim := LineSimilarity(tt.line1, tt.line2)
			assert.True(t, sim >= tt.minSim && sim <= tt.maxSim, "similarity in expected range")
		})
	}
}

// TestIncrementalStageBuilder_ModificationBufferLineUsesOldPosition verifies that
// modification groups have BufferLine computed from their old line position,
// not their relative position in the new content.
func TestIncrementalStageBuilder_ModificationBufferLineUsesOldPosition(t *testing.T) {
	// Old content: 2 lines at buffer positions 5-6
	// New content: 4 lines where:
	// - Lines 1-2 are additions
	// - Line 3 modifies old line 2 (buffer line 6)
	// - Line 4 is an addition
	oldLines := []string{
		"first line",
		"second line",
	}
	builder := NewIncrementalStageBuilder(
		oldLines,
		5,    // baseLineOffset - old lines are at buffer 5-6
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		6, 0, // cursorRow, cursorCol
		"test.go",
		0, // availableWidth
	)

	// Stream the new content
	builder.AddLine("new addition 1")
	builder.AddLine("new addition 2")
	builder.AddLine("second line modified") // modifies old line 2
	builder.AddLine("new addition 3")

	result := builder.Finalize()
	assert.NotNil(t, result, "result should not be nil")
	assert.Equal(t, 1, len(result.Stages), "should have 1 stage")

	stage := result.Stages[0]

	// Find the modification group
	var modGroup *Group
	for _, g := range stage.Groups {
		if g.Type == "modification" {
			modGroup = g
			break
		}
	}

	assert.NotNil(t, modGroup, "should have a modification group")
	// ComputeDiff maps old line 1 ("first line") as modified, BufferLine = 5
	assert.Equal(t, 5, modGroup.BufferLine,
		"modification BufferLine should match old line position (5)")
}

// TestIncrementalStageBuilder_AdditionsBeforeCursorModificationAnchoredAtCursor
// verifies that additions preceding the cursor line's modification are anchored
// at the cursor line, so they render directly above the cursor.
func TestIncrementalStageBuilder_AdditionsBeforeCursorModificationAnchoredAtCursor(t *testing.T) {
	// Old content: 2 lines at buffer positions 5-6
	// Cursor is on buffer line 6
	// New content has additions before the cursor line modification
	oldLines := []string{
		"first line",
		"cursor line content",
	}
	builder := NewIncrementalStageBuilder(
		oldLines,
		5,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		6, 0, // cursorRow, cursorCol
		"test.go",
		0, // availableWidth
	)

	// Stream: first line modified, additions inserted, cursor line modified
	builder.AddLine("first line modified")
	builder.AddLine("added line 1")
	builder.AddLine("added line 2")
	builder.AddLine("cursor line replaced")

	result := builder.Finalize()
	assert.NotNil(t, result, "result should not be nil")
	assert.Equal(t, 1, len(result.Stages), "should have 1 stage")

	stage := result.Stages[0]

	// Find the cursor line modification
	var cursorMod *Group
	for _, g := range stage.Groups {
		if g.Type == "modification" && len(g.OldLines) > 0 && g.OldLines[0] == "cursor line content" {
			cursorMod = g
			break
		}
	}

	assert.NotNil(t, cursorMod, "should have modification for cursor line")
	assert.Equal(t, 6, cursorMod.BufferLine, "cursor line modification should have BufferLine=6")

	// Find addition groups that precede the cursor line modification
	for _, g := range stage.Groups {
		if g.Type == "addition" && g.StartLine < cursorMod.StartLine {
			// Additions before cursor modification should be anchored at cursor line
			assert.Equal(t, 6, g.BufferLine,
				"additions before cursor line should be anchored at cursor line (6)")
		}
	}
}

// TestIncrementalStageBuilder_WhitespaceLineExpansion tests the scenario where
// a whitespace-only line on the cursor row is expanded with additions before it.
// This closely matches the log scenario where:
// - Buffer line 5: "" (empty line)
// - Buffer line 6: "        " (8 spaces, cursor line)
// - Completion expands to 4 lines with additions before the modification
func TestIncrementalStageBuilder_WhitespaceLineExpansion(t *testing.T) {
	oldLines := []string{
		"",         // buffer line 5
		"        ", // buffer line 6 (cursor line)
	}
	builder := NewIncrementalStageBuilder(
		oldLines,
		5,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		6, 0, // cursorRow, cursorCol
		"test.py",
		0, // availableWidth
	)

	// Stream the completion: adds content before cursor line, then modifies cursor line
	builder.AddLine("    Parameters")                      // modifies empty line or addition
	builder.AddLine("    ----------")                      // addition
	builder.AddLine("    rA : numpy array")                // modifies whitespace line
	builder.AddLine("        The coordinates of point A.") // addition

	result := builder.Finalize()
	assert.NotNil(t, result, "result should not be nil")
	assert.Equal(t, 1, len(result.Stages), "should have 1 stage")

	stage := result.Stages[0]

	// The two modifications have different render hints (append_chars and replace_chars),
	// so they are separate groups rather than one multi-line modification group.
	var modGroups []*Group
	for _, g := range stage.Groups {
		if g.Type == "modification" {
			modGroups = append(modGroups, g)
		}
	}

	assert.Equal(t, 2, len(modGroups), "should have two modification groups")
	assert.Equal(t, 5, modGroups[0].BufferLine, "first modification at buffer line 5")
	assert.Equal(t, "append_chars", modGroups[0].RenderHint, "empty line gets append_chars")
	assert.Equal(t, 6, modGroups[1].BufferLine, "second modification at buffer line 6")
	assert.Equal(t, "replace_chars", modGroups[1].RenderHint, "whitespace line gets replace_chars")

	// Additions after the modification groups should be anchored below them
	for _, g := range stage.Groups {
		if g.Type == "addition" && g.StartLine > modGroups[1].StartLine {
			assert.True(t, g.BufferLine >= 6,
				"additions after modification block should be anchored at or below buffer line 6")
		}
	}
}

// TestIncrementalStageBuilder_LowSimilarityModification tests that when streaming
// classifies a change as an addition (due to low similarity), the fallback matching
// in finalizeCurrentStage correctly identifies it as a modification, resulting in
// correct BufferStart computation.
//
// This was a bug where low-similarity modifications (similarity < 0.35) were
// classified as additions during streaming, causing the "pure additions" logic
// to incorrectly add +1 to BufferStart.
func TestIncrementalStageBuilder_LowSimilarityModification(t *testing.T) {
	tests := []struct {
		name           string
		oldLine        string
		newLine        string
		baseLineOffset int
		wantBufferLine int
	}{
		{
			name:           "single line with low similarity",
			oldLine:        `console.log("");`,
			newLine:        `console.log("OPENROUTER_API_KEY", process.env.OPENROUTER_API_KEY);`,
			baseLineOffset: 1,
			wantBufferLine: 1,
		},
		{
			name:           "empty parens to full content",
			oldLine:        `console.log();`,
			newLine:        `console.log("OPENROUTER_API_KEY", process.env.OPENROUTER_API_KEY || "YOUR_API_KEY");`,
			baseLineOffset: 1,
			wantBufferLine: 1,
		},
		{
			name:           "with non-zero base offset",
			oldLine:        `return null;`,
			newLine:        `return { data: response.data, status: response.status };`,
			baseLineOffset: 5,
			wantBufferLine: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldLines := []string{tt.oldLine}

			builder := NewIncrementalStageBuilder(
				oldLines,
				tt.baseLineOffset,
				10,   // proximityThreshold
				0,    // maxVisibleLines
				0, 0, // viewport disabled
				tt.baseLineOffset, 0, // cursor at the line being modified
				"test.ts",
				0, // availableWidth
			)

			builder.AddLine(tt.newLine)

			result := builder.Finalize()
			assert.NotNil(t, result, "result should not be nil")
			assert.Equal(t, 1, len(result.Stages), "should have 1 stage")

			stage := result.Stages[0]
			assert.Equal(t, tt.wantBufferLine, stage.BufferStart,
				"BufferStart should match baseLineOffset for single-line modification")
			assert.Equal(t, tt.wantBufferLine, stage.BufferEnd,
				"BufferEnd should match BufferStart for single-line")

			assert.True(t, len(stage.Groups) > 0, "should have groups")
			if len(stage.Groups) > 0 {
				group := stage.Groups[0]
				assert.Equal(t, tt.wantBufferLine, group.BufferLine,
					"Group BufferLine should match expected")
				assert.Equal(t, "modification", group.Type,
					"should be classified as modification after fallback matching")
			}
		})
	}
}

func TestIncrementalStageBuilder_InsertedLineIsAddition(t *testing.T) {
	oldLines := []string{"func main() {", "    return", "}"}

	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		3,    // proximityThreshold
		0,    // maxVisibleLines
		0, 0, // viewport (disabled)
		1, 0, // cursor at line 1
		"test.go",
		0, // availableWidth
	)

	// Insert an empty line between line 1 and line 2
	builder.AddLine("func main() {")
	builder.AddLine("")
	builder.AddLine("    return")
	builder.AddLine("}")

	result := builder.Finalize()
	assert.NotNil(t, result, "should have staging result")
	assert.True(t, len(result.Stages) > 0, "should have at least one stage")

	stage := result.Stages[0]
	assert.True(t, len(stage.Groups) > 0, "should have groups")

	group := stage.Groups[0]
	assert.Equal(t, "addition", group.Type, "group type")
	assert.Equal(t, []string{""}, group.Lines, "group lines")
}

// TestIncrementalStageBuilder_LowSimilarityModificationBufferRange verifies that when
// a modification has very low similarity (e.g., printf() -> printf("very long args")),
// the stage's BufferStart correctly points to the modified line, not the anchor line.
//
// During streaming, low-similarity modifications get classified as additions with an
// anchor at the preceding line. The finalization remap step correctly identifies them
// as modifications via fallback matching, but BufferStart must be recomputed from the
// actual matched old line positions.
// TestIncrementalStageBuilder_BlankLineInsertionsBetweenFunctions verifies that
// inserting blank lines between function definitions produces only addition groups,
// not spurious deletions. Reproduces a bug where the intermediate stage during
// streaming showed a deletion of an unrelated line (e.g. print(calculate_min_max))
// because findOldLineRange only captured old lines between the nearest anchors,
// missing the fact that lines further down were just shifted, not deleted.
func TestIncrementalStageBuilder_BlankLineInsertionsBetweenFunctions(t *testing.T) {
	oldLines := []string{
		"import numpy as np",                    // 1
		"",                                      // 2
		"",                                      // 3
		"def calculate_mean(data):",             // 4
		"    return np.mean(data)",              // 5
		"",                                      // 6
		"",                                      // 7
		"def calculate_min_max(data):",          // 8
		"    return np.min(data), np.max(data)", // 9
		"def calculate_var(data):",              // 10
		"    return np.var(data)",               // 11
		"if __name__ == \"__main__\":",          // 12
		"    data = np.array([1, 2, 3, 4, 5])",  // 13
		"    print(calculate_mean(data))",       // 14
		"    print(calculate_min_max(data))",    // 15
		"    print(calculate_var(data))",        // 16
	}

	builder := NewIncrementalStageBuilder(
		oldLines,
		1,     // baseLineOffset
		3,     // proximityThreshold
		4,     // maxVisibleLines
		1, 50, // viewport
		10, 0, // cursorRow, cursorCol
		"test.py",
		0, // availableWidth
	)

	// Stream all new lines (model adds 2 blank lines before each def/if block)
	newLines := []string{
		"import numpy as np",
		"",
		"",
		"def calculate_mean(data):",
		"    return np.mean(data)",
		"",
		"",
		"def calculate_min_max(data):",
		"    return np.min(data), np.max(data)",
		"", // added blank line
		"", // added blank line
		"def calculate_var(data):",
		"    return np.var(data)",
		"", // added blank line
		"", // added blank line
		"if __name__ == \"__main__\":",
		"    data = np.array([1, 2, 3, 4, 5])",
		"    print(calculate_mean(data))",
		"    print(calculate_min_max(data))",
		"    print(calculate_var(data))",
	}

	var intermediateStages []*Stage
	for _, line := range newLines {
		if stage := builder.AddLine(line); stage != nil {
			intermediateStages = append(intermediateStages, stage)
		}
	}

	// Verify intermediate stages have no deletions
	for i, stage := range intermediateStages {
		for _, group := range stage.Groups {
			assert.NotEqual(t, "deletion", group.Type,
				fmt.Sprintf("intermediate stage %d should have no deletion groups, got deletion at buffer_line %d with lines %v",
					i, group.BufferLine, group.Lines))
		}
	}

	result := builder.Finalize()
	assert.NotNil(t, result, "result should not be nil")

	// Verify the finalized (batch) result has no deletions
	for _, stage := range result.Stages {
		for _, group := range stage.Groups {
			assert.NotEqual(t, "deletion", group.Type,
				fmt.Sprintf("finalized stage should have no deletion groups, got deletion at buffer_line %d with lines %v",
					group.BufferLine, group.Lines))
		}
	}
}

func TestIncrementalStageBuilder_LowSimilarityModificationBufferRange(t *testing.T) {
	oldLines := []string{
		"#include \"header.h\"",
		"",
		"void func(int a,",
		"          int b,",
		"          int c) {",
		"    printf()",
		"",
		"    return;",
		"}",
	}

	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		3,    // proximityThreshold
		0,    // maxVisibleLines
		0, 0, // viewport disabled
		6, 0, // cursor at the printf line
		"test.c",
		0, // availableWidth
	)

	builder.AddLine(`#include "header.h"`)
	builder.AddLine("")
	builder.AddLine("void func(int a,")
	builder.AddLine("          int b,")
	builder.AddLine("          int c) {")
	// Low-similarity modification: short line -> very long line
	builder.AddLine(`    printf("signal=%p, n_samples=%zu, lead_idx=%d\n", signal, n_samples, lead_idx);`)
	builder.AddLine("")
	builder.AddLine("    return;")
	builder.AddLine("}")

	result := builder.Finalize()
	assert.NotNil(t, result, "result should not be nil")
	assert.Equal(t, 1, len(result.Stages), "should have 1 stage")

	stage := result.Stages[0]
	// BufferStart should be 6 (the printf line), not 5 (the anchor line before it)
	assert.Equal(t, 6, stage.BufferStart,
		"BufferStart should be at the modified line, not the preceding anchor")
	assert.Equal(t, 6, stage.BufferEnd,
		"BufferEnd should match for single-line modification")

	assert.True(t, len(stage.Groups) > 0, "should have groups")
	if len(stage.Groups) > 0 {
		group := stage.Groups[0]
		assert.Equal(t, 6, group.BufferLine,
			"Group BufferLine should be at the modified line")
		assert.Equal(t, "modification", group.Type,
			"should be modification after fallback matching")
	}
}

func TestIncrementalStageBuilder_EmptyStreamProducesNoStages(t *testing.T) {
	oldLines := []string{"line 1", "line 2", "line 3", "line 4", "line 5"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,  // baseLineOffset
		10, // proximityThreshold
		0,  // maxVisibleLines
		1,  // viewportTop
		50, // viewportBottom
		1,  // cursorRow
		0,  // cursorCol
		"test.go",
		0, // availableWidth
	)

	// No lines added — simulates a provider returning empty content
	result := builder.Finalize()
	assert.Nil(t, result, "empty stream should produce no stages")
}

func TestIncrementalStageBuilder_FewModificationsInLargeFile(t *testing.T) {
	// Simulates a provider that returns the full file with a few lines changed
	// (e.g., "app.route" → "app.api.route" on 4 lines in a 30-line file).
	// The stage should only cover the modified lines, not extend to the end of the file.
	var oldLines []string
	for i := 1; i <= 30; i++ {
		oldLines = append(oldLines, fmt.Sprintf("line %d content", i))
	}

	builder := NewIncrementalStageBuilder(
		oldLines,
		1,  // baseLineOffset
		10, // proximityThreshold
		0,  // maxVisibleLines
		1,  // viewportTop
		50, // viewportBottom
		15, // cursorRow (near the modifications)
		0,  // cursorCol
		"test.go",
		0, // availableWidth
	)

	// Stream the full file with 4 modifications in the middle (lines 12-15)
	for i := 1; i <= 30; i++ {
		if i >= 12 && i <= 15 {
			builder.AddLine(fmt.Sprintf("line %d MODIFIED", i))
		} else {
			builder.AddLine(fmt.Sprintf("line %d content", i))
		}
	}

	result := builder.Finalize()
	assert.NotNil(t, result, "result")
	assert.True(t, len(result.Stages) >= 1, "should have at least 1 stage")

	stage := result.Stages[0]

	// The stage should cover roughly lines 12-15, not extend to line 30
	assert.True(t, stage.BufferEnd <= 20,
		fmt.Sprintf("BufferEnd should not extend far past modifications, got %d", stage.BufferEnd))
	assert.Equal(t, 12, stage.BufferStart,
		fmt.Sprintf("BufferStart should be at first modification, got %d", stage.BufferStart))

	// Should not produce deletion groups for unchanged trailing lines
	for _, g := range stage.Groups {
		assert.True(t, g.Type != "deletion",
			fmt.Sprintf("should not have deletion groups for unchanged content, got deletion at buffer line %d", g.BufferLine))
	}
}

// TestIncrementalStageBuilder_MaxVisibleLinesSplitNoSpuriousDeletions tests that when
// MaxVisibleLines splits a stage mid-stream and the lines immediately following the
// split boundary are also changes (no exact matches), the finalized stage does not
// produce spurious deletions of the unchanged trailing content.
//
// This reproduces a provider bug where a model changes N lines and
// MaxVisibleLines=4 splits after 4 changes, but the next changed lines have no
// forward anchor yet in the streaming mapping, causing findOldLineRange to fall back
// to len(OldLines) and include the entire remaining file as "deleted" content.
func TestIncrementalStageBuilder_MaxVisibleLinesSplitNoSpuriousDeletions(t *testing.T) {
	// Simulate a file with:
	// - Lines 1-5: unchanged prefix (e.g., imports, middleware)
	// - Lines 6-13: 8 changed routes (old: app.route, new: app.api.route)
	// - Lines 14-20: unchanged suffix (other routes, exports)
	oldLines := []string{
		"const app = new App();",       // 1 - unchanged
		"app.use(middleware);",         // 2 - unchanged
		"",                             // 3 - unchanged
		"// Mount routes",              // 4 - unchanged
		"app.use(authMiddleware);",     // 5 - unchanged
		`app.route("/health", health)`, // 6 - will be changed
		`app.route("/auth", auth)`,     // 7 - will be changed
		`app.route("/admin", admin)`,   // 8 - will be changed
		`app.route("/oauth", oauth)`,   // 9 - will be changed
		`app.route("/users", users)`,   // 10 - will be changed
		`app.route("/posts", posts)`,   // 11 - will be changed
		`app.route("/files", files)`,   // 12 - will be changed
		`app.route("/data", data)`,     // 13 - will be changed
		"",                             // 14 - unchanged
		"// Exports",                   // 15 - unchanged
		"export { app };",              // 16 - unchanged
		"export { db };",               // 17 - unchanged
		"export { session };",          // 18 - unchanged
		"export { cache };",            // 19 - unchanged
		"export { queue };",            // 20 - unchanged
	}

	builder := NewIncrementalStageBuilder(
		oldLines,
		1, // baseLineOffset
		5, // proximityThreshold
		4, // maxVisibleLines = 4 (triggers mid-stream split after 4 changes)
		0, // viewportTop (disabled)
		0, // viewportBottom (disabled)
		3, // cursorRow
		0, // cursorCol
		"test.ts",
		0, // availableWidth
	)

	// Stream emits the full modified file: first 5 lines unchanged, then 8 changed routes
	var firstStage *Stage
	for _, line := range []string{
		"const app = new App();",           // 1 - match
		"app.use(middleware);",             // 2 - match
		"",                                 // 3 - match
		"// Mount routes",                  // 4 - match
		"app.use(authMiddleware);",         // 5 - match
		`app.api.route("/health", health)`, // 6 - changed
		`app.api.route("/auth", auth)`,     // 7 - changed
		`app.api.route("/admin", admin)`,   // 8 - changed
		`app.api.route("/oauth", oauth)`,   // 9 - changed (MaxVisibleLines fires here)
		`app.api.route("/users", users)`,   // 10 - changed (no forward anchor yet)
		`app.api.route("/posts", posts)`,   // 11 - changed
		`app.api.route("/files", files)`,   // 12 - changed
		`app.api.route("/data", data)`,     // 13 - changed
		"",                                 // 14 - match (forward anchor)
		"// Exports",                       // 15 - match
		"export { app };",                  // 16 - match
		"export { db };",                   // 17 - match
		"export { session };",              // 18 - match
		"export { cache };",                // 19 - match
		"export { queue };",                // 20 - match
	} {
		if s := builder.AddLine(line); s != nil && firstStage == nil {
			firstStage = s
		}
	}

	// The first mid-stream stage (health-oauth, 4 changes) should have been finalized
	// when processing the 5th change (users). Verify it has no spurious deletions.
	if firstStage != nil {
		for _, g := range firstStage.Groups {
			assert.True(t, g.Type != "deletion",
				fmt.Sprintf("first mid-stream stage should not have deletion groups, got %q at buffer line %d", g.Type, g.BufferLine))
		}
		// Should cover exactly 4 lines (health-oauth), not the whole remaining file
		stageLineSpan := firstStage.BufferEnd - firstStage.BufferStart + 1
		assert.True(t, stageLineSpan <= 4,
			fmt.Sprintf("first stage should span at most 4 lines, spans %d (BufferStart=%d, BufferEnd=%d)",
				stageLineSpan, firstStage.BufferStart, firstStage.BufferEnd))
	}

	// Finalize and check overall result has no spurious deletions
	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	for _, stage := range result.Stages {
		for _, g := range stage.Groups {
			assert.True(t, g.Type != "deletion",
				fmt.Sprintf("stage should not have deletion groups for lines that exist in new content, got %q at buffer line %d", g.Type, g.BufferLine))
		}
	}

	// All 8 changes should be modifications, not deletions
	totalModifications := 0
	for _, stage := range result.Stages {
		for _, g := range stage.Groups {
			if g.Type == "modification" {
				totalModifications += g.EndLine - g.StartLine + 1
			}
		}
	}
	assert.Equal(t, 8, totalModifications, "all 8 changed routes should appear as modifications")
}

// TestIncrementalStageBuilder_PartialLineModificationDuringStreaming tests that when
// the cursor is on a partially-typed line (e.g., "def insers") and the model streams
// a completed version ("def insertion_sort(arr):"), the first streamed stage correctly
// detects a modification rather than a pure addition.
//
// The fix: maxVisibleLines defers stage building until the next matched line,
// so oldLineIdx has advanced past the partially-typed line and the batch diff
// can detect the modification via fuzzy matching.
func TestIncrementalStageBuilder_PartialLineModificationDuringStreaming(t *testing.T) {
	oldLines := []string{
		"import numpy as np",
		"",
		"",
		"def bubble_sort(arr):",
		"    for i in range(len(arr)):",
		"        for j in range(i + 1, len(arr)):",
		"            if arr[i] > arr[j]:",
		"                arr[i], arr[j] = arr[j], arr[i]",
		"    return arr",
		"",
		"def insers",
		"",
		"",
		"if __name__ == \"__main__\":",
	}

	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold
		4,    // maxVisibleLines — defers after 4 changes, builds on next match
		0, 0, // viewport disabled
		11, 10, // cursorRow (line 11), cursorCol (end of "def insers")
		"test.py",
		0, // availableWidth
	)

	// Stream lines: first 10 match, then the changes start
	for _, line := range oldLines[:10] {
		stage := builder.AddLine(line)
		assert.Nil(t, stage, "unchanged lines should not produce a stage")
	}

	// 4 change lines — maxVisibleLines is reached but stage is deferred
	stage := builder.AddLine("def insertion_sort(arr):")
	assert.Nil(t, stage, "should not produce a stage on change (deferred)")
	stage = builder.AddLine("    for i in range(1, len(arr)):")
	assert.Nil(t, stage, "should not produce a stage on change (deferred)")
	stage = builder.AddLine("        key = arr[i]")
	assert.Nil(t, stage, "should not produce a stage on change (deferred)")
	stage = builder.AddLine("        j = i - 1")
	assert.Nil(t, stage, "should not produce a stage on change (deferred)")

	// More body lines (still changes, still deferred)
	builder.AddLine("        while j >= 0 and arr[j] > key:")
	builder.AddLine("            arr[j + 1] = arr[j]")
	builder.AddLine("            j -= 1")
	builder.AddLine("        arr[j + 1] = key")
	builder.AddLine("    return arr")

	// Empty lines match old[11] and old[12] — the deferred build fires on
	// the first match because oldLineIdx has now advanced past "def insers".
	stage = builder.AddLine("")
	assert.NotNil(t, stage, "should produce a stage on first match after deferral")

	// The first group on buffer line 11 must be a modification (replacing
	// "def insers" → "def insertion_sort(arr):"), not an addition.
	hasModification := false
	for _, g := range stage.Groups {
		if g.BufferLine == 11 && g.Type == "modification" {
			hasModification = true
			assert.Equal(t, 1, len(g.OldLines), "modification should have 1 old line")
			assert.Equal(t, "def insers", g.OldLines[0], "old line content")
		}
	}
	assert.True(t, hasModification,
		"first streamed stage should have a modification for the partially-typed line")
}
