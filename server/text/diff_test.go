package text

import (
	"cmp"
	"cursortab/assert"
	"fmt"
	"slices"
	"testing"
)

// changeWant is the Lua-contract slice of a LineChange: type, contents and
// character span flow into groups, lines, old_lines and render hints.
type changeWant struct {
	Type     ChangeType
	Content  string
	Old      string
	ColStart int
	ColEnd   int
}

func changeWants(changes []LineChange) []changeWant {
	if len(changes) == 0 {
		return nil
	}
	wants := make([]changeWant, len(changes))
	for i, c := range changes {
		wants[i] = changeWant{Type: c.Type, Content: c.Content, Old: c.OldContent, ColStart: c.ColStart, ColEnd: c.ColEnd}
	}
	return wants
}

func sortWants(wants []changeWant) {
	slices.SortFunc(wants, func(a, b changeWant) int {
		if c := cmp.Compare(a.Type, b.Type); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Content, b.Content); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Old, b.Old); c != 0 {
			return c
		}
		if c := cmp.Compare(a.ColStart, b.ColStart); c != 0 {
			return c
		}
		return cmp.Compare(a.ColEnd, b.ColEnd)
	})
}

// assertCategorization compares the full change set without caring which line
// each change was keyed to (line placement is asserted by the txtar goldens).
func assertCategorization(t *testing.T, got []LineChange, want []changeWant) {
	t.Helper()
	gotWants := changeWants(got)
	sortWants(gotWants)
	sortWants(want)
	assert.Equal(t, want, gotWants, "categorization")
}

func TestComputeDiffCategorization(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
		want []changeWant
	}{
		{
			name: "single deletion",
			old:  "line 1\nline 2\nline 3\nline 4",
			new:  "line 1\nline 3\nline 4",
			want: []changeWant{{Type: ChangeDeletion, Content: "line 2"}},
		},
		{
			name: "single addition",
			old:  "line 1\nline 3\nline 4",
			new:  "line 1\nline 2\nline 3\nline 4",
			want: []changeWant{{Type: ChangeAddition, Content: "line 2"}},
		},
		{
			name: "append chars at end",
			old:  "Hello world",
			new:  "Hello world!",
			want: []changeWant{{Type: ChangeAppendChars, Content: "Hello world!", Old: "Hello world", ColStart: 11, ColEnd: 12}},
		},
		{
			name: "delete chars at end",
			old:  "Hello world!",
			new:  "Hello world",
			want: []changeWant{{Type: ChangeDeleteChars, Content: "Hello world", Old: "Hello world!", ColStart: 11, ColEnd: 12}},
		},
		{
			name: "delete chars in middle",
			old:  "Hello world John",
			new:  "Hello John",
			want: []changeWant{{Type: ChangeDeleteChars, Content: "Hello John", Old: "Hello world John", ColStart: 6, ColEnd: 12}},
		},
		{
			name: "replace chars",
			old:  "Hello world",
			new:  "Hello there",
			want: []changeWant{{Type: ChangeReplaceChars, Content: "Hello there", Old: "Hello world", ColStart: 6, ColEnd: 11}},
		},
		{
			name: "replace chars in middle",
			old:  "Hello world John",
			new:  "Hello there John",
			want: []changeWant{{Type: ChangeReplaceChars, Content: "Hello there John", Old: "Hello world John", ColStart: 6, ColEnd: 11}},
		},
		{
			name: "modification plus addition",
			old:  "function hello() {\n    console.log(\"old message\");\n    return true;\n}",
			new:  "function hello() {\n    console.log(\"new message\");\n    return true;\n    console.log(\"added line\");\n}",
			want: []changeWant{
				{Type: ChangeReplaceChars, Content: "    console.log(\"new message\");", Old: "    console.log(\"old message\");", ColStart: 17, ColEnd: 20},
				{Type: ChangeAddition, Content: "    console.log(\"added line\");"},
			},
		},
		{
			name: "multiple deletions",
			old:  "line 1\nline 2\nline 3\nline 4\nline 5",
			new:  "line 1\nline 3\nline 5",
			want: []changeWant{
				{Type: ChangeDeletion, Content: "line 2"},
				{Type: ChangeDeletion, Content: "line 4"},
			},
		},
		{
			name: "multiple additions",
			old:  "line 1\nline 3\nline 5",
			new:  "line 1\nline 2\nline 3\nline 4\nline 5",
			want: []changeWant{
				{Type: ChangeAddition, Content: "line 2"},
				{Type: ChangeAddition, Content: "line 4"},
			},
		},
		{
			name: "character change on every line",
			old:  "Hello world\nGoodbye world\nWelcome world",
			new:  "Hello there\nGoodbye there\nWelcome there",
			want: []changeWant{
				{Type: ChangeReplaceChars, Content: "Hello there", Old: "Hello world", ColStart: 6, ColEnd: 11},
				{Type: ChangeReplaceChars, Content: "Goodbye there", Old: "Goodbye world", ColStart: 8, ColEnd: 13},
				{Type: ChangeReplaceChars, Content: "Welcome there", Old: "Welcome world", ColStart: 8, ColEnd: 13},
			},
		},
		{
			name: "mixed character operations",
			old:  "Hello world\nGoodbye world!\nWelcome world",
			new:  "Hello there\nGoodbye world\nWelcome there!",
			want: []changeWant{
				{Type: ChangeReplaceChars, Content: "Hello there", Old: "Hello world", ColStart: 6, ColEnd: 11},
				{Type: ChangeDeleteChars, Content: "Goodbye world", Old: "Goodbye world!", ColStart: 13, ColEnd: 14},
				{Type: ChangeReplaceChars, Content: "Welcome there!", Old: "Welcome world", ColStart: 8, ColEnd: 14},
			},
		},
		{
			name: "full line modification",
			old:  "start middle end",
			new:  "beginning middle finish extra",
			want: []changeWant{{Type: ChangeModification, Content: "beginning middle finish extra", Old: "start middle end"}},
		},
		{
			name: "identical texts have no changes",
			old:  "line 1\nline 2\nline 3",
			new:  "line 1\nline 2\nline 3",
		},
		{
			name: "empty old text",
			old:  "",
			new:  "line 1\nline 2\nline 3",
			want: []changeWant{
				{Type: ChangeAddition, Content: "line 1"},
				{Type: ChangeAddition, Content: "line 2"},
				{Type: ChangeAddition, Content: "line 3"},
			},
		},
		{
			name: "empty new text",
			old:  "line 1\nline 2\nline 3",
			new:  "",
			want: []changeWant{
				{Type: ChangeDeletion, Content: "line 1"},
				{Type: ChangeDeletion, Content: "line 2"},
				{Type: ChangeDeletion, Content: "line 3"},
			},
		},
		{
			name: "single line append",
			old:  "hello",
			new:  "hello world",
			want: []changeWant{{Type: ChangeAppendChars, Content: "hello world", Old: "hello", ColStart: 5, ColEnd: 11}},
		},
		{
			name: "consecutive modifications",
			old:  "function test() {\n    start middle end\n    start middle end\n    start middle end\n}",
			new:  "function test() {\n    beginning middle finish extra\n    beginning middle finish extra\n    beginning middle finish extra\n}",
			want: []changeWant{
				{Type: ChangeModification, Content: "    beginning middle finish extra", Old: "    start middle end"},
				{Type: ChangeModification, Content: "    beginning middle finish extra", Old: "    start middle end"},
				{Type: ChangeModification, Content: "    beginning middle finish extra", Old: "    start middle end"},
			},
		},
		{
			name: "consecutive additions",
			old:  "function test() {\n    return true;\n}",
			new:  "function test() {\n    let x = 1;\n    let y = 2;\n    let z = 3;\n    return true;\n}",
			want: []changeWant{
				{Type: ChangeAddition, Content: "    let x = 1;"},
				{Type: ChangeAddition, Content: "    let y = 2;"},
				{Type: ChangeAddition, Content: "    let z = 3;"},
			},
		},
		{
			name: "trailing empty line preserved",
			old:  "import numpy as np\n\n",
			new:  "import numpy as np\n\ndef test():\n    pass\n",
			want: []changeWant{
				{Type: ChangeAddition, Content: "def test():"},
				{Type: ChangeAddition, Content: "    pass"},
			},
		},
		{
			name: "empty line addition in middle",
			old:  "line 1\nline 2\nline 3",
			new:  "line 1\nline 2\n\nline 3",
			want: []changeWant{{Type: ChangeAddition, Content: ""}},
		},
		{
			name: "multiple empty line additions",
			old:  "start\nend",
			new:  "start\n\n\n\nend",
			want: []changeWant{
				{Type: ChangeAddition, Content: ""},
				{Type: ChangeAddition, Content: ""},
				{Type: ChangeAddition, Content: ""},
			},
		},
		{
			name: "pure additions at end of file",
			old:  "import numpy as np\n\n",
			new:  "import numpy as np\n\ndef f1():\n    pass\n\ndef f2():\n    pass\n",
			want: []changeWant{
				{Type: ChangeAddition, Content: "def f1():"},
				{Type: ChangeAddition, Content: "    pass"},
				{Type: ChangeAddition, Content: ""},
				{Type: ChangeAddition, Content: "def f2():"},
				{Type: ChangeAddition, Content: "    pass"},
			},
		},
		{
			name: "empty line filled with content",
			old:  "\n",
			new:  "def calc_angle(x, y\n",
			want: []changeWant{{Type: ChangeAppendChars, Content: "def calc_angle(x, y", ColStart: 0, ColEnd: 19}},
		},
		{
			name: "append chars after indentation on every filled line",
			old:  "func hello() {\n    \n    \n}",
			new:  "func hello() {\n    fmt.Println(\"hello\")\n    return nil\n}",
			want: []changeWant{
				{Type: ChangeAppendChars, Content: "    fmt.Println(\"hello\")", Old: "    ", ColStart: 4, ColEnd: len("    fmt.Println(\"hello\")")},
				{Type: ChangeAppendChars, Content: "    return nil", Old: "    ", ColStart: 4, ColEnd: len("    return nil")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertCategorization(t, ComputeDiff(tt.old, tt.new).Changes, tt.want)
		})
	}
}

func TestLineChangeClassification(t *testing.T) {
	tests := []struct {
		name     string
		oldLine  string
		newLine  string
		expected ChangeType
	}{
		{
			name:     "Simple word replacement - should be replace_chars",
			oldLine:  "Hello world",
			newLine:  "Hello there",
			expected: ChangeReplaceChars,
		},
		{
			name:     "Multiple changes - should be modification",
			oldLine:  "start middle end",
			newLine:  "beginning middle finish extra",
			expected: ChangeModification,
		},
		{
			name:     "Single word change - should be replace_chars",
			oldLine:  "let x = 1;",
			newLine:  "let x = 10;",
			expected: ChangeReplaceChars,
		},
		{
			name:     "Complex restructuring - should be modification",
			oldLine:  `function hello() { return true; }`,
			newLine:  `async function hello() { const result = await process(); return result; }`,
			expected: ChangeModification,
		},
		{
			name:     "Append at end - should be append_chars",
			oldLine:  "Hello world",
			newLine:  "Hello world!",
			expected: ChangeAppendChars,
		},
		{
			name:     "App to server replacement - should be replace_chars",
			oldLine:  `app.route("/health", health);`,
			newLine:  `server.route("/health", health);`,
			expected: ChangeReplaceChars,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			diffType, _, _ := categorizeLineChangeWithColumns(test.oldLine, test.newLine)
			assert.Equal(t, test.expected, diffType, "change classification")
		})
	}
}

func TestChangedByteSpanUsesUTF8Boundaries(t *testing.T) {
	start, oldEnd, newEnd := ChangedByteSpan("Hello 🎉 world", "Hello 🚀 world")

	assert.Equal(t, len("Hello "), start, "start")
	assert.Equal(t, len("Hello 🎉"), oldEnd, "old end")
	assert.Equal(t, len("Hello 🚀"), newEnd, "new end")
}

func TestChangedByteSpanHandlesInvalidUTF8Bytes(t *testing.T) {
	oldLine := "a\xffb"
	newLine := "a\xfeb"

	start, oldEnd, newEnd := ChangedByteSpan(oldLine, newLine)

	assert.Equal(t, 1, start, "start")
	assert.Equal(t, 2, oldEnd, "old end")
	assert.Equal(t, 2, newEnd, "new end")
}

func TestCategorizeLineChangeUsesUTF8Boundaries(t *testing.T) {
	changeType, colStart, colEnd := categorizeLineChangeWithColumns("Hello 🎉 world", "Hello 🚀 world")

	assert.Equal(t, ChangeReplaceChars, changeType, "change type")
	assert.Equal(t, len("Hello "), colStart, "start col")
	assert.Equal(t, len("Hello 🚀"), colEnd, "end col")
}

func TestJoinLinesSplitLinesRoundTrip(t *testing.T) {
	cases := [][]string{
		{"a"},
		{"a", "b"},
		{"a", ""},      // trailing empty line
		{"a", "", "b"}, // empty line in middle
		{"", "a"},      // empty line at start
		{"a", "b", "c"},
	}

	for _, lines := range cases {
		text := JoinLines(lines)
		result := SplitLines(text)
		assert.Equal(t, len(lines), len(result), "round-trip length")

		for i := range lines {
			assert.Equal(t, lines[i], result[i], fmt.Sprintf("round-trip element mismatch at %d", i))
		}
	}
}

// TestDiffVeryLongFile verifies change detection stays exact on a large file.
func TestDiffVeryLongFile(t *testing.T) {
	var lines1, lines2 []string
	for i := range 500 {
		lines1 = append(lines1, fmt.Sprintf("line %d content here", i+1))
		lines2 = append(lines2, fmt.Sprintf("line %d content here", i+1))
	}
	lines2[100] = "modified line 101"
	lines2[200] = "modified line 201"
	lines2[300] = "modified line 301"

	actual := ComputeDiff(JoinLines(lines1), JoinLines(lines2))

	assert.Equal(t, 3, len(actual.Changes), "should detect 3 changes in large file")
}

// TestDiffSingleCharacterChanges tests detection of single character changes.
func TestDiffSingleCharacterChanges(t *testing.T) {
	tests := []struct {
		name     string
		old      string
		new      string
		expected ChangeType
	}{
		{"add single char at end", "hello", "hello!", ChangeAppendChars},
		{"remove single char at end", "hello!", "hello", ChangeDeleteChars},
		{"replace single char", "hello", "hallo", ChangeReplaceChars},
		{"add single char at start", "ello", "hello", ChangeReplaceChars},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := ComputeDiff(tt.old, tt.new)
			assert.Equal(t, 1, len(actual.Changes), "should have 1 change")
			assert.Equal(t, tt.expected, actual.Changes[0].Type, "change type")
		})
	}
}

// TestDiffLineCount verifies OldLineCount and NewLineCount are accurate.
func TestDiffLineCount(t *testing.T) {
	tests := []struct {
		name     string
		old      string
		new      string
		oldCount int
		newCount int
	}{
		{"single to single", "one", "one", 1, 1},
		{"single to multi", "one", "one\ntwo", 1, 2},
		{"multi to single", "one\ntwo", "combined", 2, 1},
		{"empty to content", "", "content", 0, 1},
		{"content to empty", "content", "", 1, 0},
		{"multi to multi same", "a\nb\nc", "x\ny\nz", 3, 3},
		{"add lines", "a\nb", "a\nb\nc\nd", 2, 4},
		{"remove lines", "a\nb\nc\nd", "a\nd", 4, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := ComputeDiff(tt.old, tt.new)
			assert.Equal(t, tt.oldCount, actual.OldLineCount, "OldLineCount")
			assert.Equal(t, tt.newCount, actual.NewLineCount, "NewLineCount")
		})
	}
}

// TestDiffUnicodeContent tests diff with unicode characters.
func TestDiffUnicodeContent(t *testing.T) {
	text1 := "Hello 世界"
	text2 := "Hello 世界!"

	actual := ComputeDiff(text1, text2)

	assert.Equal(t, 1, len(actual.Changes), "should have 1 change")
	assert.Equal(t, ChangeAppendChars, actual.Changes[0].Type, "should be append_chars")
}

// TestIndentedLineFilledWithContent verifies that adding code after existing
// indentation is categorized as append_chars, not a full modification.
func TestIndentedLineFilledWithContent(t *testing.T) {
	tests := []struct {
		name    string
		oldLine string
		newLine string
	}{
		{"spaces only", "    ", "    return result"},
		{"tabs only", "\t\t", "\t\tresult := compute()"},
		{"partial keyword", "    re", "    return result"},
		{"tabs with partial content", "\t\tlogger.Debug(\"", "\t\tlogger.Debug(\"contextual filter shown\")"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := ComputeDiff(tt.oldLine, tt.newLine)

			assert.Equal(t, 1, len(actual.Changes), "should have 1 change")
			change := actual.Changes[0]
			assert.Equal(t, ChangeAppendChars, change.Type, "should be append_chars")
			assert.Equal(t, len(tt.oldLine), change.ColStart, "ColStart at end of old content")
			assert.Equal(t, len(tt.newLine), change.ColEnd, "ColEnd at end of new content")
		})
	}
}
