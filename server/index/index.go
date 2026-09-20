// Package index builds a lightweight lexical index of a workspace and answers
// cursor-derived lookups. The point is relevance: a prompt should carry the
// declarations that share names with the code being written, not just the
// files the user happened to touch most recently.
package index

import (
	"math"
	"sort"
	"strings"

	"cursortab/types"
	"cursortab/utils"
)

const (
	defaultLimit = 8

	// maxChunksPerFile keeps one file from crowding out the rest of the
	// workspace. Two declarations from the same file is usually enough.
	maxChunksPerFile = 2

	// Scoring weights. Identifier overlap dominates because code reuse is
	// almost always name reuse.
	identWeight = 1.0
	tokenWeight = 0.35
	nameBoost   = 2.0
	dirBoost    = 0.5
)

type chunk struct {
	Path      string
	Kind      string
	Name      string
	Content   string
	Signature string
	Tokens    []string
}

type index struct {
	root   string
	chunks []chunk
	df     map[string]int
}

// query ranks indexed chunks against the cursor context and returns the best
// matches. Chunks with no signal at all are dropped.
func (ix *index) query(q types.RetrievalQuery) []types.RetrievalChunk {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	idents := make(map[string]bool, len(q.Identifiers))
	for _, id := range q.Identifiers {
		idents[id] = true
	}
	tokens := make(map[string]bool, len(q.Tokens))
	for _, t := range q.Tokens {
		tokens[t] = true
	}

	type scored struct {
		chunk *chunk
		score float64
	}
	results := make([]scored, 0, len(ix.chunks))
	for i := range ix.chunks {
		c := &ix.chunks[i]
		// The current file is already in the provider's window. Re-sending its
		// declarations as retrieved context measurably degrades completions,
		// because the model reads its own enclosing declaration as a
		// reference and copies from it.
		if c.Path == q.CurrentPath {
			continue
		}
		score := ix.score(c, idents, tokens, q.CurrentPath)
		if score <= 0 {
			continue
		}
		results = append(results, scored{c, score})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return results[i].chunk.Path < results[j].chunk.Path
	})

	out := make([]types.RetrievalChunk, 0, limit)
	perFile := make(map[string]int)
	seenContent := make(map[string]bool)
	for _, r := range results {
		if perFile[r.chunk.Path] >= maxChunksPerFile {
			continue
		}
		// Duplicated declarations (copy-paste, generated files) add prompt
		// cost without new information.
		key := contentKey(r.chunk)
		if seenContent[key] {
			continue
		}
		seenContent[key] = true
		perFile[r.chunk.Path]++

		out = append(out, types.RetrievalChunk{
			Path:      r.chunk.Path,
			Kind:      r.chunk.Kind,
			Name:      r.chunk.Name,
			Signature: r.chunk.Signature,
			Content:   r.chunk.Content,
			Score:     r.score,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func contentKey(c *chunk) string {
	return c.Name + "\x00" + c.Content
}

func (ix *index) score(c *chunk, idents, tokens map[string]bool, currentPath string) float64 {
	var score float64
	for _, token := range c.Tokens {
		switch {
		case idents[token]:
			score += identWeight * ix.idf(token)
		case tokens[token]:
			score += tokenWeight * ix.idf(token)
		}
	}
	if score == 0 {
		return 0
	}
	for _, part := range utils.TokenizeCode(c.Name) {
		if idents[part] {
			score += nameBoost
		}
	}
	if sameDir(c.Path, currentPath) {
		score += dirBoost
	}
	return score
}

// idf weights rare names above ubiquitous ones, so a project-specific helper
// outranks a generic word shared by every file.
func (ix *index) idf(token string) float64 {
	n := float64(len(ix.chunks))
	df := float64(ix.df[token])
	return math.Log(1 + n/(1+df))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// headerOf returns the declaration header: everything up to and including the
// first opening brace, or the first line when there is none. For a function
// this is the signature, which is what a prompt should carry.
func headerOf(s string) string {
	if i := strings.IndexByte(s, '{'); i >= 0 {
		return strings.TrimRight(s[:i+1], " \t")
	}
	return firstLine(s)
}

func sameDir(a, b string) bool {
	return dirOf(a) == dirOf(b)
}

func dirOf(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return ""
}
