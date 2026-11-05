package api

import (
	"context"
	"net/http"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/adapter"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

// resumeNote tells a resumed agent what happened: its history may end with
// failures from a workspace that was removed, or with it giving up.
const resumeNote = "You were interrupted and this is a new workspace, restored from your last checkpoint. " +
	"Errors above about a missing container no longer apply. Anything you wrote after that checkpoint may be gone: " +
	"check with git_status and read before you continue, then finish the task."

func (h *Handlers) ResumeRun(w http.ResponseWriter, r *http.Request) {
	// Installing packages can take minutes: leaving the page doesn't stop it.
	ctx := context.WithoutCancel(r.Context())
	runID := r.PathValue("id")

	run, err := h.Runs.Get(ctx, runID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, busy := h.resuming.LoadOrStore(runID, true); busy {
		http.Error(w, "this run is already being resumed; installing packages can take minutes", http.StatusConflict)
		return
	}
	defer h.resuming.Delete(runID)
	// PROVISIONING with no resume under way: one failed or was cut off by a
	// restart, and can be tried again.
	if run.Status == domain.RunProvisioning {
		run.Status = domain.RunResumable
		_ = h.Runs.UpdateStatus(ctx, runID, run.Status, "", time.Now().UTC())
	}
	if run.Status != domain.RunResumable && run.Status != domain.RunBlocked {
		http.Error(w, "run is not resumable (status="+string(run.Status)+")", http.StatusConflict)
		return
	}
	statusBefore, reasonBefore := run.Status, run.BlockedReason
	// A run refused before its agent started (a spend cap, say) has no
	// checkpoint: resuming it starts the agent from the beginning.
	if run.LastCheckpoint == nil && run.Status != domain.RunBlocked {
		http.Error(w, "no checkpoint to resume from", http.StatusConflict)
		return
	}
	task, _ := h.Tasks.Get(ctx, run.TaskID)
	if reason := h.overBudget(ctx, task); reason != "" {
		http.Error(w, (&BudgetError{Reason: reason}).Error(), http.StatusConflict)
		return
	}
	def, sysPrompt, tools, err := h.composeImplementer(run, task)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Re-provision fresh worktree + container
	// ws, err := workspace.Provision(ctx, task.RepoPath, run.BaseCommit, h.WorkerImage, run.ID+"-r")
	// if err != nil {
	// 	http.Error(w, err.Error(), http.StatusInternalServerError)
	// 	return
	// }
	_ = h.Runs.UpdateStatus(ctx, runID, domain.RunProvisioning, "", time.Now().UTC())
	ws, err := workspace.ProvisionWithSetup(ctx, task.RepoPath, run.BaseCommit, h.WorkerImage, run.ID+"-r", h.SetupTimeout)
	if err != nil {
		_ = h.Runs.UpdateStatus(ctx, runID, statusBefore, reasonBefore, time.Now().UTC())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() {
		if err != nil {
			workspace.Cleanup(ctx, ws, task.RepoPath)
		}
	}()

	// Restore worktree from checkpoint
	var checkpoint any
	if run.LastCheckpoint != nil {
		checkpoint = run.LastCheckpoint
		if err := workspace.ApplyDiff(ctx, ws.WorktreePath, run.LastCheckpoint.WorkspaceDiff); err != nil {
			workspace.Cleanup(ctx, ws, task.RepoPath)
			_ = h.Runs.UpdateStatus(ctx, runID, statusBefore, reasonBefore, time.Now().UTC())
			http.Error(w, "apply checkpoint diff: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Verification and review run in the run's recorded workspace; point it at
	// the new one and retire the old (its agent is gone).
	workspace.Cleanup(ctx, &workspace.Workspace{WorktreePath: run.WorktreePath, ContainerName: run.ContainerName}, task.RepoPath)
	_ = h.Runs.UpdateWorkspace(ctx, runID, ws.WorktreePath, ws.ContainerName, time.Now().UTC())

	if err := h.SM.Transition(ctx, task, domain.StatusRunning, "resume requested"); err != nil {
		workspace.Cleanup(ctx, ws, task.RepoPath)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	_ = h.Runs.UpdateStatus(ctx, runID, domain.RunRunning, "", time.Now().UTC())
	h.reopenParent(ctx, task)

	if _, err := h.Adapter.StartRun(ctx, &adapter.StartRunRequest{
		RunID:          run.ID + "-r",
		CallbackRunID:  run.ID,
		ContainerName:  ws.ContainerName,
		WorktreePath:   ws.WorktreePath,
		Role:           def.Name,
		Task:           task.Requirement + "\n\nAcceptance:\n" + bulletList(task.Acceptance) + h.subtaskBrief(ctx, task, false),
		SystemPrompt:   sysPrompt,
		Tools:          tools,
		SubmissionTool: def.SubmissionTool,
		CallbackURL:    h.SelfURL,
		Provider:       run.Provenance.Provider,
		Model:          run.Provenance.Model,
		MaxSteps:       task.Budget.MaxSteps,
		MaxRetries:     task.Budget.MaxRetries,
		WallClockMS:    task.Budget.WallClockMS,
		Checkpoint:     checkpoint,
		Diagnostics:    resumeNote,
	}); err != nil {
		_ = h.Runs.UpdateStatus(ctx, runID, domain.RunBlocked, err.Error(), time.Now().UTC())
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"runId": runID, "status": "RUNNING"})
}

// reopenParent: a parent blocked because this subtask was blocked waits for
// its subtasks again once the subtask is resumed. The scheduler blocks it
// again if another subtask is still blocked.
func (h *Handlers) reopenParent(ctx context.Context, child *domain.Task) {
	if child.ParentID == "" {
		return
	}
	parent, err := h.Tasks.Get(ctx, child.ParentID)
	if err != nil || parent.Status != domain.StatusBlocked || parent.Delivery != nil {
		return
	}
	_ = h.SM.Transition(ctx, parent, domain.StatusDispatched, "subtask "+child.ID+" resumed")
}
