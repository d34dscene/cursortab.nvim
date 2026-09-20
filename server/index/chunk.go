package index

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"cursortab/utils"
)

// Bounds that keep first-time indexing cheap on large workspaces.
const (
	maxFiles      = 4000
	maxFileBytes  = 256 * 1024
	maxChunkLines = 120
	maxChunkBytes = 8 * 1024
)

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, ".next": true, "target": true, ".venv": true,
	"__pycache__": true, ".cache": true, "coverage": true, ".idea": true,
	".vscode": true, "out": true, "bin": true, "obj": true, "tmp": true,
}

var sourceExts = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".svelte": true, ".py": true, ".rs": true, ".java": true, ".rb": true,
	".c": true, ".h": true, ".cc": true, ".cpp": true, ".hpp": true,
}

// Build walks the workspace and indexes its declarations. It is safe to call
// from a background goroutine; the result is immutable once returned.
func Build(root string) (*index, error) {
	paths, err := discover(root)
	if err != nil {
		return nil, err
	}

	ix := &index{root: root, df: make(map[string]int)}
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil || len(src) == 0 {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		for _, c := range chunkFile(rel, src) {
			if c.Content == "" {
				continue
			}
			c.Tokens = utils.TokenizeCode(c.Content + " " + c.Name)
			if len(c.Tokens) == 0 {
				continue
			}
			for _, token := range c.Tokens {
				ix.df[token]++
			}
			ix.chunks = append(ix.chunks, c)
		}
	}
	return ix, nil
}

// discover returns indexable source files under root, newest-first is not
// needed: the index is static until the next rebuild. Results are sorted for
// deterministic indexes across runs.
func discover(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		name := d.Name()
		if d.IsDir() {
			if path == root {
				return nil
			}
			if skipDirs[name] || strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		if len(paths) >= maxFiles {
			return fs.SkipAll
		}
		if !sourceExts[strings.ToLower(filepath.Ext(name))] {
			return nil
		}
		if info, err := d.Info(); err == nil && info.Size() > maxFileBytes {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func newChunk(path, kind, name, content string) chunk {
	return chunk{Path: path, Kind: kind, Name: name, Content: content, Signature: headerOf(content)}
}

func chunkFile(path string, src []byte) []chunk {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return chunkGo(path, src)
	case ".svelte":
		return chunkSvelte(path, src)
	default:
		return chunkGeneric(path, src)
	}
}

// chunkGo extracts top-level declarations with the real Go parser so chunk
// boundaries are exact.
func chunkGo(path string, src []byte) []chunk {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil || file == nil {
		return chunkGeneric(path, src)
	}

	slice := func(n ast.Node) string {
		start := fset.Position(n.Pos()).Offset
		end := fset.Position(n.End()).Offset
		if start < 0 || end > len(src) || start >= end {
			return ""
		}
		return capChunk(string(src[start:end]))
	}

	var out []chunk
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			kind := "func"
			if d.Recv != nil {
				kind = "method"
			}
			if content := slice(d); content != "" {
				c := newChunk(path, kind, d.Name.Name, content)
				// Exact signature: source from the func keyword through the body's
				// opening brace. A multi-line parameter list is kept intact.
				if d.Body != nil {
					start := fset.Position(d.Pos()).Offset
					end := fset.Position(d.Body.Lbrace).Offset + 1
					if start >= 0 && end > start && end <= len(src) {
						c.Signature = string(src[start:end])
					}
				}
				out = append(out, c)
			}
		case *ast.GenDecl:
			switch d.Tok {
			case token.IMPORT:
				continue
			case token.TYPE:
				// One chunk per declaration, sliced from the GenDecl so the
				// `type` keyword and any grouping parens are included. Slicing
				// the TypeSpec alone would drop the keyword and produce a
				// fragment that is not valid Go.
				var names []string
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						names = append(names, ts.Name.Name)
					}
				}
				if content := slice(d); content != "" {
					out = append(out, newChunk(path, "type", strings.Join(names, ","), content))
				}
			default: // CONST, VAR
				var names []string
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, n := range vs.Names {
							names = append(names, n.Name)
						}
					}
				}
				if content := slice(d); content != "" {
					out = append(out, newChunk(path, strings.ToLower(d.Tok.String()), strings.Join(names, ","), content))
				}
			}
		}
	}
	return out
}

// declRe matches a top-level declaration line for brace and indent based
// chunkers: optional export/default/async, a declaration keyword, then a name.
var declRe = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?(function|class|interface|type|enum|struct|const|let|var|def|fn)\s+([A-Za-z_$][\w$]*)`)

// chunkGeneric groups lines into declarations by brace depth. It ignores
// strings and comments, which is acceptable for retrieval: a slightly wrong
// boundary still returns representative code for the file.
func chunkGeneric(path string, src []byte) []chunk {
	lines := strings.Split(string(src), "\n")
	var out []chunk

	for i := 0; i < len(lines); {
		m := declRe.FindStringSubmatch(lines[i])
		if m == nil {
			i++
			continue
		}
		start := i
		depth := 0
		sawBrace := false
		for i < len(lines) {
			depth += strings.Count(lines[i], "{") - strings.Count(lines[i], "}")
			if strings.Contains(lines[i], "{") {
				sawBrace = true
			}
			i++
			if sawBrace && depth <= 0 {
				break
			}
			if !sawBrace && strings.TrimSpace(lines[i-1]) == "" {
				break
			}
			if i-start >= maxChunkLines {
				break
			}
		}
		content := capChunk(strings.TrimRight(strings.Join(lines[start:i], "\n"), " \t\n"))
		if content != "" {
			out = append(out, newChunk(path, m[1], m[2], content))
		}
	}
	return out
}

// scriptBodyRe captures the body of a Svelte <script> block.
var scriptBodyRe = regexp.MustCompile(`(?s)<script[^>]*>(.*?)</script>`)

// chunkSvelte indexes the script body of a component plus its top-level
// markup, since both carry names a completion may need.
func chunkSvelte(path string, src []byte) []chunk {
	var out []chunk
	for _, m := range scriptBodyRe.FindAllSubmatch(src, -1) {
		out = append(out, chunkGeneric(path, m[1])...)
	}
	if len(out) == 0 {
		return chunkGeneric(path, src)
	}
	return out
}

func capChunk(s string) string {
	if len(s) <= maxChunkBytes {
		return s
	}
	return s[:maxChunkBytes]
}
