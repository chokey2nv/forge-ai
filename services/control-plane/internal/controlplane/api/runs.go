package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/agent"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/adapter"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/statemachine"
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

func (h *Handlers) GetVerification(w http.ResponseWriter, r *http.Request) {
	vr, err := h.Verifications.LatestForRun(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, vr)
}

type AgentCompleteBody struct {
	Role          string           `json:"role"`
	Status        string           `json:"status"`
	FinalMessage  string           `json:"finalMessage"`
	Submission    json.RawMessage  `json:"submission,omitempty"`
	StepCount     int              `json:"stepCount"`
	Usage         []domain.Usage   `json:"usage"`
	WorkspaceDiff string           `json:"workspaceDiff"`
	Messages      []domain.Message `json:"messages"`
	Spend         *SpendReport     `json:"spend,omitempty"`
}

func (h *Handlers) RecordComplete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := r.PathValue("id")

	var body AgentCompleteBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

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

	// A reviewer reports against the implementer's run ID; its conversation
	// must not replace the implementer's checkpoint, which retries resume from.
	if h.completionKindOf(body.Role) == kindReview {
		_ = h.Runs.Heartbeat(ctx, runID, time.Now().UTC())
	} else {
		cp := &domain.Checkpoint{
			Messages: body.Messages, StepCount: body.StepCount, Usage: body.Usage,
			Status: body.Status, WorkspaceDiff: keptDiff(run, body.WorkspaceDiff), SavedAt: time.Now().UTC(),
		}
		if err := h.Runs.SaveCheckpoint(ctx, runID, cp, time.Now().UTC()); err != nil {
			log.Printf("complete %s: save checkpoint failed (diff %d bytes): %v", runID, len(cp.WorkspaceDiff), err)
		}
	}

	h.recordSpend(ctx, run, task, body.Role, body.Spend)
	h.dispatchCompletion(ctx, run, task, body)

	w.WriteHeader(http.StatusNoContent)
}
func (h *Handlers) handleImplementerDone(ctx context.Context, run *domain.WorkflowRun, task *domain.Task, body AgentCompleteBody) {
	if body.Status != "complete" {
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, body.FinalMessage, time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, body.FinalMessage)
		return
	}
	// Marked stale, its workspace can be removed while the agent is reporting:
	// an empty diff then says nothing about its work. The task stays RESUMABLE,
	// and a resume rebuilds the workspace from the last real checkpoint.
	if strings.TrimSpace(body.WorkspaceDiff) == "" && run.Status == domain.RunResumable {
		log.Printf("complete %s: empty diff from a run marked stale; leaving it resumable", run.ID)
		return
	}
	// Implementers have no submission tool, so a text-only "I'm blocked" reply
	// arrives as status=complete. An unchanged workspace is never a success.
	if strings.TrimSpace(body.WorkspaceDiff) == "" {
		reason := "agent finished without changing the workspace: " + body.FinalMessage
		if strings.TrimSpace(body.FinalMessage) == "" {
			reason = "agent finished without changing the workspace, and gave no explanation"
		}
		reason += ". A subtask has to produce a change to review; to get a report, ask for it to be written to a file"
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, reason, time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, reason)
		return
	}

	if !h.startVerifying(ctx, run, task) {
		return
	}
	// Detach from the callback request: its context ends when we return 204.
	go h.runVerificationAndReview(context.WithoutCancel(ctx), run, task)
}

// keptDiff is the diff to checkpoint: a run marked stale may report an empty
// one because its workspace is being removed, which must not replace the
// work a resume would rebuild from.
func keptDiff(run *domain.WorkflowRun, diff string) string {
	if strings.TrimSpace(diff) == "" && run.Status == domain.RunResumable && run.LastCheckpoint != nil {
		return run.LastCheckpoint.WorkspaceDiff
	}
	return diff
}

// startVerifying moves a task whose agent reported complete to VERIFYING.
// The stale-run watcher may have marked it RESUMABLE moments before the
// completion arrived (no heartbeat for STALE_AFTER while the agent was in
// fact finishing). The agent did finish, so its work is used, unless the
// reaper has already released its workspace: then the task stays RESUMABLE
// and a resume rebuilds the workspace from the checkpoint just saved.
func (h *Handlers) startVerifying(ctx context.Context, run *domain.WorkflowRun, task *domain.Task) bool {
	now := time.Now().UTC()
	_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunVerifying, "", now)
	err := h.SM.Transition(ctx, task, domain.StatusVerifying, "agent reported complete")
	if err == nil {
		return true
	}
	if errors.Is(err, statemachine.ErrStaleTransition) {
		fresh, terr := h.Tasks.Get(ctx, task.ID)
		latest, rerr := h.Runs.Get(ctx, run.ID)
		if terr == nil && fresh.Status == domain.StatusResumable {
			if rerr == nil && latest.WorkspaceReleasedAt == nil {
				*task = *fresh
				if h.SM.Transition(ctx, task, domain.StatusRunning, "the agent finished just after its run was marked stale; its completion is used") == nil &&
					h.SM.Transition(ctx, task, domain.StatusVerifying, "agent reported complete") == nil {
					_ = h.Events.Append(ctx, &domain.Event{TaskID: task.ID, RunID: run.ID, Type: "run.revived",
						Reason: "completion arrived after the watcher marked the run stale", At: now})
					return true
				}
			}
			_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunResumable, "completion arrived after the run was marked stale and its workspace released; resume it", now)
		}
	}
	log.Printf("transition to VERIFYING failed: %v", err)
	return false
}
func (h *Handlers) runVerificationAndReview(ctx context.Context, run *domain.WorkflowRun, task *domain.Task) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("verification panic for run %s: %v", run.ID, rec)
			_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, fmt.Sprintf("verification panic: %v", rec), time.Now().UTC())
			_ = h.SM.Transition(ctx, task, domain.StatusBlocked, fmt.Sprintf("verification panic: %v", rec))
		}
	}()

	attempt := run.VerificationAttempt + 1

	vr := h.Verifier.Run(ctx, run.ContainerName, run.WorktreePath, task.DiffScope)
	vr.ID = fmt.Sprintf("vr-%s-r%d-%d", run.ID, run.ReviewRound, attempt)
	vr.RunID, vr.TaskID, vr.Attempt = run.ID, task.ID, attempt
	_ = h.Verifications.Create(ctx, vr)

	if !vr.Passed {
		h.handleVerificationFailure(run, task, vr, attempt)
		return
	}

	// Verification passed → hand to reviewer (or straight to REVIEW_PENDING).
	if !h.ReviewerEnabled {
		h.passWithoutReviewer(ctx, run, task, attempt, "verification passed")
		return
	}

	if err := h.SM.Transition(ctx, task, domain.StatusAgentReview, "verification passed, reviewing"); err != nil {
		log.Printf("transition to AGENT_REVIEW failed: %v", err)
		return
	}
	_ = h.Runs.UpdateVerification(ctx, run.ID, attempt, true, domain.RunVerifying, "", time.Now().UTC())

	h.startReviews(ctx, run, task)
}

// passWithoutReviewer finishes a verified run that has no agent reviewer: a
// child integrates into its parent, a root goes to human review.
func (h *Handlers) passWithoutReviewer(ctx context.Context, run *domain.WorkflowRun, task *domain.Task, attempt int, reason string) {
	_ = h.Runs.UpdateVerification(ctx, run.ID, attempt, true, domain.RunComplete, "", time.Now().UTC())
	if task.ParentID != "" {
		h.integrateChild(ctx, run, task)
		return
	}
	_ = h.SM.Transition(ctx, task, domain.StatusReviewPending, reason)
}

// integrateChild applies a finished child's diff to its parent's integration
// branch and retires the child's workspace.
func (h *Handlers) integrateChild(ctx context.Context, run *domain.WorkflowRun, task *domain.Task) {
	latest, err := h.Runs.Get(ctx, run.ID)
	if err != nil {
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "get run for integration: "+err.Error())
		return
	}
	diff := ""
	if latest.LastCheckpoint != nil {
		diff = latest.LastCheckpoint.WorkspaceDiff
	}
	if h.Integrator != nil {
		if err := h.Integrator.Integrate(ctx, task, latest.BaseCommit, diff); err != nil {
			log.Printf("integration failed for child %s: %v", task.ID, err)
			// Integrator already transitioned child and parent to BLOCKED.
		} else {
			h.postThreadReplies(ctx, task, latest)
		}
	}
	workspace.Cleanup(ctx, &workspace.Workspace{
		WorktreePath:  latest.WorktreePath,
		ContainerName: latest.ContainerName,
	}, task.RepoPath)
}

// composeImplementer resolves the agent that produced run (e.g. a child's
// assigned agent), falling back to the stock implementer.
func (h *Handlers) composeImplementer(run *domain.WorkflowRun, task *domain.Task) (*agent.AgentDefinition, string, []agent.ToolSchema, error) {
	def, ok := h.Registry.GetAgent(agentNameOf(run))
	if !ok {
		def, ok = h.Registry.GetAgent("implementer")
	}
	if !ok {
		return nil, "", nil, fmt.Errorf("implementer agent not registered")
	}
	sysPrompt, tools, err := agent.Compose(def, h.Registry.GetSkill)
	if err != nil {
		return nil, "", nil, fmt.Errorf("compose %s: %w", def.Name, err)
	}
	return def, sysPrompt, agent.WithThreadReplies(tools, len(task.Addresses) > 0), nil
}

func bulletList(ss []string) string {
	var b strings.Builder
	for _, s := range ss {
		b.WriteString("- ")
		b.WriteString(s)
		b.WriteString("\n")
	}
	return b.String()
}

func agentNameOf(run *domain.WorkflowRun) string {
	s := run.Provenance.AgentDefinition
	if i := strings.Index(s, "@"); i > 0 {
		return s[:i]
	}
	return s
}

func buildReviewerPrompt(task *domain.Task, diff string, vr *domain.VerificationRun) string {
	var b strings.Builder
	b.WriteString("Task requirement:\n")
	b.WriteString(task.Requirement)
	b.WriteString("\n\nAcceptance criteria:\n")
	for i, a := range task.Acceptance {
		fmt.Fprintf(&b, "%d. %s\n", i+1, a)
	}
	b.WriteString("\nDiff to review:\n```\n")
	b.WriteString(diff)
	b.WriteString("\n```\n")
	if vr != nil {
		b.WriteString("\n")
		b.WriteString(verificationForReview(vr))
	}
	b.WriteString("\nReview the diff against every acceptance criterion. Give each defect's location as path:line on the new side of the diff (for example src/price.js:12), or just the path when no line fits. Call submit_review when done.")
	return b.String()
}

// reviewGateOutputTail bounds how much of each gate's output a reviewer sees.
const reviewGateOutputTail = 2000

// verificationForReview presents the platform's gate results to a reviewer.
// Reviewers have read-only tools and cannot run the suite, so without this
// they cannot tell whether a criterion like "npm test passes" was met, or
// which tests actually ran.
func verificationForReview(vr *domain.VerificationRun) string {
	var b strings.Builder
	b.WriteString("Verification (run by the platform on exactly this diff; you do not need to run it):\n")
	for _, g := range vr.Gates {
		switch {
		case g.Skipped:
			fmt.Fprintf(&b, "- %s: skipped (%s)\n", g.Name, g.SkipReason)
		case g.Passed:
			fmt.Fprintf(&b, "- %s: passed\n", g.Name)
		default:
			fmt.Fprintf(&b, "- %s: FAILED (exit %d)\n", g.Name, g.ExitCode)
		}
		if out := strings.TrimSpace(g.Output); out != "" && !g.Skipped {
			if len(out) > reviewGateOutputTail {
				out = "..." + out[len(out)-reviewGateOutputTail:]
			}
			fmt.Fprintf(&b, "  output:\n```\n%s\n```\n", out)
		}
	}
	return b.String()
}

func (h *Handlers) handleVerificationFailure(run *domain.WorkflowRun, task *domain.Task, vr *domain.VerificationRun, attempt int) {
	ctx := context.Background()

	if attempt >= h.MaxVerificationAttempts {
		reason := fmt.Sprintf("verification failed after %d attempts", attempt)
		_ = h.Runs.UpdateVerification(ctx, run.ID, attempt, false, domain.RunBlocked, reason, time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, reason)
		workspace.Cleanup(ctx, &workspace.Workspace{WorktreePath: run.WorktreePath, ContainerName: run.ContainerName}, task.RepoPath)
		return
	}

	_ = h.Runs.UpdateVerification(ctx, run.ID, attempt, false, domain.RunRunning, "", time.Now().UTC())
	_ = h.SM.Transition(ctx, task, domain.StatusRunning, "verification failed; retrying")
	h.reinvokeImplementer(run, task, "The previous attempt failed verification:\n\n"+h.Verifier.SummaryText(vr))
}

func (h *Handlers) handlePlannerDone(run *domain.WorkflowRun, task *domain.Task, body AgentCompleteBody) {
	ctx := context.Background()

	var sub struct {
		Steps []struct {
			Description string   `json:"description"`
			Files       []string `json:"files,omitempty"`
		} `json:"steps"`
		Risks []string `json:"risks,omitempty"`
	}
	_ = json.Unmarshal(body.Submission, &sub)

	plan := &domain.Plan{CreatedAt: time.Now().UTC(), Risks: sub.Risks}
	for _, s := range sub.Steps {
		plan.Steps = append(plan.Steps, domain.PlanStep{Description: s.Description, Files: s.Files})
	}
	_ = h.Runs.SavePlan(ctx, run.ID, plan, time.Now().UTC())

	if err := h.SM.Transition(ctx, task, domain.StatusRunning, "planner produced plan"); err != nil {
		log.Printf("transition to RUNNING failed: %v", err)
		return
	}

	h.invokeImplementer(run, task, buildImplementerTaskWithPlan(task, plan))
}

func (h *Handlers) invokeImplementer(run *domain.WorkflowRun, task *domain.Task, taskPrompt string) {
	ctx := context.Background()
	def, sysPrompt, tools, err := h.composeImplementer(run, task)
	if err != nil {
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, err.Error(), time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, err.Error())
		return
	}
	_, err = h.Adapter.StartRun(ctx, &adapter.StartRunRequest{
		RunID:          run.ID + "-impl",
		CallbackRunID:  run.ID,
		ContainerName:  run.ContainerName,
		WorktreePath:   run.WorktreePath,
		Role:           def.Name,
		Task:           taskPrompt,
		SystemPrompt:   sysPrompt,
		Tools:          tools,
		SubmissionTool: def.SubmissionTool,
		CallbackURL:    h.SelfURL,
		Provider:       run.Provenance.Provider,
		Model:          run.Provenance.Model,
		MaxSteps:       task.Budget.MaxSteps,
		MaxRetries:     task.Budget.MaxRetries,
		WallClockMS:    task.Budget.WallClockMS,
	})
	if err != nil {
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, err.Error(), time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "implementer invocation failed: "+err.Error())
	}
}

func (h *Handlers) reinvokeImplementer(run *domain.WorkflowRun, task *domain.Task, diagnostics string) {
	ctx := context.Background()
	latest, err := h.Runs.Get(ctx, run.ID)
	if err != nil {
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, "load run for retry: "+err.Error(), time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "load run for retry: "+err.Error())
		return
	}

	def, sysPrompt, tools, err := h.composeImplementer(latest, task)
	if err != nil {
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, err.Error(), time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, err.Error())
		return
	}

	// Retire the previous attempt's workspace; the checkpoint diff carries its work.
	workspace.Cleanup(ctx, &workspace.Workspace{
		WorktreePath: latest.WorktreePath, ContainerName: latest.ContainerName,
	}, task.RepoPath)

	// PROVISIONING until the new workspace exists: the stale-run watcher must
	// not mistake a slow dependency install for a dead agent.
	_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunProvisioning, "", time.Now().UTC())
	ws, err := workspace.ProvisionWithSetup(ctx, task.RepoPath, run.BaseCommit, h.WorkerImage, run.ID+"-ri", h.SetupTimeout)
	if err != nil {
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, "provision: "+err.Error(), time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "provision failed")
		return
	}
	if latest.LastCheckpoint != nil {
		_ = workspace.ApplyDiff(ctx, ws.WorktreePath, latest.LastCheckpoint.WorkspaceDiff)
	}
	_ = h.Runs.UpdateWorkspace(ctx, run.ID, ws.WorktreePath, ws.ContainerName, time.Now().UTC())
	_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunRunning, "", time.Now().UTC())

	var cp any
	if latest.LastCheckpoint != nil {
		cp = map[string]any{
			"messages":      latest.LastCheckpoint.Messages,
			"stepCount":     latest.LastCheckpoint.StepCount,
			"usage":         latest.LastCheckpoint.Usage,
			"status":        latest.LastCheckpoint.Status,
			"workspaceDiff": latest.LastCheckpoint.WorkspaceDiff,
		}
	}

	_, err = h.Adapter.StartRun(ctx, &adapter.StartRunRequest{
		RunID:          run.ID + "-impl-r" + fmt.Sprint(time.Now().UnixNano()),
		CallbackRunID:  run.ID,
		ContainerName:  ws.ContainerName,
		WorktreePath:   ws.WorktreePath,
		Role:           def.Name,
		Task:           task.Requirement + "\n\nAcceptance:\n" + bulletList(task.Acceptance) + h.subtaskBrief(ctx, task, false),
		SystemPrompt:   sysPrompt,
		Tools:          tools,
		SubmissionTool: def.SubmissionTool,
		CallbackURL:    h.SelfURL,
		Provider:       latest.Provenance.Provider,
		Model:          latest.Provenance.Model,
		MaxSteps:       task.Budget.MaxSteps,
		MaxRetries:     task.Budget.MaxRetries,
		WallClockMS:    task.Budget.WallClockMS,
		Checkpoint:     cp,
		Diagnostics:    diagnostics,
	})
	if err != nil {
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, "adapter retry: "+err.Error(), time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "adapter retry failed: "+err.Error())
	}
}

func buildImplementerTaskWithPlan(task *domain.Task, plan *domain.Plan) string {
	var b strings.Builder
	b.WriteString(task.Requirement)
	b.WriteString("\n\nAcceptance criteria (ALL must be satisfied):\n")
	for i, a := range task.Acceptance {
		fmt.Fprintf(&b, "%d. %s\n", i+1, a)
	}
	if len(plan.Steps) > 0 {
		b.WriteString("\nSuggested plan:\n")
		for i, s := range plan.Steps {
			fmt.Fprintf(&b, "%d. %s", i+1, s.Description)
			if len(s.Files) > 0 {
				fmt.Fprintf(&b, " [%s]", strings.Join(s.Files, ", "))
			}
			b.WriteString("\n")
		}
	}
	if len(plan.Risks) > 0 {
		b.WriteString("\nRisks to watch for:\n")
		for _, r := range plan.Risks {
			fmt.Fprintf(&b, "- %s\n", r)
		}
	}
	return b.String()
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
	vr.ID = fmt.Sprintf("vr-%s-r%d-%d", run.ID, run.ReviewRound, attempt)
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
		CallbackRunID: run.ID,
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
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "adapter retry failed: "+err.Error())
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
