package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/adapter"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

func (h *Handlers) GetRun(w http.ResponseWriter, r *http.Request) {
	run, err := h.Runs.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *Handlers) ListEvents(w http.ResponseWriter, r *http.Request) {
	events, err := h.Events.ListByRun(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (h *Handlers) GetDiff(w http.ResponseWriter, r *http.Request) {
	run, err := h.Runs.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	diff := ""
	if run.LastCheckpoint != nil {
		diff = run.LastCheckpoint.WorkspaceDiff
	}
	writeJSON(w, http.StatusOK, map[string]string{"runId": run.ID, "diff": diff})
}

/*
	 func (h *Handlers) runVerification(run *domain.WorkflowRun, task *domain.Task) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("verification panic for run %s: %v", run.ID, rec)
				bg := context.Background()
				_ = h.Runs.UpdateStatus(bg, run.ID, domain.RunBlocked, fmt.Sprintf("verification panic: %v", rec), time.Now().UTC())
				_ = h.SM.Transition(bg, task, domain.StatusBlocked, fmt.Sprintf("verification panic: %v", rec))
			}
		}()

		ctx := context.Background()
		attempt := run.VerificationAttempt + 1

		vr := h.Verifier.Run(ctx, run.ContainerName, run.WorktreePath, diffScopeOf(task))
		vr.ID = fmt.Sprintf("vr-%s-%d", run.ID, attempt)
		vr.RunID = run.ID
		vr.TaskID = task.ID
		vr.Attempt = attempt
		_ = h.Verifications.Create(ctx, vr)

		_ = h.Events.Append(ctx, &domain.Event{
			RunID: run.ID, Type: "verification.gate",
			Payload: map[string]any{"attempt": attempt, "passed": vr.Passed},
			At:      time.Now().UTC(),
		})

		if vr.Passed {
			_ = h.Runs.UpdateVerification(ctx, run.ID, attempt, true, domain.RunComplete, "", time.Now().UTC())
			_ = h.SM.Transition(ctx, task, domain.StatusReviewPending, "verification passed")
			_ = h.Events.Append(ctx, &domain.Event{RunID: run.ID, Type: "verification.pass", At: time.Now().UTC()})
			workspace.Cleanup(ctx, &workspace.Workspace{WorktreePath: run.WorktreePath, ContainerName: run.ContainerName}, task.RepoPath)
			return
		}

		// Failed
		if attempt >= h.MaxVerificationAttempts {
			reason := "verification failed after " + fmt.Sprint(attempt) + " attempts"
			_ = h.Runs.UpdateVerification(ctx, run.ID, attempt, false, domain.RunBlocked, reason, time.Now().UTC())
			_ = h.SM.Transition(ctx, task, domain.StatusBlocked, reason)
			_ = h.Events.Append(ctx, &domain.Event{RunID: run.ID, Type: "verification.exhausted", Reason: reason, At: time.Now().UTC()})
			workspace.Cleanup(ctx, &workspace.Workspace{WorktreePath: run.WorktreePath, ContainerName: run.ContainerName}, task.RepoPath)
			return
		}

		// Retry: transition VERIFYING → RUNNING, provision fresh workspace with checkpoint, re-invoke agent with diagnostics.
		_ = h.Runs.UpdateVerification(ctx, run.ID, attempt, false, domain.RunRunning, "", time.Now().UTC())
		if err := h.SM.Transition(ctx, task, domain.StatusRunning, "verification failed; retrying"); err != nil {
			_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, "state transition failed: "+err.Error(), time.Now().UTC())
			return
		}
		_ = h.Events.Append(ctx, &domain.Event{
			RunID: run.ID, Type: "verification.retry",
			Payload: map[string]any{"attempt": attempt},
			At:      time.Now().UTC(),
		})

		// Retire the current container; a fresh one will be provisioned.
		workspace.Cleanup(ctx, &workspace.Workspace{WorktreePath: run.WorktreePath, ContainerName: run.ContainerName}, task.RepoPath)

		// Provision fresh workspace.
		latest, _ := h.Runs.Get(ctx, run.ID)
		ws, err := workspace.ProvisionWithSetup(ctx, task.RepoPath, latest.BaseCommit, h.WorkerImage, run.ID+"-r"+fmt.Sprint(attempt), h.SetupTimeout)
		if err != nil {
			_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, "retry provision: "+err.Error(), time.Now().UTC())
			_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "retry provision failed")
			return
		}
		if latest.LastCheckpoint != nil {
			if err := workspace.ApplyDiff(ctx, ws.WorktreePath, latest.LastCheckpoint.WorkspaceDiff); err != nil {
				_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, "apply diff: "+err.Error(), time.Now().UTC())
				_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "apply diff failed")
				workspace.Cleanup(ctx, ws, task.RepoPath)
				return
			}
		}
		_ = h.Runs.UpdateWorkspace(ctx, run.ID, ws.WorktreePath, ws.ContainerName, time.Now().UTC())

		diagnostics := "The previous attempt failed verification:\n\n" + h.Verifier.SummaryText(vr) +
			"\n\nFix the issues above and try again. The changes so far are preserved."

		_, err = h.Adapter.StartRun(ctx, &adapter.StartRunRequest{
			RunID:         run.ID + "-r" + fmt.Sprint(attempt),
			ContainerName: ws.ContainerName,
			WorktreePath:  ws.WorktreePath,
			Task:          task.Requirement,
			SystemPrompt:  h.SystemPrompt,
			CallbackURL:   h.SelfURL,
			Provider:      latest.Provenance.Provider,
			Model:         latest.Provenance.Model,
			MaxSteps:      task.Budget.MaxSteps,
			MaxRetries:    task.Budget.MaxRetries,
			WallClockMS:   task.Budget.WallClockMS,
			Checkpoint:    checkpointToMap(latest.LastCheckpoint),
			Diagnostics:   diagnostics,
		})
		if err != nil {
			_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, "adapter retry: "+err.Error(), time.Now().UTC())
			_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "adapter retry failed")
			workspace.Cleanup(ctx, ws, task.RepoPath)
		}
	}
*/
func (h *Handlers) GetVerification(w http.ResponseWriter, r *http.Request) {
	vr, err := h.Verifications.LatestForRun(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, vr)
}

func (h *Handlers) RecordComplete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := r.PathValue("id")

	var body struct {
		Status        string           `json:"status"`
		FinalMessage  string           `json:"finalMessage"`
		StepCount     int              `json:"stepCount"`
		Usage         []domain.Usage   `json:"usage"`
		WorkspaceDiff string           `json:"workspaceDiff"`
		Messages      []domain.Message `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	run, err := h.Runs.Get(ctx, runID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	task, err := h.Tasks.Get(ctx, run.TaskID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Persist final checkpoint
	cp := &domain.Checkpoint{
		Messages: body.Messages, StepCount: body.StepCount,
		Usage: body.Usage, Status: body.Status,
		WorkspaceDiff: body.WorkspaceDiff, SavedAt: time.Now().UTC(),
	}
	_ = h.Runs.SaveCheckpoint(ctx, runID, cp, time.Now().UTC())

	// Agent-reported blocked/error: no verification, just block.
	if body.Status != "complete" {
		_ = h.Runs.UpdateStatus(ctx, runID, domain.RunBlocked, body.FinalMessage, time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, body.FinalMessage)
		_ = h.Events.Append(ctx, &domain.Event{RunID: runID, Type: "complete", Reason: body.FinalMessage, At: time.Now().UTC()})
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Agent claims complete → verify.
	_ = h.Runs.UpdateStatus(ctx, runID, domain.RunVerifying, "", time.Now().UTC())
	if err := h.SM.Transition(ctx, task, domain.StatusVerifying, "agent reported complete"); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	_ = h.Events.Append(ctx, &domain.Event{RunID: runID, Type: "verification.start", At: time.Now().UTC()})

	// Respond immediately; verification runs asynchronously.
	w.WriteHeader(http.StatusNoContent)

	go h.runVerification(run, task)
}

func (h *Handlers) runVerification(run *domain.WorkflowRun, task *domain.Task) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("verification panic for run %s: %v", run.ID, rec)
			bg := context.Background()
			_ = h.Runs.UpdateStatus(bg, run.ID, domain.RunBlocked, fmt.Sprintf("verification panic: %v", rec), time.Now().UTC())
			_ = h.SM.Transition(bg, task, domain.StatusBlocked, fmt.Sprintf("verification panic: %v", rec))
		}
	}()

	ctx := context.Background()
	attempt := run.VerificationAttempt + 1

	vr := h.Verifier.Run(ctx, run.ContainerName, run.WorktreePath, diffScopeOf(task))
	vr.ID = fmt.Sprintf("vr-%s-%d", run.ID, attempt)
	vr.RunID = run.ID
	vr.TaskID = task.ID
	vr.Attempt = attempt
	_ = h.Verifications.Create(ctx, vr)

	_ = h.Events.Append(ctx, &domain.Event{
		RunID: run.ID, Type: "verification.gate",
		Payload: map[string]any{"attempt": attempt, "passed": vr.Passed},
		At:      time.Now().UTC(),
	})

	if vr.Passed {
		_ = h.Runs.UpdateVerification(ctx, run.ID, attempt, true, domain.RunComplete, "", time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusReviewPending, "verification passed")
		_ = h.Events.Append(ctx, &domain.Event{RunID: run.ID, Type: "verification.pass", At: time.Now().UTC()})
		workspace.Cleanup(ctx, &workspace.Workspace{WorktreePath: run.WorktreePath, ContainerName: run.ContainerName}, task.RepoPath)
		return
	}

	// Failed
	if attempt >= h.MaxVerificationAttempts {
		reason := "verification failed after " + fmt.Sprint(attempt) + " attempts"
		_ = h.Runs.UpdateVerification(ctx, run.ID, attempt, false, domain.RunBlocked, reason, time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, reason)
		_ = h.Events.Append(ctx, &domain.Event{RunID: run.ID, Type: "verification.exhausted", Reason: reason, At: time.Now().UTC()})
		workspace.Cleanup(ctx, &workspace.Workspace{WorktreePath: run.WorktreePath, ContainerName: run.ContainerName}, task.RepoPath)
		return
	}

	// Retry: transition VERIFYING → RUNNING, provision fresh workspace with checkpoint, re-invoke agent with diagnostics.
	_ = h.Runs.UpdateVerification(ctx, run.ID, attempt, false, domain.RunRunning, "", time.Now().UTC())
	if err := h.SM.Transition(ctx, task, domain.StatusRunning, "verification failed; retrying"); err != nil {
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, "state transition failed: "+err.Error(), time.Now().UTC())
		return
	}
	_ = h.Events.Append(ctx, &domain.Event{
		RunID: run.ID, Type: "verification.retry",
		Payload: map[string]any{"attempt": attempt},
		At:      time.Now().UTC(),
	})

	// Retire the current container; a fresh one will be provisioned.
	workspace.Cleanup(ctx, &workspace.Workspace{WorktreePath: run.WorktreePath, ContainerName: run.ContainerName}, task.RepoPath)

	// Provision fresh workspace.
	latest, _ := h.Runs.Get(ctx, run.ID)
	ws, err := workspace.ProvisionWithSetup(ctx, task.RepoPath, latest.BaseCommit, h.WorkerImage, run.ID+"-r"+fmt.Sprint(attempt), h.SetupTimeout)
	if err != nil {
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, "retry provision: "+err.Error(), time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "retry provision failed")
		return
	}
	if latest.LastCheckpoint != nil {
		if err := workspace.ApplyDiff(ctx, ws.WorktreePath, latest.LastCheckpoint.WorkspaceDiff); err != nil {
			_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, "apply diff: "+err.Error(), time.Now().UTC())
			_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "apply diff failed")
			workspace.Cleanup(ctx, ws, task.RepoPath)
			return
		}
	}
	_ = h.Runs.UpdateWorkspace(ctx, run.ID, ws.WorktreePath, ws.ContainerName, time.Now().UTC())

	diagnostics := "The previous attempt failed verification:\n\n" + h.Verifier.SummaryText(vr) +
		"\n\nFix the issues above and try again. The changes so far are preserved."

	_, err = h.Adapter.StartRun(ctx, &adapter.StartRunRequest{
		RunID:         run.ID + "-r" + fmt.Sprint(attempt),
		ContainerName: ws.ContainerName,
		WorktreePath:  ws.WorktreePath,
		Task:          task.Requirement,
		SystemPrompt:  h.SystemPrompt,
		CallbackURL:   h.SelfURL,
		Provider:      latest.Provenance.Provider,
		Model:         latest.Provenance.Model,
		MaxSteps:      task.Budget.MaxSteps,
		MaxRetries:    task.Budget.MaxRetries,
		WallClockMS:   task.Budget.WallClockMS,
		Checkpoint:    checkpointToMap(latest.LastCheckpoint),
		Diagnostics:   diagnostics,
	})
	if err != nil {
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, "adapter retry: "+err.Error(), time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "adapter retry failed")
		workspace.Cleanup(ctx, ws, task.RepoPath)
	}
}

func diffScopeOf(t *domain.Task) []string {
	// If task.Acceptance contains a line starting with "diff-scope:", use it.
	// Simpler: check t for a DiffScope field once added.
	return t.DiffScope
}

func checkpointToMap(cp *domain.Checkpoint) any {
	if cp == nil {
		return nil
	}
	return map[string]any{
		"messages":      cp.Messages,
		"stepCount":     cp.StepCount,
		"usage":         cp.Usage,
		"status":        cp.Status,
		"workspaceDiff": cp.WorkspaceDiff,
	}
}
