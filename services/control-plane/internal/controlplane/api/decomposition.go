package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/delivery"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
	"github.com/google/uuid"
)

// handleHeadEngineerDone processes the head engineer's submit_task_graph output.
func (h *Handlers) handleHeadEngineerDone(run *domain.WorkflowRun, task *domain.Task, submission json.RawMessage) {
	ctx := context.Background()

	var sub struct {
		Tasks []struct {
			ID            string   `json:"id"`
			Title         string   `json:"title"`
			AssignedAgent string   `json:"assignedAgent"`
			Acceptance    []string `json:"acceptance"`
			DependsOn     []string `json:"dependsOn"`
			DiffScope     []string `json:"diffScope,omitempty"`
			Rationale     string   `json:"rationale,omitempty"`
		} `json:"tasks"`
		Notes string `json:"notes,omitempty"`
	}
	if err := json.Unmarshal(submission, &sub); err != nil {
		h.failDecomposition(task, "invalid task graph JSON: "+err.Error())
		return
	}
	if len(sub.Tasks) == 0 {
		h.failDecomposition(task, "head engineer produced no tasks: "+sub.Notes)
		return
	}

	// Validate agent names exist, and that dependency IDs are valid.
	idToTask := map[string]struct{}{}
	for _, t := range sub.Tasks {
		if t.ID == "" || t.Title == "" || t.AssignedAgent == "" || len(t.Acceptance) == 0 {
			h.failDecomposition(task, "task missing required fields: "+t.ID)
			return
		}
		if _, exists := idToTask[t.ID]; exists {
			h.failDecomposition(task, "duplicate task ID: "+t.ID)
			return
		}
		idToTask[t.ID] = struct{}{}
		if _, ok := h.Registry.GetAgent(t.AssignedAgent); !ok {
			h.failDecomposition(task, fmt.Sprintf("task %q assigned to unknown agent %q", t.ID, t.AssignedAgent))
			return
		}
		if kind := h.completionKindOf(t.AssignedAgent); kind != kindWork {
			h.failDecomposition(task, fmt.Sprintf(
				"task %q assigned to %q, a %s agent; subtasks must change code (reviews run automatically after each one)",
				t.ID, t.AssignedAgent, kind))
			return
		}
	}
	for _, t := range sub.Tasks {
		for _, dep := range t.DependsOn {
			if _, ok := idToTask[dep]; !ok {
				h.failDecomposition(task, fmt.Sprintf("task %q depends on unknown ID %q", t.ID, dep))
				return
			}
		}
	}
	graph := make([]graphTask, len(sub.Tasks))
	for i, t := range sub.Tasks {
		graph[i] = graphTask{ID: t.ID, DependsOn: t.DependsOn, Scope: t.DiffScope}
	}
	if cycle := findCycle(graph); cycle != nil {
		h.failDecomposition(task, "dependency cycle: "+strings.Join(cycle, " -> "))
		return
	}
	// Independent tasks whose scopes overlap (or are undeclared) run one after
	// another; only provably disjoint work runs in parallel.
	implicit := serializeOverlaps(graph)

	// The parent branch was created with the task (Phase 11); tasks from
	// before that get one named after their ID here.
	integrationBranch := task.IntegrationBranch
	if integrationBranch == "" {
		integrationBranch = fmt.Sprintf("forge/%s", task.ID)
	}
	if err := h.prepareIntegrationBranch(task, integrationBranch); err != nil {
		h.failDecomposition(task, "prepare integration branch: "+err.Error())
		return
	}
	_ = h.Tasks.UpdateIntegration(ctx, task.ID, task.IntegrationWorktree, integrationBranch, task.IntegrationBaseCommit, time.Now().UTC())

	// Materialize child tasks. Store a stable ID per submission ID for dependency resolution.
	subIDToTaskID := make(map[string]string, len(sub.Tasks))
	for _, t := range sub.Tasks {
		childID := "task-" + uuid.NewString()[:8]
		subIDToTaskID[t.ID] = childID
	}
	usedBranch := map[string]bool{}
	for i, t := range sub.Tasks {
		// Each subtask works on its own branch off the parent branch, created
		// when it is dispatched and merged back into the parent branch.
		branch := delivery.SubtaskBranchName(integrationBranch, t.Title)
		if usedBranch[branch] {
			branch += "-" + delivery.Slug(t.ID)
		}
		usedBranch[branch] = true

		depTaskIDs := make([]string, 0, len(t.DependsOn))
		for _, subID := range t.DependsOn {
			depTaskIDs = append(depTaskIDs, subIDToTaskID[subID])
		}
		var implicitIDs, implicitWhy []string
		for _, d := range implicit {
			if d.Of == i {
				on := sub.Tasks[d.On]
				implicitIDs = append(implicitIDs, subIDToTaskID[on.ID])
				implicitWhy = append(implicitWhy, fmt.Sprintf("waits for %q: %s", on.Title, d.Reason))
			}
		}

		child := &domain.Task{
			ID:                subIDToTaskID[t.ID],
			ParentID:          task.ID,
			Title:             t.Title,
			AssignedAgent:     t.AssignedAgent,
			Requirement:       t.Title + "\n\n" + t.Rationale,
			Acceptance:        t.Acceptance,
			RepoPath:          task.RepoPath,
			BaseCommit:        integrationBranch, // children work off the integration branch
			Status:            domain.StatusCreated,
			DiffScope:         t.DiffScope,
			DependsOn:         depTaskIDs,
			ImplicitDependsOn: implicitIDs,
			Budget:            task.Budget,
			Branch:            branch,
			CreatedAt:         time.Now().UTC(),
			UpdatedAt:         time.Now().UTC(),
		}
		if err := h.Tasks.Create(ctx, child); err != nil {
			h.failDecomposition(task, "create child: "+err.Error())
			return
		}
		_ = h.Events.Append(ctx, &domain.Event{
			TaskID: child.ID, RunID: run.ID,
			Type:   "child.created",
			Reason: t.Title,
			At:     time.Now().UTC(),
		})
		for _, why := range implicitWhy {
			_ = h.Events.Append(ctx, &domain.Event{
				TaskID: child.ID, RunID: run.ID, Type: "child.serialized", Reason: why, At: time.Now().UTC(),
			})
		}
	}

	// Parent is now DISPATCHED; children will be picked up by the scheduler.
	if err := h.SM.Transition(ctx, task, domain.StatusDispatched, fmt.Sprintf("%d children materialized", len(sub.Tasks))); err != nil {
		log.Printf("parent transition to DISPATCHED failed: %v", err)
	}
	_ = h.Events.Append(ctx, &domain.Event{
		TaskID: task.ID, RunID: run.ID,
		Type:    "decomposition.complete",
		Payload: map[string]any{"childCount": len(sub.Tasks), "notes": sub.Notes},
		At:      time.Now().UTC(),
	})
}

// prepareIntegrationBranch creates the parent's integration worktree and branch.
func (h *Handlers) prepareIntegrationBranch(task *domain.Task, branch string) error {
	// A branch created with the task already exists at its pinned base:
	// check it out as it is.
	if task.IntegrationBranch == branch && task.IntegrationBaseCommit != "" {
		worktree, err := workspace.AddWorktreeForBranch(task.RepoPath, branch)
		if err != nil {
			return err
		}
		task.IntegrationWorktree = worktree
		return nil
	}

	// Pin the base to a SHA: a symbolic base like "HEAD" would move with the
	// integration branch, and the final review would diff HEAD..HEAD (empty).
	base, err := workspace.ResolveCommit(context.Background(), task.RepoPath, task.BaseCommit)
	if err != nil {
		return err
	}
	worktree, err := workspace.CreateIntegrationWorktree(task.RepoPath, base, branch)
	if err != nil {
		return err
	}
	task.IntegrationWorktree = worktree
	task.IntegrationBranch = branch
	task.IntegrationBaseCommit = base
	return nil
}

func (h *Handlers) failDecomposition(task *domain.Task, reason string) {
	ctx := context.Background()
	log.Printf("decomposition failed for task %s: %s", task.ID, reason)
	_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "decomposition: "+reason)
}
