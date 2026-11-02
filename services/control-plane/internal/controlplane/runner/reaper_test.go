package runner

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

type fakeTasks struct {
	store.TaskRepo
	byID map[string]*domain.Task
}

func (f *fakeTasks) Get(_ context.Context, id string) (*domain.Task, error) {
	if t, ok := f.byID[id]; ok {
		return t, nil
	}
	return nil, errors.New("not found")
}

func (f *fakeTasks) ListByStatus(_ context.Context, s domain.TaskStatus, _ int) ([]*domain.Task, error) {
	var out []*domain.Task
	for _, t := range f.byID {
		if t.Status == s {
			out = append(out, t)
		}
	}
	return out, nil
}

type fakeRuns struct {
	store.RunRepo
	runs     []*domain.WorkflowRun
	released map[string]bool
}

func (f *fakeRuns) ListUnreleasedWorkspaces(context.Context) ([]*domain.WorkflowRun, error) {
	var out []*domain.WorkflowRun
	for _, r := range f.runs {
		if !f.released[r.ID] {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeRuns) MarkWorkspaceReleased(_ context.Context, id string, _ time.Time) error {
	f.released[id] = true
	return nil
}

func TestSweepReleasesFinishedRunsAndOrphans(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	old, fresh := now.Add(-time.Hour), now.Add(-time.Minute)

	tasks := &fakeTasks{byID: map[string]*domain.Task{
		"task-blocked": {ID: "task-blocked", Status: domain.StatusBlocked, RepoPath: "/repo"},
		"task-running": {ID: "task-running", Status: domain.StatusRunning, RepoPath: "/repo"},
		"task-parent":  {ID: "task-parent", Status: domain.StatusIntegrating, RepoPath: "/repo"},
		"task-done":    {ID: "task-done", Status: domain.StatusReviewPending, RepoPath: "/repo"},
	}}
	runs := &fakeRuns{released: map[string]bool{}, runs: []*domain.WorkflowRun{
		{ID: "run-task-blocked", TaskID: "task-blocked", ContainerName: "forge-worker-task-blocked", WorktreePath: "/tmp/forge-child-1"},
		{ID: "run-task-running", TaskID: "task-running", ContainerName: "forge-worker-task-running", WorktreePath: "/tmp/forge-child-live"},
		{ID: "run-task-done-final-review", TaskID: "task-done", ContainerName: "forge-worker-task-done"},
		{ID: "run-gone", TaskID: "task-deleted", ContainerName: "forge-worker-run-gone"},
	}}

	containers := []workspace.Container{
		{Name: "forge-worker-task-running", Label: "task-running", Created: old},               // in use
		{Name: "forge-worker-run-task-running-ri", Label: "run-task-running-ri", Created: old}, // active task's retry
		{Name: "forge-worker-task-parent", Label: "task-parent", Created: old},                 // parent verification, no run yet
		{Name: "forge-worker-run-abc", Label: "run-abc", Created: old},                         // orphan
		{Name: "forge-setup-forge-child-9", Label: "setup-forge-child-9", Created: old},        // killed install
		{Name: "forge-worker-run-new", Label: "run-new", Created: fresh},                       // run record not written yet
	}

	var released, removed, removedDirs []string
	r := &Reaper{
		Runs: runs, Tasks: tasks, OrphanGrace: 10 * time.Minute,
		ListContainers:  func(context.Context) ([]workspace.Container, error) { return containers, nil },
		RemoveContainer: func(_ context.Context, name string) { removed = append(removed, name) },
		Release: func(_ context.Context, ws *workspace.Workspace, _ string) {
			released = append(released, ws.ContainerName)
		},
		ListDirs: func() ([]workspace.WorkspaceDir, error) {
			return []workspace.WorkspaceDir{
				{Path: "/tmp/forge-child-live", ModTime: old},   // RUNNING task's workspace
				{Path: "/tmp/forge-child-orphan", ModTime: old}, // leaked by a failed provision
				{Path: "/tmp/forge-wt-new", ModTime: fresh},     // being provisioned
			}, nil
		},
		RemoveDir: func(_ context.Context, p string) { removedDirs = append(removedDirs, p) },
		Now:       func() time.Time { return now },
	}
	r.Sweep(context.Background())

	sort.Strings(released)
	wantReleased := []string{"forge-worker-run-gone", "forge-worker-task-blocked", "forge-worker-task-done"}
	if !equal(released, wantReleased) {
		t.Errorf("released = %v, want %v", released, wantReleased)
	}
	if runs.released["run-task-running"] {
		t.Error("run of a RUNNING task must keep its workspace")
	}

	sort.Strings(removed)
	wantRemoved := []string{"forge-setup-forge-child-9", "forge-worker-run-abc"}
	if !equal(removed, wantRemoved) {
		t.Errorf("removed orphans = %v, want %v", removed, wantRemoved)
	}

	if len(removedDirs) != 1 || removedDirs[0] != "/tmp/forge-child-orphan" {
		t.Errorf("removed dirs = %v, want only the orphan", removedDirs)
	}

	// A second pass must not release anything again.
	released, removed = nil, nil
	containers = nil
	r.Sweep(context.Background())
	if len(released) != 0 {
		t.Errorf("second sweep re-released %v", released)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
