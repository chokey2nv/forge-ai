package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
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
			Addresses     []string `json:"addresses,omitempty"` // follow-up: review threads it handles
		} `json:"tasks"`
		Responses []graphResponse `json:"responses,omitempty"` // follow-up: threads answered without code
		Notes     string          `json:"notes,omitempty"`
	}
	// A follow-up (Phase 12) that cannot be used goes back to the human's
	// decision; a first decomposition that cannot be used blocks the task.
	followUp := currentFollowUp(task, run.ID)
	fail := func(reason string) {
		if followUp != nil {
			h.refuseFollowUp(ctx, task, followUp, reason)
			return
		}
		h.failDecomposition(task, reason)
	}
	if err := json.Unmarshal(submission, &sub); err != nil {
		fail("invalid task graph JSON: " + err.Error())
		return
	}
	if len(sub.Tasks) == 0 {
		fail("head engineer produced no tasks: " + sub.Notes)
		return
	}

	// Validate agent names exist, and that dependency IDs are valid.
	idToTask := map[string]struct{}{}
	for _, t := range sub.Tasks {
		if t.ID == "" || t.Title == "" || t.AssignedAgent == "" || len(t.Acceptance) == 0 {
			fail("task missing required fields: " + t.ID)
			return
		}
		if _, exists := idToTask[t.ID]; exists {
			fail("duplicate task ID: " + t.ID)
			return
		}
		idToTask[t.ID] = struct{}{}
		if _, ok := h.Registry.GetAgent(t.AssignedAgent); !ok {
			fail(fmt.Sprintf("task %q assigned to unknown agent %q", t.ID, t.AssignedAgent))
			return
		}
		if kind := h.completionKindOf(t.AssignedAgent); kind != kindWork {
			fail(fmt.Sprintf(
				"task %q assigned to %q, a %s agent; subtasks must change code (reviews run automatically after each one)",
				t.ID, t.AssignedAgent, kind))
			return
		}
	}
	for _, t := range sub.Tasks {
		for _, dep := range t.DependsOn {
			if _, ok := idToTask[dep]; !ok {
				fail(fmt.Sprintf("task %q depends on unknown ID %q", t.ID, dep))
				return
			}
		}
	}
	graph := make([]graphTask, len(sub.Tasks))
	for i, t := range sub.Tasks {
		graph[i] = graphTask{ID: t.ID, DependsOn: t.DependsOn, Scope: t.DiffScope}
	}
	if cycle := findCycle(graph); cycle != nil {
		fail("dependency cycle: " + strings.Join(cycle, " -> "))
		return
	}
	// Independent tasks whose scopes overlap (or are undeclared) run one after
	// another; only provably disjoint work runs in parallel.
	implicit := serializeOverlaps(graph)

	// A follow-up must handle every open review thread.
	var threads []*domain.Thread
	var addressedBy map[string][]string
	var answers map[string]string
	if followUp != nil {
		threads = h.openThreads(ctx, task.ID)
		addresses := map[string][]string{}
		for _, t := range sub.Tasks {
			if len(t.Addresses) > 0 {
				addresses[t.ID] = t.Addresses
			}
		}
		var err error
		if addressedBy, answers, err = followUpGraph(threads, addresses, sub.Responses); err != nil {
			fail(err.Error())
			return
		}
	}

	// The parent branch was created with the task (Phase 11); tasks from
	// before that get one named after their ID here.
	integrationBranch := task.IntegrationBranch
	if integrationBranch == "" {
		integrationBranch = fmt.Sprintf("forge/%s", task.ID)
	}
	if err := h.prepareIntegrationBranch(task, integrationBranch); err != nil {
		fail("prepare integration branch: " + err.Error())
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
	if followUp != nil {
		existing, _ := h.Tasks.ListChildren(ctx, task.ID)
		for _, c := range existing {
			usedBranch[c.Branch] = true
		}
	}
	for i, t := range sub.Tasks {
		// Each subtask works on its own branch off the parent branch, created
		// when it is dispatched and merged back into the parent branch.
		branch := delivery.SubtaskBranchName(integrationBranch, t.Title)
		if usedBranch[branch] {
			branch += "-" + delivery.Slug(t.ID)
		}
		// A follow-up's subtask may share a title with an earlier one.
		for n, b := 2, branch; usedBranch[branch]; n++ {
			branch = fmt.Sprintf("%s-%d", b, n)
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
			Requirement:       t.Title + "\n\n" + t.Rationale + threadBrief(threads, addressedBy[t.ID]),
			Addresses:         addressedBy[t.ID],
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
			fail("create child: " + err.Error())
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

	reason := fmt.Sprintf("%d children materialized", len(sub.Tasks))
	if followUp != nil {
		h.applyThreadHandling(ctx, run, subIDToTaskID, addressedBy, answers)
		_ = h.Tasks.SetFollowUpOutcome(ctx, task.ID, followUp.N, "planned", time.Now().UTC())
		reason = fmt.Sprintf("follow-up %d: %d subtask(s) planned, %d thread(s) addressed, %d answered",
			followUp.N, len(sub.Tasks), countAddressed(addressedBy), len(answers))
	}

	// Parent is now DISPATCHED; children will be picked up by the scheduler.
	if err := h.SM.Transition(ctx, task, domain.StatusDispatched, reason); err != nil {
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
	// A follow-up keeps working in the worktree the first round made.
	if task.IntegrationWorktree != "" && task.IntegrationBranch == branch {
		if _, err := os.Stat(task.IntegrationWorktree); err == nil {
			return nil
		}
	}
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

// applyThreadHandling records which follow-up subtask takes each thread on,
// and posts the head engineer's direct answers as agent replies.
func (h *Handlers) applyThreadHandling(ctx context.Context, run *domain.WorkflowRun, subIDToTaskID map[string]string, addressedBy map[string][]string, answers map[string]string) {
	now := time.Now().UTC()
	for subID, ids := range addressedBy {
		for _, id := range ids {
			_ = h.Threads.SetAddressedBy(ctx, id, subIDToTaskID[subID], now)
		}
	}
	for id, body := range answers {
		_ = h.Threads.AddComment(ctx, id, domain.Comment{Author: "head-engineer", Kind: domain.CommentAgent, Body: body, At: now})
		_ = h.Threads.SetStatus(ctx, id, domain.ThreadAddressed, "", now)
		_ = h.Events.Append(ctx, &domain.Event{TaskID: run.TaskID, RunID: run.ID, Type: "thread.answered", Reason: id + " by head-engineer", At: now})
	}
}

func countAddressed(byTask map[string][]string) int {
	seen := map[string]bool{}
	for _, ids := range byTask {
		for _, id := range ids {
			seen[id] = true
		}
	}
	return len(seen)
}
