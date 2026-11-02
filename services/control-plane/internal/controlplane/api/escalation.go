package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

// Escalation decisions for a task whose reviewers kept rejecting it.
const (
	EscalationAccept = "accept" // integrate (child) or go to human approval (root)
	EscalationRetry  = "retry"  // one more round, with the human's guidance
	EscalationReject = "reject"
)

type escalationRequest struct {
	Decision  string `json:"decision"`
	Reviewer  string `json:"reviewer"`
	Rationale string `json:"rationale,omitempty"`
	Guidance  string `json:"guidance,omitempty"`
}

// validateEscalation checks a decision against the task it targets.
func validateEscalation(task *domain.Task, req escalationRequest) (status int, msg string) {
	if req.Reviewer == "" {
		return http.StatusBadRequest, "reviewer is required"
	}
	switch req.Decision {
	case EscalationAccept, EscalationReject:
	case EscalationRetry:
		if isParentReview(task) {
			return http.StatusConflict, "a parent's combined change has no implementer to send back to; accept or reject it"
		}
	default:
		return http.StatusBadRequest, "decision must be accept, retry or reject"
	}
	if task.Status != domain.StatusEscalated {
		return http.StatusConflict, "task is not ESCALATED (current: " + string(task.Status) + ")"
	}
	return 0, ""
}

// escalatedRunID is the run whose review was escalated.
func escalatedRunID(task *domain.Task) string {
	if isParentReview(task) {
		return "run-" + task.ID + "-final-review"
	}
	return task.LatestRunID
}

// DecideEscalation applies a human's decision to an ESCALATED task.
func (h *Handlers) DecideEscalation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req escalationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.Decision = strings.ToLower(strings.TrimSpace(req.Decision))

	task, err := h.Tasks.Get(ctx, r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if code, msg := validateEscalation(task, req); code != 0 {
		http.Error(w, msg, code)
		return
	}
	run, err := h.Runs.Get(ctx, escalatedRunID(task))
	if err != nil {
		http.Error(w, "escalated run not found: "+err.Error(), http.StatusInternalServerError)
		return
	}

	now := time.Now().UTC()
	reason := fmt.Sprintf("escalation %s by %s", req.Decision, req.Reviewer)
	if req.Rationale != "" {
		reason += ": " + req.Rationale
	}
	_ = h.Approvals.Create(ctx, &domain.Approval{
		TaskID: task.ID, RunID: run.ID, Action: "escalation-" + req.Decision,
		Reviewer: req.Reviewer, Rationale: req.Rationale, DecidedAt: now,
	})
	_ = h.Events.Append(ctx, &domain.Event{
		TaskID: task.ID, RunID: run.ID, Type: "escalation." + req.Decision, Reason: reason, At: now,
	})

	bg := context.WithoutCancel(ctx)
	switch req.Decision {
	case EscalationAccept:
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunComplete, "", now)
		if task.ParentID != "" {
			h.integrateChild(bg, run, task) // ESCALATED -> INTEGRATED, or BLOCKED on conflict
		} else if err := h.SM.Transition(ctx, task, domain.StatusReviewPending, reason); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}

	case EscalationReject:
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, reason, now)
		if err := h.SM.Transition(ctx, task, domain.StatusRejected, reason); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}

	case EscalationRetry:
		feedback, err := h.escalationFeedback(ctx, run, req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := h.SM.Transition(ctx, task, domain.StatusRunning, reason); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		// One more round, numbered after the escalated one, with fresh
		// verification attempts. Another rejection escalates again.
		_ = h.Runs.StartReviewRound(ctx, run.ID, run.ReviewRound+1, now)
		go h.reinvokeImplementer(run, task, feedback) // provisioning takes a while
	}

	latest, _ := h.Tasks.Get(ctx, task.ID)
	status := task.Status
	if latest != nil {
		status = latest.Status
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"taskId": task.ID, "decision": req.Decision, "status": string(status)})
}

// escalationFeedback is the escalated round's rejections plus the human's
// guidance, for the implementer.
func (h *Handlers) escalationFeedback(ctx context.Context, run *domain.WorkflowRun, req escalationRequest) (string, error) {
	all, err := h.Reviews.ListByRun(ctx, run.ID)
	if err != nil {
		return "", fmt.Errorf("load review verdicts: %w", err)
	}
	escalatedRound := run.ReviewRound + 1 // the round that ran last; escalation does not advance the counter
	var round []*domain.Review
	for _, rv := range all {
		if rv.Round == escalatedRound {
			round = append(round, rv)
		}
	}
	return escalationMessage(round, req), nil
}

func escalationMessage(round []*domain.Review, req escalationRequest) string {
	var b strings.Builder
	b.WriteString(rejectionFeedback(round))
	fmt.Fprintf(&b, "\n## Guidance from %s (human reviewer)\n", req.Reviewer)
	if req.Guidance != "" {
		b.WriteString(req.Guidance)
	} else {
		b.WriteString("No extra guidance; address the reviewers' defects.")
	}
	b.WriteString("\n")
	return b.String()
}
