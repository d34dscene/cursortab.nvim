package index

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"cursortab/assert"
	"cursortab/types"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

const storeGo = `package store

func LoadUser(id string) (User, error) {
	return User{ID: id}, nil
}

type User struct {
	ID   string
	Name string
}
`

const cacheGo = `package store

func EvictCache(key string) {
	delete(cache, key)
}
`

func TestBuildIndexesGoDeclarations(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "store.go", storeGo)
	writeFile(t, dir, "cache.go", cacheGo)

	ix, err := Build(dir)
	assert.NoError(t, err, "build")
	assert.Equal(t, 3, len(ix.chunks), "one chunk per declaration")

	got := ix.query(types.RetrievalQuery{Root: dir, Identifiers: []string{"user"}, Limit: 5})
	assert.Greater(t, len(got), 0, "user query matches")
	for _, chunk := range got {
		assert.NotEqual(t, "EvictCache", chunk.Name, "unrelated declaration excluded")
	}
}

func TestQueryPrefersIdentifierOverGenericTokens(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "store.go", storeGo)
	writeFile(t, dir, "cache.go", cacheGo)

	ix, err := Build(dir)
	assert.NoError(t, err, "build")

	got := ix.query(types.RetrievalQuery{
		Root:        dir,
		Identifiers: []string{"evict"},
		Tokens:      []string{"user"},
		Limit:       1,
	})
	assert.Len(t, 1, got, "single result")
	assert.Equal(t, "EvictCache", got[0].Name, "identifier match wins over generic token")
}

func TestQueryExcludesCurrentFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package p\n\nfunc SharedThing() {}\n")
	writeFile(t, dir, "b.go", "package p\n\nfunc SharedThing() {}\n")

	ix, err := Build(dir)
	assert.NoError(t, err, "build")

	got := ix.query(types.RetrievalQuery{Root: dir, CurrentPath: "a.go", Identifiers: []string{"shared"}, Limit: 5})
	for _, chunk := range got {
		assert.NotEqual(t, "a.go", chunk.Path, "current file never retrieved")
	}
}

func TestQueryReturnsNothingWithoutSignal(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "store.go", storeGo)

	ix, err := Build(dir)
	assert.NoError(t, err, "build")

	got := ix.query(types.RetrievalQuery{Root: dir, Identifiers: []string{"nonexistent"}})
	assert.Len(t, 0, got, "no signal means no chunks")
}

func TestChunkGenericTypeScript(t *testing.T) {
	dir := t.TempDir()
	src := `export function serializeUser(user: User): string {
  return JSON.stringify(user)
}

const DEFAULT_LIMIT = 20

export class UserStore {
  find(id: string) {
    return this.items.find((i) => i.id === id)
  }
}
`
	writeFile(t, dir, "store.ts", src)

	ix, err := Build(dir)
	assert.NoError(t, err, "build")

	names := make([]string, 0, len(ix.chunks))
	for _, c := range ix.chunks {
		names = append(names, c.Name)
	}
	assert.Equal(t, []string{"serializeUser", "DEFAULT_LIMIT", "UserStore"}, names, "top level declarations")
}

func TestChunkSvelteScriptDeclarations(t *testing.T) {
	dir := t.TempDir()
	src := `<script lang="ts">
  export function formatPrice(cents: number): string {
    return "$" + (cents / 100)
  }

  const CURRENCY = "USD"
</script>

<p>{formatPrice(1299)}</p>
`
	writeFile(t, dir, "Price.svelte", src)

	ix, err := Build(dir)
	assert.NoError(t, err, "build")

	names := make([]string, 0, len(ix.chunks))
	for _, c := range ix.chunks {
		names = append(names, c.Name)
	}
	assert.Equal(t, []string{"formatPrice", "CURRENCY"}, names, "script declarations indexed")
}

func TestChunkSignaturesAreHeaderOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "store.go", storeGo)

	ix, err := Build(dir)
	assert.NoError(t, err, "build")

	byName := make(map[string]chunk, len(ix.chunks))
	for _, c := range ix.chunks {
		byName[c.Name] = c
	}

	assert.Equal(t, "func LoadUser(id string) (User, error) {", byName["LoadUser"].Signature, "func signature stops at the body brace")
	assert.Contains(t, byName["User"].Content, "Name string", "type keeps its fields")
}

func TestQueryCapsChunksPerFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "big.go", `package p

func AlphaThing() {}

func BetaThing() {}

func GammaThing() {}
`)

	ix, err := Build(dir)
	assert.NoError(t, err, "build")

	got := ix.query(types.RetrievalQuery{Root: dir, Identifiers: []string{"thing"}, Limit: 5})
	assert.Len(t, 2, got, "at most two chunks from one file")
}

func TestQueryDedupesIdenticalChunks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package p\n\nfunc DupeThing() {}\n")
	writeFile(t, dir, "b.go", "package p\n\nfunc DupeThing() {}\n")

	ix, err := Build(dir)
	assert.NoError(t, err, "build")

	got := ix.query(types.RetrievalQuery{Root: dir, Identifiers: []string{"dupe", "thing"}, Limit: 5})
	assert.Len(t, 1, got, "identical declarations collapse to one")
}

// waitForRetrieval polls until the manager has a built index and returns
// results, or fails the test.
func waitForRetrieval(t *testing.T, m *Manager, q types.RetrievalQuery) []types.RetrievalChunk {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if chunks := m.Retrieve(q); len(chunks) > 0 {
			return chunks
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("retrieval never returned results")
	return nil
}

func TestManagerServesBuiltIndex(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "store.go", storeGo)

	m := NewManager(Options{TTL: time.Hour})
	q := types.RetrievalQuery{Root: dir, Identifiers: []string{"user"}}

	// Cold: the first lookup starts a background build and returns nothing.
	assert.Len(t, 0, m.Retrieve(q), "cold manager returns nothing")

	// A warm manager serves results, including a stale-triggered rebuild path.
	got := waitForRetrieval(t, m, q)
	assert.Greater(t, len(got), 0, "warm manager serves results")
}

func TestDiscoverSkipsVendorAndNodeModules(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.go", "package app\n\nfunc Main() {}\n")
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "dep"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "node_modules", "dep"), "dep.go", "package dep\n\nfunc Dep() {}\n")

	paths, err := discover(dir)
	assert.NoError(t, err, "discover")
	assert.Len(t, 1, paths, "only the app file is indexed")
}
