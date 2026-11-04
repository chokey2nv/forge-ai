package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/statemachine"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
)

// casTasks stores task statuses the way the state machine sees them.
type casTasks struct {
	store.TaskRepo
	status map[string]domain.TaskStatus
}

func (c *casTasks) CompareAndSetStatus(_ context.Context, id string, from, to domain.TaskStatus, _ time.Time) (bool, error) {
	if c.status[id] != from {
		return false, nil
	}
	c.status[id] = to
	return true, nil
}

func (c *casTasks) Get(_ context.Context, id string) (*domain.Task, error) {
	s, ok := c.status[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return &domain.Task{ID: id, Status: s}, nil
}

type statusRuns struct {
	store.RunRepo
	run *domain.WorkflowRun
}

func (r *statusRuns) UpdateStatus(_ context.Context, _ string, s domain.RunStatus, _ string, _ time.Time) error {
	r.run.Status = s
	return nil
}

func (r *statusRuns) Get(context.Context, string) (*domain.WorkflowRun, error) {
	cp := *r.run
	return &cp, nil
}

// The watcher marks a run RESUMABLE (no heartbeat) just before its agent's
// completion lands. The finished work must not be lost.
func TestCompletionAfterStaleMarking(t *testing.T) {
	released := time.Now()
	cases := []struct {
		name       string
		stored     domain.TaskStatus // what the watcher (or someone) left
		released   *time.Time
		proceeds   bool
		wantTask   domain.TaskStatus
		wantRun    domain.RunStatus
		wantRevive bool
	}{
		{"watcher won, workspace still there: completion used", domain.StatusResumable, nil, true, domain.StatusVerifying, domain.RunVerifying, true},
		{"watcher won and the workspace is gone: stays resumable", domain.StatusResumable, &released, false, domain.StatusResumable, domain.RunResumable, false},
		{"someone rejected it meanwhile: left alone", domain.StatusRejected, nil, false, domain.StatusRejected, domain.RunVerifying, false},
		{"no race", domain.StatusRunning, nil, true, domain.StatusVerifying, domain.RunVerifying, false},
	}
	for _, c := range cases {
		tasks := &casTasks{status: map[string]domain.TaskStatus{"c": c.stored}}
		runs := &statusRuns{run: &domain.WorkflowRun{ID: "run-c", TaskID: "c", Status: domain.RunResumable, WorkspaceReleasedAt: c.released}}
		events := &recordEvents{}
		h := &Handlers{Tasks: tasks, Runs: runs, Events: events, SM: &statemachine.Service{Tasks: tasks, Events: events}}

		task := &domain.Task{ID: "c", Status: domain.StatusRunning} // the completion's view, from before the watcher
		got := h.startVerifying(context.Background(), runs.run, task)
		if got != c.proceeds || tasks.status["c"] != c.wantTask || runs.run.Status != c.wantRun {
			t.Errorf("%s: proceeds=%v task=%s run=%s, want %v %s %s", c.name, got, tasks.status["c"], runs.run.Status, c.proceeds, c.wantTask, c.wantRun)
		}
		if revived := events.has("run.revived"); revived != c.wantRevive {
			t.Errorf("%s: run.revived event %v, want %v", c.name, revived, c.wantRevive)
		}
	}
}

// Marked stale, the run's workspace is removed while the agent reports: its
// empty diff must neither replace the checkpoint nor block the run.
func TestEmptyCompletionFromStaleRun(t *testing.T) {
	stale := &domain.WorkflowRun{ID: "run-c", TaskID: "c", Status: domain.RunResumable,
		LastCheckpoint: &domain.Checkpoint{WorkspaceDiff: "diff --git a/x b/x"}}
	if keptDiff(stale, " \n") != "diff --git a/x b/x" {
		t.Error("an empty diff from a stale run replaced the checkpoint's")
	}
	if keptDiff(stale, "new") != "new" {
		t.Error("a real diff from a stale run is used")
	}
	if keptDiff(&domain.WorkflowRun{Status: domain.RunRunning, LastCheckpoint: stale.LastCheckpoint}, "") != "" {
		t.Error("a live run's empty diff is reported as it is")
	}

	tasks := &casTasks{status: map[string]domain.TaskStatus{"c": domain.StatusResumable}}
	runs := &statusRuns{run: stale}
	events := &recordEvents{}
	h := &Handlers{Tasks: tasks, Runs: runs, Events: events, SM: &statemachine.Service{Tasks: tasks, Events: events}}
	h.handleImplementerDone(context.Background(), stale, &domain.Task{ID: "c", Status: domain.StatusResumable},
		AgentCompleteBody{Status: "complete", FinalMessage: "Fixed it."})
	if runs.run.Status != domain.RunResumable || tasks.status["c"] != domain.StatusResumable {
		t.Errorf("run %s, task %s: both stay resumable", runs.run.Status, tasks.status["c"])
	}
}

type recordEvents struct {
	store.EventRepo
	types []string
}

func (e *recordEvents) Append(_ context.Context, ev *domain.Event) error {
	e.types = append(e.types, ev.Type)
	return nil
}

func (e *recordEvents) has(t string) bool {
	for _, x := range e.types {
		if x == t {
			return true
		}
	}
	return false
}
