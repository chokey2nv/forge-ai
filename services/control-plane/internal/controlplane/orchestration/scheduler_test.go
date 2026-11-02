package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/statemachine"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
)

type memTasks struct {
	store.TaskRepo
	byID map[string]*domain.Task
}

func (m *memTasks) Get(_ context.Context, id string) (*domain.Task, error) {
	t, ok := m.byID[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *t
	return &cp, nil
}

func (m *memTasks) ListByStatus(_ context.Context, s domain.TaskStatus, _ int) ([]*domain.Task, error) {
	var out []*domain.Task
	for _, t := range m.byID {
		if t.Status == s {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *memTasks) ListChildren(_ context.Context, parentID string) ([]*domain.Task, error) {
	var out []*domain.Task
	for _, t := range m.byID {
		if t.ParentID == parentID {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *memTasks) CompareAndSetStatus(_ context.Context, id string, from, to domain.TaskStatus, _ time.Time) (bool, error) {
	t := m.byID[id]
	if t == nil || t.Status != from {
		return false, nil
	}
	t.Status = to
	return true, nil
}

// memRuns reports every run as already existing, so dispatchChild fails right
// after claiming the child and never reaches docker/git.
type memRuns struct{ store.RunRepo }

func (memRuns) Get(_ context.Context, id string) (*domain.WorkflowRun, error) {
	return &domain.WorkflowRun{ID: id}, nil
}

type memEvents struct {
	store.EventRepo
	types []string
}

func (m *memEvents) Append(_ context.Context, e *domain.Event) error {
	m.types = append(m.types, e.Type)
	return nil
}

func newTestScheduler(tasks ...*domain.Task) (*Scheduler, *memTasks, *memEvents) {
	mt := &memTasks{byID: map[string]*domain.Task{}}
	for _, t := range tasks {
		mt.byID[t.ID] = t
	}
	ev := &memEvents{}
	return &Scheduler{
		Tasks:  mt,
		Runs:   memRuns{},
		Events: ev,
		SM:     &statemachine.Service{Tasks: mt, Events: ev},
	}, mt, ev
}

func TestFailedDispatchBlocksChildAndParent(t *testing.T) {
	parent := &domain.Task{ID: "p", Status: domain.StatusDispatched, IntegrationBranch: "forge/p"}
	child := &domain.Task{ID: "c", ParentID: "p", Status: domain.StatusCreated}
	s, mt, _ := newTestScheduler(parent, child)

	s.tick(context.Background())
	if got := mt.byID["c"].Status; got != domain.StatusBlocked {
		t.Fatalf("child after failed dispatch = %s, want BLOCKED (a CREATED child is re-dispatched every tick)", got)
	}

	s.tick(context.Background())
	if got := mt.byID["p"].Status; got != domain.StatusBlocked {
		t.Fatalf("parent with a blocked child = %s, want BLOCKED", got)
	}
}

func TestParentWithoutIntegrationBranchIsNotDispatched(t *testing.T) {
	parent := &domain.Task{ID: "p", Status: domain.StatusDispatched}
	child := &domain.Task{ID: "c", ParentID: "p", Status: domain.StatusCreated}
	s, mt, _ := newTestScheduler(parent, child)

	err := s.dispatchChild(context.Background(), mt.byID["p"], mt.byID["c"])
	if err == nil {
		t.Fatal("want an error for a parent with no integration branch")
	}
	if got := mt.byID["c"].Status; got != domain.StatusCreated {
		t.Errorf("child must not be claimed, got %s", got)
	}
}

func TestStaleClaimIsSkippedNotBlocked(t *testing.T) {
	parent := &domain.Task{ID: "p", Status: domain.StatusDispatched, IntegrationBranch: "forge/p"}
	child := &domain.Task{ID: "c", ParentID: "p", Status: domain.StatusCreated}
	s, mt, _ := newTestScheduler(parent, child)

	// Scheduler's view says CREATED; the store already says RUNNING.
	view := *mt.byID["c"]
	mt.byID["c"].Status = domain.StatusRunning

	err := s.dispatchChild(context.Background(), mt.byID["p"], &view)
	if !errors.Is(err, statemachine.ErrStaleTransition) {
		t.Fatalf("want ErrStaleTransition, got %v", err)
	}
	if got := mt.byID["c"].Status; got != domain.StatusRunning {
		t.Errorf("running child must be left alone, got %s", got)
	}
}
