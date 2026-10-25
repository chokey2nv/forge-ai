package api

import (
	"net/http"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/adapter"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

func (h *Handlers) ResumeRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := r.PathValue("id")

	run, err := h.Runs.Get(ctx, runID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if run.Status != domain.RunResumable && run.Status != domain.RunBlocked {
		http.Error(w, "run is not resumable (status="+string(run.Status)+")", http.StatusConflict)
		return
	}
	if run.LastCheckpoint == nil {
		http.Error(w, "no checkpoint to resume from", http.StatusConflict)
		return
	}
	task, _ := h.Tasks.Get(ctx, run.TaskID)

	// Re-provision fresh worktree + container
	// ws, err := workspace.Provision(ctx, task.RepoPath, run.BaseCommit, h.WorkerImage, run.ID+"-r")
	// if err != nil {
	// 	http.Error(w, err.Error(), http.StatusInternalServerError)
	// 	return
	// }
	ws, err := workspace.ProvisionWithSetup(ctx, task.RepoPath, run.BaseCommit, h.WorkerImage, run.ID+"-r", h.SetupTimeout)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() {
		if err != nil {
			workspace.Cleanup(ctx, ws, task.RepoPath)
		}
	}()

	// Restore worktree from checkpoint
	if err := workspace.ApplyDiff(ctx, ws.WorktreePath, run.LastCheckpoint.WorkspaceDiff); err != nil {
		workspace.Cleanup(ctx, ws, task.RepoPath)
		http.Error(w, "apply checkpoint diff: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := h.SM.Transition(ctx, task, domain.StatusRunning, "resume requested"); err != nil {
		workspace.Cleanup(ctx, ws, task.RepoPath)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	_ = h.Runs.UpdateStatus(ctx, runID, domain.RunRunning, "", time.Now().UTC())

	if _, err := h.Adapter.StartRun(ctx, &adapter.StartRunRequest{
		RunID:         run.ID + "-r",
		ContainerName: ws.ContainerName,
		WorktreePath:  ws.WorktreePath,
		Task:          task.Requirement,
		SystemPrompt:  h.SystemPrompt,
		CallbackURL:   h.SelfURL,
		Provider:      run.Provenance.Provider,
		Model:         run.Provenance.Model,
		MaxSteps:      task.Budget.MaxSteps,
		MaxRetries:    task.Budget.MaxRetries,
		WallClockMS:   task.Budget.WallClockMS,
		Checkpoint:    run.LastCheckpoint,
	}); err != nil {
		_ = h.Runs.UpdateStatus(ctx, runID, domain.RunBlocked, err.Error(), time.Now().UTC())
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"runId": runID, "status": "RUNNING"})
}
