package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

// failedVerification is the run's latest verification if it failed: what an
// escalated run needs a decision on, rather than its reviewers' verdicts.
func (h *Handlers) failedVerification(ctx context.Context, runID string) *domain.VerificationRun {
	vr := h.latestVerification(ctx, runID)
	if vr == nil || vr.Passed {
		return nil
	}
	return vr
}

// failedGates names the gates that failed, for a reason a person reads.
func failedGates(vr *domain.VerificationRun) string {
	var names []string
	for _, g := range vr.Gates {
		if !g.Passed && !g.Skipped {
			names = append(names, g.Name)
		}
	}
	return strings.Join(names, ", ")
}

// stillVerifying reports whether the task is still waiting for the checks
// that just ran: someone may have stopped them to decide.
func (h *Handlers) stillVerifying(ctx context.Context, taskID string) bool {
	task, err := h.Tasks.Get(ctx, taskID)
	return err != nil || task.Status == domain.StatusVerifying
}

// escalationBlocker is why a task has nothing for a person to decide on, or "".
// A person can take over while checks run, or once the task is blocked for any
// reason (failed checks, no model credit, out of steps), if there is work to
// accept. A delivery has its own retry, and a parent is decided on once all its
// subtasks are in.
func (h *Handlers) escalationBlocker(ctx context.Context, task *domain.Task) string {
	switch task.Status {
	case domain.StatusVerifying, domain.StatusAgentReview:
	case domain.StatusBlocked:
		if task.Delivery != nil {
			return "its delivery failed; retry that instead"
		}
	default:
		return "task is not blocked or being checked (current: " + string(task.Status) + ")"
	}
	if isParentReview(task) {
		children, _ := h.Tasks.ListChildren(ctx, task.ID)
		for _, c := range children {
			if c.Status != domain.StatusIntegrated {
				return "subtask " + c.ID + " is " + string(c.Status) + "; decide on that first"
			}
		}
		if len(children) == 0 {
			return "it has no subtasks, so no work to accept"
		}
		return ""
	}
	run, err := h.Runs.Get(ctx, task.LatestRunID)
	if err != nil {
		return "it has no run"
	}
	if run.LastCheckpoint == nil || strings.TrimSpace(run.LastCheckpoint.WorkspaceDiff) == "" {
		return "it has no saved work to accept"
	}
	return ""
}

// EscalateTask takes a task to a person, who then accepts the change as it is,
// sends it back or rejects it (POST /tasks/{id}/escalation). Accepting calls no
// model, so this is also the way forward when none can be reached. Asking is
// open to anyone who can start work; deciding still takes a reviewer.
func (h *Handlers) EscalateTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	task, err := h.Tasks.Get(ctx, r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if why := h.escalationBlocker(ctx, task); why != "" {
		http.Error(w, "can't escalate: "+why, http.StatusConflict)
		return
	}
	who := actorOf(r)
	now := time.Now().UTC()
	checking := task.Status != domain.StatusBlocked

	runID := task.LatestRunID
	if isParentReview(task) {
		runID = finalReviewRunID(task)
	}
	run, runErr := h.Runs.Get(ctx, runID)

	var reason string
	failed := h.failedVerification(ctx, runID)
	switch {
	case checking:
		reason = fmt.Sprintf("%s stopped the checks to decide (%s)", who.Name, task.Status)
	case failed != nil:
		reason = fmt.Sprintf("verification failed (%s); escalated by %s", failedGates(failed), who.Name)
	case runErr == nil && run.BlockedReason != "":
		reason = fmt.Sprintf("%s; escalated by %s", run.BlockedReason, who.Name)
	default:
		reason = "blocked; escalated by " + who.Name
	}

	// The state changes first: checks stopped below see it and discard their result.
	if err := h.SM.Transition(ctx, task, domain.StatusEscalated, reason); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if runErr == nil {
		_ = h.Runs.UpdateStatus(ctx, runID, domain.RunEscalated, reason, now)
		if p := run.ReviewPending; p != nil {
			_ = h.Runs.SetReviewPending(ctx, runID, nil, now) // a late verdict is ignored
			for _, i := range p.Outstanding {
				_ = h.Adapter.CancelRun(ctx, reviewerRunID(run, p, i))
			}
		}
	} else if isParentReview(task) {
		// A parent's final review run is made once the combined change passes.
		provider, model := parentProvenance(ctx, h, task)
		err = h.Runs.Create(ctx, &domain.WorkflowRun{
			ID: runID, TaskID: task.ID, Status: domain.RunEscalated, BlockedReason: reason,
			BaseCommit:    task.IntegrationBaseCommit,
			Provenance:    domain.Provenance{AgentDefinition: "final-review", Provider: provider, Model: model},
			LastHeartbeat: now, CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			http.Error(w, "create the run to decide on: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if checking {
		bg := context.WithoutCancel(ctx)
		if isParentReview(task) {
			workspace.RemoveContainer(bg, "forge-worker-"+task.ID)
		} else if runErr == nil {
			workspace.Cleanup(bg, &workspace.Workspace{WorktreePath: run.WorktreePath, ContainerName: run.ContainerName}, task.RepoPath)
		}
	}
	_ = h.Events.Append(ctx, &domain.Event{
		TaskID: task.ID, RunID: runID, Type: "task.escalated", Reason: reason, Actor: who.Name, ActorID: who.ID, At: now,
	})
	h.reopenParent(ctx, task) // it waits on an escalated subtask instead of being blocked by it
	writeJSON(w, http.StatusAccepted, map[string]string{"taskId": task.ID, "status": string(domain.StatusEscalated)})
}

// verificationFeedback is the failed gates plus the human's guidance, for the
// agent a failed verification is sent back to.
func (h *Handlers) verificationFeedback(vr *domain.VerificationRun, req escalationRequest) string {
	var b strings.Builder
	b.WriteString("The previous attempt failed verification:\n\n")
	b.WriteString(h.Verifier.SummaryText(vr))
	fmt.Fprintf(&b, "\n## Guidance from %s (human reviewer)\n", req.Reviewer)
	if req.Guidance != "" {
		b.WriteString(req.Guidance)
	} else {
		b.WriteString("No extra guidance; fix what failed.")
	}
	b.WriteString("\n")
	return b.String()
}
