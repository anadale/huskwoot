package storage

import (
	"context"
	"sync"

	"github.com/anadale/huskwoot/internal/model"
)

// CachedTaskStore is a decorator over model.TaskStore that caches ListProjects in memory.
// The cache is reset by an explicit Invalidate() call; write methods in transactions
// (CreateProjectTx/UpdateProjectTx) do not touch the cache — otherwise a concurrent
// ListProjects between invalidate() and tx.Commit() could cache the pre-commit state
// and return a stale list until the next invalidation.
// The use case must call Invalidate() after tx.Commit().
//
// Used to serve frequent agent requests (injecting the project list into the system
// prompt) without extra database round-trips.
type CachedTaskStore struct {
	model.TaskStore

	mu       sync.RWMutex
	cached   []model.Project
	cacheSet bool
}

// NewCachedTaskStore wraps inner and returns a cached decorator.
func NewCachedTaskStore(inner model.TaskStore) *CachedTaskStore {
	return &CachedTaskStore{TaskStore: inner}
}

// ListProjects returns the project list from the cache; on the first call (or after
// invalidation) fetches from the underlying store. Errors are not cached.
func (s *CachedTaskStore) ListProjects(ctx context.Context) ([]model.Project, error) {
	s.mu.RLock()
	if s.cacheSet {
		out := cloneProjects(s.cached)
		s.mu.RUnlock()
		return out, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cacheSet {
		return cloneProjects(s.cached), nil
	}

	projects, err := s.TaskStore.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	s.cached = cloneProjects(projects)
	s.cacheSet = true
	return cloneProjects(s.cached), nil
}

// Invalidate resets the cache. The use case must call this method AFTER a successful
// tx.Commit() for operations that change the project set — otherwise a race between
// invalidate and commit could leave a pre-commit state in the cache.
func (s *CachedTaskStore) Invalidate() {
	s.mu.Lock()
	s.cached = nil
	s.cacheSet = false
	s.mu.Unlock()
}

func cloneProjects(src []model.Project) []model.Project {
	if src == nil {
		return nil
	}
	out := make([]model.Project, len(src))
	copy(out, src)
	return out
}
