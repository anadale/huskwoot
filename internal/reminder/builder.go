package reminder

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// Builder builds a model.Summary from the open tasks in TaskStore.
type Builder struct {
	taskStore model.TaskStore
	cfg       BuilderConfig
}

// NewBuilder creates a Builder.
func NewBuilder(store model.TaskStore, cfg BuilderConfig) *Builder {
	return &Builder{taskStore: store, cfg: cfg}
}

// Build assembles the task summary for slot slot at time at.
func (b *Builder) Build(ctx context.Context, slot string, at time.Time) (model.Summary, error) {
	tasks, err := b.taskStore.ListTasks(ctx, "", model.TaskFilter{Status: "open"})
	if err != nil {
		return model.Summary{}, fmt.Errorf("fetching open tasks: %w", err)
	}

	projects, err := b.taskStore.ListProjects(ctx)
	if err != nil {
		return model.Summary{}, fmt.Errorf("fetching projects: %w", err)
	}
	projectByID := make(map[string]model.Project, len(projects))
	for _, p := range projects {
		projectByID[p.ID] = p
	}

	loc := at.Location()
	startOfDay := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, loc)
	endOfDay := startOfDay.AddDate(0, 0, 1)
	planLimit := startOfDay.Add(b.cfg.PlansHorizon)

	var overdue, today, upcoming, undatedAll []model.Task

	for _, t := range tasks {
		if t.Deadline == nil {
			undatedAll = append(undatedAll, t)
			continue
		}
		d := *t.Deadline
		switch {
		case d.Before(at):
			overdue = append(overdue, t)
		case d.Before(endOfDay):
			today = append(today, t)
		case d.Before(planLimit):
			upcoming = append(upcoming, t)
			// beyond the horizon — discard
		}
	}

	sort.SliceStable(undatedAll, func(i, j int) bool {
		return undatedAll[i].CreatedAt.Before(undatedAll[j].CreatedAt)
	})
	undatedTotal := len(undatedAll)
	undated := undatedAll
	if b.cfg.UndatedLimit == 0 {
		undated = nil
	} else if len(undated) > b.cfg.UndatedLimit {
		undated = undated[:b.cfg.UndatedLimit]
	}

	defaultID := b.taskStore.DefaultProjectID()
	summary := model.Summary{
		GeneratedAt:  at,
		Slot:         slot,
		Overdue:      groupByProject(overdue, projectByID, defaultID, sortByDeadlineAsc),
		Today:        groupByProject(today, projectByID, defaultID, sortByDeadlineAsc),
		Upcoming:     groupByProject(upcoming, projectByID, defaultID, sortByDeadlineAsc),
		Undated:      groupByProject(undated, projectByID, defaultID, sortByCreatedAtAsc),
		UndatedTotal: undatedTotal,
	}
	summary.IsEmpty = len(summary.Overdue) == 0 && len(summary.Today) == 0 &&
		len(summary.Upcoming) == 0 && len(summary.Undated) == 0

	return summary, nil
}

func sortByDeadlineAsc(tasks []model.Task) {
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].Deadline == nil || tasks[j].Deadline == nil {
			return false
		}
		return tasks[i].Deadline.Before(*tasks[j].Deadline)
	})
}

func sortByCreatedAtAsc(tasks []model.Task) {
	sort.SliceStable(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})
}

// groupByProject groups tasks into []ProjectGroup, sorted: Inbox first, rest by ProjectName asc.
func groupByProject(tasks []model.Task, projectByID map[string]model.Project, defaultID string, sortTasks func([]model.Task)) []model.ProjectGroup {
	if len(tasks) == 0 {
		return nil
	}

	grouped := make(map[string][]model.Task)
	for _, t := range tasks {
		grouped[t.ProjectID] = append(grouped[t.ProjectID], t)
	}

	for pid := range grouped {
		sortTasks(grouped[pid])
	}

	// Collect project IDs: Inbox first, rest by ProjectName asc
	ids := make([]string, 0, len(grouped))
	for pid := range grouped {
		ids = append(ids, pid)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		if ids[i] == defaultID {
			return true
		}
		if ids[j] == defaultID {
			return false
		}
		return projectByID[ids[i]].Name < projectByID[ids[j]].Name
	})

	result := make([]model.ProjectGroup, 0, len(ids))
	for _, pid := range ids {
		p := projectByID[pid]
		result = append(result, model.ProjectGroup{
			ProjectID:   pid,
			ProjectName: p.Name,
			Tasks:       grouped[pid],
		})
	}
	return result
}
