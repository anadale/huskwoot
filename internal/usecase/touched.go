package usecase

import (
	"context"
	"sync"

	"github.com/anadale/huskwoot/internal/model"
)

// touchedSet is a concurrency-safe set of unique strings preserving insertion
// order. Used by ChatService to aggregate task/project IDs touched during a
// single agent invocation.
type touchedSet struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	order []string
}

func newTouchedSet() *touchedSet {
	return &touchedSet{seen: make(map[string]struct{})}
}

func (t *touchedSet) add(id string) {
	if id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.seen[id]; ok {
		return
	}
	t.seen[id] = struct{}{}
	t.order = append(t.order, id)
}

func (t *touchedSet) values() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.order))
	copy(out, t.order)
	return out
}

type touchedKey int

const (
	touchedTasksKey touchedKey = iota
	touchedProjectsKey
)

// withTouched returns a context carrying touched-ID sets for tasks and
// projects. The sets are also returned directly so the caller can read them
// after the processing chain completes.
func withTouched(ctx context.Context) (context.Context, *touchedSet, *touchedSet) {
	tasks := newTouchedSet()
	projects := newTouchedSet()
	ctx = context.WithValue(ctx, touchedTasksKey, tasks)
	ctx = context.WithValue(ctx, touchedProjectsKey, projects)
	return ctx, tasks, projects
}

func appendTouchedTasks(ctx context.Context, tasks []model.Task) {
	set, ok := ctx.Value(touchedTasksKey).(*touchedSet)
	if !ok || set == nil {
		return
	}
	for _, t := range tasks {
		set.add(t.ID)
	}
}

func appendTouchedProjects(ctx context.Context, projects []model.Project) {
	set, ok := ctx.Value(touchedProjectsKey).(*touchedSet)
	if !ok || set == nil {
		return
	}
	for _, p := range projects {
		set.add(p.ID)
	}
}
