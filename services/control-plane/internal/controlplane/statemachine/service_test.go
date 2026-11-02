package statemachine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
)

type memTasks struct {
	store.TaskRepo
	status map[string]domain.TaskStatus
}

func (m *memTasks) CompareAndSetStatus(_ context.Context, id string, from, to domain.TaskStatus, _ time.Time) (bool, error) {
	if m.status[id] != from {
		return false, nil
	}
	m.status[id] = to
	return true, nil
}

type memEvents struct{ store.EventRepo }

func (memEvents) Append(context.Context, *domain.Event) error { return nil }

func TestChildDispatchTransitions(t *testing.T) {
	for _, to := range []domain.TaskStatus{domain.StatusDispatched, domain.StatusBlocked} {
		if !CanTransition(domain.StatusCreated, to) {
			t.Errorf("CREATED → %s must be allowed", to)
		}
	}
	if !CanTransition(domain.StatusDispatched, domain.StatusRunning) {
		t.Error("DISPATCHED → RUNNING must be allowed")
	}
	if !CanTransition(domain.StatusIntegrating, domain.StatusVerifying) ||
		!CanTransition(domain.StatusVerifying, domain.StatusAgentReview) {
		t.Error("parent INTEGRATING → VERIFYING → AGENT_REVIEW must be allowed")
	}
}

func TestTransitionRejectsStaleView(t *testing.T) {
	tasks := &memTasks{status: map[string]domain.TaskStatus{"c1": domain.StatusDispatched}}
	sm := &Service{Tasks: tasks, Events: memEvents{}}

	// Caller still believes the child is CREATED, but another writer claimed it.
	stale := &domain.Task{ID: "c1", Status: domain.StatusCreated}
	err := sm.Transition(context.Background(), stale, domain.StatusDispatched, "claim")
	if !errors.Is(err, ErrStaleTransition) {
		t.Fatalf("want ErrStaleTransition, got %v", err)
	}
	if stale.Status != domain.StatusCreated {
		t.Errorf("in-memory status must not change on a failed transition, got %s", stale.Status)
	}
}

func TestTransitionUpdatesStoreAndTask(t *testing.T) {
	tasks := &memTasks{status: map[string]domain.TaskStatus{"c1": domain.StatusCreated}}
	sm := &Service{Tasks: tasks, Events: memEvents{}}

	task := &domain.Task{ID: "c1", Status: domain.StatusCreated}
	if err := sm.Transition(context.Background(), task, domain.StatusDispatched, "claim"); err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusDispatched || tasks.status["c1"] != domain.StatusDispatched {
		t.Errorf("task=%s store=%s, want DISPATCHED", task.Status, tasks.status["c1"])
	}
}
