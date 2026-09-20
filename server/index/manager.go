package index

import (
	"sync"
	"time"

	"cursortab/logger"
	"cursortab/types"
)

const (
	// defaultTTL is how long a built index is served before a refresh is
	// kicked off in the background.
	defaultTTL = 60 * time.Second
)

// Options configures a Manager.
type Options struct {
	// TTL is how long a built index is served before a background refresh.
	TTL time.Duration
}

// Manager serves retrieval lookups per workspace root. The first lookup for a
// root starts a background build and returns nothing, so a cold index never
// blocks a completion. Later lookups hit the built index while a stale one is
// refreshed in place.
type Manager struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]*entry
	build   func(root string) (*index, error)
}

type entry struct {
	idx      *index
	builtAt  time.Time
	building bool
}

// NewManager returns a Manager with the default refresh window.
func NewManager(opts Options) *Manager {
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	return &Manager{
		ttl:     ttl,
		entries: make(map[string]*entry),
		build:   Build,
	}
}

// Retrieve answers a query from the cached index for the query's root. It
// never blocks on a build: a cold or stale root returns whatever index is
// already available while a rebuild runs in the background.
func (m *Manager) Retrieve(q types.RetrievalQuery) []types.RetrievalChunk {
	if q.Root == "" {
		return nil
	}

	m.mu.Lock()
	e := m.entries[q.Root]
	if e == nil {
		m.startBuildLocked(q.Root)
		m.mu.Unlock()
		return nil
	}
	if time.Since(e.builtAt) > m.ttl {
		m.startBuildLocked(q.Root)
	}
	idx := e.idx
	m.mu.Unlock()

	if idx == nil {
		return nil
	}
	return idx.query(q)
}

func (m *Manager) startBuildLocked(root string) {
	e := m.entries[root]
	if e == nil {
		e = &entry{}
		m.entries[root] = e
	}
	if e.building {
		return
	}
	e.building = true

	go func() {
		idx, err := m.build(root)
		m.mu.Lock()
		current := m.entries[root]
		if current != nil {
			current.building = false
			if err == nil {
				current.idx = idx
				current.builtAt = time.Now()
			}
		}
		m.mu.Unlock()

		if err != nil {
			logger.Debug("index: build failed for %s: %v", root, err)
			return
		}
		logger.Debug("index: built %d chunks for %s", len(idx.chunks), root)
	}()
}
