package runner

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

// activeStatuses are the task states in which an agent, verifier or reviewer
// may still be using the task's container. In every other state the
// workspace is dead weight: resume and retry provision a fresh one from the
// checkpoint diff.
var activeStatuses = []domain.TaskStatus{
	domain.StatusDecomposing,
	domain.StatusDispatched,
	domain.StatusRunning,
	domain.StatusVerifying,
	domain.StatusAgentReview,
	domain.StatusIntegrating,
	domain.StatusDelivering, // re-verification after the target moved
}

// Reaper removes agent containers and workspace directories once nothing can
// use them, and sweeps labeled containers and workspace directories that no
// active run owns (crashes, killed dependency installs, replaced retries).
type Reaper struct {
	lastVolumeSweep time.Time
	Runs            store.RunRepo
	Tasks           store.TaskRepo

	// OrphanGrace protects containers created moments ago whose run record
	// is not written yet.
	OrphanGrace time.Duration
	Interval    time.Duration

	// Overridable for tests.
	ListContainers  func(ctx context.Context) ([]workspace.Container, error)
	RemoveContainer func(ctx context.Context, name string)
	ListDirs        func() ([]workspace.WorkspaceDir, error)
	RemoveDir       func(ctx context.Context, path string)
	Release         func(ctx context.Context, ws *workspace.Workspace, repoPath string)
	Now             func() time.Time
}

func NewReaper(runs store.RunRepo, tasks store.TaskRepo, orphanGrace time.Duration) *Reaper {
	return &Reaper{
		Runs: runs, Tasks: tasks,
		OrphanGrace:     orphanGrace,
		Interval:        30 * time.Second,
		ListContainers:  workspace.ListManagedContainers,
		RemoveContainer: workspace.RemoveContainer,
		ListDirs:        workspace.ListWorkspaceDirs,
		RemoveDir:       workspace.RemoveOrphanDir,
		Release:         workspace.Cleanup,
		Now:             time.Now,
	}
}

func (r *Reaper) Run(ctx context.Context) {
	r.Sweep(ctx) // reclaim leftovers from before a restart right away
	t := time.NewTicker(r.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Sweep(ctx)
		}
	}
}

// Sweep runs one reclamation pass.
func (r *Reaper) Sweep(ctx context.Context) {
	if time.Since(r.lastVolumeSweep) > time.Hour {
		r.lastVolumeSweep = time.Now()
		workspace.RemoveUnusedDepsVolumes(ctx)
	}
	active, err := r.activeTasks(ctx)
	if err != nil {
		log.Printf("reaper: list active tasks: %v", err)
		return // without this we cannot tell what is in use
	}

	runs, err := r.Runs.ListUnreleasedWorkspaces(ctx)
	if err != nil {
		log.Printf("reaper: list runs: %v", err)
		return
	}
	inUse := map[string]bool{} // container names and workspace paths
	for _, run := range runs {
		if _, ok := active[run.TaskID]; ok {
			inUse[run.ContainerName] = true
			inUse[run.WorktreePath] = true
			continue
		}
		repoPath := ""
		if t, err := r.Tasks.Get(ctx, run.TaskID); err == nil {
			repoPath = t.RepoPath
		}
		r.Release(ctx, &workspace.Workspace{
			WorktreePath: run.WorktreePath, ContainerName: run.ContainerName,
		}, repoPath)
		if err := r.Runs.MarkWorkspaceReleased(ctx, run.ID, r.Now().UTC()); err != nil {
			log.Printf("reaper: mark %s released: %v", run.ID, err)
			continue
		}
		log.Printf("reaper: released workspace of run %s (container %s)", run.ID, run.ContainerName)
	}

	containers, err := r.ListContainers(ctx)
	if err != nil {
		log.Printf("reaper: list containers: %v", err)
		return
	}
	for _, c := range containers {
		if inUse[c.Name] || labelsActiveTask(c.Label, active) {
			continue
		}
		if r.Now().Sub(c.Created) < r.OrphanGrace {
			continue
		}
		r.RemoveContainer(ctx, c.Name)
		log.Printf("reaper: removed orphaned container %s (label %q)", c.Name, c.Label)
	}

	dirs, err := r.ListDirs()
	if err != nil {
		log.Printf("reaper: list workspace dirs: %v", err)
		return
	}
	removedDirs := 0
	for _, d := range dirs {
		if inUse[d.Path] || r.Now().Sub(d.ModTime) < r.OrphanGrace {
			continue
		}
		r.RemoveDir(ctx, d.Path)
		removedDirs++
	}
	if removedDirs > 0 {
		log.Printf("reaper: removed %d orphaned workspace directories", removedDirs)
	}
}

func (r *Reaper) activeTasks(ctx context.Context) (map[string]struct{}, error) {
	active := map[string]struct{}{}
	for _, s := range activeStatuses {
		tasks, err := r.Tasks.ListByStatus(ctx, s, 0)
		if err != nil {
			return nil, err
		}
		for _, t := range tasks {
			active[t.ID] = struct{}{}
		}
	}
	return active, nil
}

// labelsActiveTask reports whether a container label names an active task.
// Child and parent containers are labeled with the task ID itself, and
// retries with run IDs that embed it (run-<task>-ri); this covers containers
// that exist before their run record does, e.g. the parent's verification
// container.
func labelsActiveTask(label string, active map[string]struct{}) bool {
	for id := range active {
		if strings.Contains(label, id) {
			return true
		}
	}
	return false
}
