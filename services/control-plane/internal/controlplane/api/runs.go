package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github/chokey2nv/forge-ai/services/control-plane/internal/controlplane/agent"
	"log"
	"net/http"
	"strings"
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

	cp := &domain.Checkpoint{
		Messages: body.Messages, StepCount: body.StepCount, Usage: body.Usage,
		Status: body.Status, WorkspaceDiff: body.WorkspaceDiff, SavedAt: time.Now().UTC(),
	}
	_ = h.Runs.SaveCheckpoint(ctx, runID, cp, time.Now().UTC())

	switch body.Role {
	case "planner":
		h.handlePlannerDone(run, task, body)
	case "reviewer":
		h.handleReviewerDone(run, task, body)
	default:
		h.handleImplementerDone(run, task, body)
	}

	w.WriteHeader(http.StatusNoContent)
}
func (h *Handlers) handleImplementerDone(run *domain.WorkflowRun, task *domain.Task, body AgentCompleteBody) {
	ctx := context.Background()

	if body.Status != "complete" {
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, body.FinalMessage, time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, body.FinalMessage)
		return
	}

	_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunVerifying, "", time.Now().UTC())
	if err := h.SM.Transition(ctx, task, domain.StatusVerifying, "agent reported complete"); err != nil {
		log.Printf("transition to VERIFYING failed: %v", err)
		return
	}
	go h.runVerificationAndReview(run, task)
}
func (h *Handlers) runVerificationAndReview(run *domain.WorkflowRun, task *domain.Task) {
	ctx := context.Background()
	attempt := run.VerificationAttempt + 1

	vr := h.Verifier.Run(ctx, run.ContainerName, run.WorktreePath, task.DiffScope)
	vr.ID = fmt.Sprintf("vr-%s-%d", run.ID, attempt)
	vr.RunID, vr.TaskID, vr.Attempt = run.ID, task.ID, attempt
	_ = h.Verifications.Create(ctx, vr)

	if !vr.Passed {
		h.handleVerificationFailure(run, task, vr, attempt)
		return
	}

	// Verification passed → hand to reviewer (or straight to REVIEW_PENDING).
	if !h.ReviewerEnabled {
		_ = h.Runs.UpdateVerification(ctx, run.ID, attempt, true, domain.RunComplete, "", time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusReviewPending, "verification passed")
		return
	}

	if err := h.SM.Transition(ctx, task, domain.StatusAgentReview, "verification passed, reviewing"); err != nil {
		log.Printf("transition to AGENT_REVIEW failed: %v", err)
		return
	}
	_ = h.Runs.UpdateVerification(ctx, run.ID, attempt, true, domain.RunVerifying, "", time.Now().UTC())

	// Invoke reviewer via adapter.
	latest, _ := h.Runs.Get(ctx, run.ID)
	// diff := latest.LastCheckpoint.WorkspaceDiff

	// taskPrompt := buildReviewerPrompt(task, diff)

	// Look up the implementer's configured reviewer.
	impl, ok := h.Registry.GetAgent(agentNameOf(run))
	if !ok || len(impl.Reviewers) == 0 {
		// No reviewer configured: proceed directly to REVIEW_PENDING (human reviews).
		_ = h.Runs.UpdateVerification(ctx, run.ID, attempt, true, domain.RunComplete, "", time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusReviewPending, "verification passed; no agent reviewer configured")
		return
	}

	reviewerName := impl.Reviewers[0]
	reviewer, ok := h.Registry.GetAgent(reviewerName)
	if !ok {
		_ = h.SM.Transition(ctx, task, domain.StatusReviewPending, "verification passed; configured reviewer not registered")
		return
	}
	systemPrompt, tools, err := agent.Compose(reviewer, h.Registry.GetSkill)
	if err != nil {
		_ = h.SM.Transition(ctx, task, domain.StatusReviewPending, "verification passed; reviewer compose failed")
		return
	}

	_ = h.SM.Transition(ctx, task, domain.StatusAgentReview, "verification passed, invoking "+reviewerName)


	_, err = h.Adapter.StartRun(ctx, &adapter.StartRunRequest{
		RunID:         run.ID + "-rev" + fmt.Sprint(attempt),
		ContainerName: run.ContainerName,
		WorktreePath:  run.WorktreePath,
		Role:          reviewer.Name,
		Task:          buildReviewerPrompt(task, latest.LastCheckpoint.WorkspaceDiff),
		SystemPrompt:  systemPrompt,
		Tools:         tools,
		CallbackURL:   h.SelfURL,
		Provider:      latest.Provenance.Provider,
		Model:         latest.Provenance.Model,
		MaxSteps:      h.ReviewerMaxSteps,
		MaxRetries:    task.Budget.MaxRetries,
		WallClockMS:   h.ReviewerWallClockMS,
	})

	if err != nil {
		log.Printf("reviewer invocation failed: %v", err)
		// Reviewer failure does not block — proceed to human review.
		_ = h.SM.Transition(ctx, task, domain.StatusReviewPending, "reviewer failed to start")
	}
}

func agentNameOf(run *domain.WorkflowRun) string {
	s := run.Provenance.AgentDefinition
	if i := strings.Index(s, "@"); i > 0 {
		return s[:i]
	}
	return s
}

func buildReviewerPrompt(task *domain.Task, diff string) string {
	var b strings.Builder
	b.WriteString("Task requirement:\n")
	b.WriteString(task.Requirement)
	b.WriteString("\n\nAcceptance criteria:\n")
	for i, a := range task.Acceptance {
		fmt.Fprintf(&b, "%d. %s\n", i+1, a)
	}
	b.WriteString("\nDiff to review:\n```\n")
	b.WriteString(diff)
	b.WriteString("\n```\n\nReview the diff against every acceptance criterion. Call submit_review when done.")
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

func (h *Handlers) handleReviewerDone(run *domain.WorkflowRun, task *domain.Task, body AgentCompleteBody) {
	ctx := context.Background()

	var sub struct {
		Verdict string `json:"verdict"`
		Summary string `json:"summary"`
		Defects []struct {
			Severity    string `json:"severity"`
			Description string `json:"description"`
			Location    string `json:"location,omitempty"`
		} `json:"defects"`
	}
	_ = json.Unmarshal(body.Submission, &sub)

	verdict := strings.ToLower(strings.TrimSpace(sub.Verdict))
	if verdict != "pass" {
		verdict = "fail"
	}
	_ = h.Runs.SaveReviewVerdict(ctx, run.ID, verdict, time.Now().UTC())

	if verdict == "pass" {
		_ = h.SM.Transition(ctx, task, domain.StatusReviewPending, "reviewer passed: "+sub.Summary)
		workspace.Cleanup(ctx, &workspace.Workspace{WorktreePath: run.WorktreePath, ContainerName: run.ContainerName}, task.RepoPath)
		return
	}

	attempt := run.VerificationAttempt + 1
	if attempt >= h.MaxVerificationAttempts {
		reason := "reviewer failed change after " + fmt.Sprint(attempt) + " attempts: " + sub.Summary
		_ = h.Runs.UpdateVerification(ctx, run.ID, attempt, false, domain.RunBlocked, reason, time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, reason)
		workspace.Cleanup(ctx, &workspace.Workspace{WorktreePath: run.WorktreePath, ContainerName: run.ContainerName}, task.RepoPath)
		return
	}

	_ = h.Runs.UpdateVerification(ctx, run.ID, attempt, false, domain.RunRunning, "", time.Now().UTC())
	_ = h.SM.Transition(ctx, task, domain.StatusRunning, "reviewer rejected; retrying")

	var diag strings.Builder
	diag.WriteString("A code review rejected your change:\n\n")
	diag.WriteString(sub.Summary)
	diag.WriteString("\n\nDefects:\n")
	for _, d := range sub.Defects {
		fmt.Fprintf(&diag, "- [%s] %s", d.Severity, d.Description)
		if d.Location != "" {
			fmt.Fprintf(&diag, " (%s)", d.Location)
		}
		diag.WriteString("\n")
	}
	diag.WriteString("\nAddress every defect, then re-verify the acceptance criteria.")
	h.reinvokeImplementer(run, task, diag.String())
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
	_, err := h.Adapter.StartRun(ctx, &adapter.StartRunRequest{
		RunID:         run.ID + "-impl",
		ContainerName: run.ContainerName,
		WorktreePath:  run.WorktreePath,
		Role:          "implementer",
		Task:          taskPrompt,
		CallbackURL:   h.SelfURL,
		Provider:      run.Provenance.Provider,
		Model:         run.Provenance.Model,
		MaxSteps:      task.Budget.MaxSteps,
		MaxRetries:    task.Budget.MaxRetries,
		WallClockMS:   task.Budget.WallClockMS,
	})
	if err != nil {
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, err.Error(), time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "implementer invocation failed")
	}
}

func (h *Handlers) reinvokeImplementer(run *domain.WorkflowRun, task *domain.Task, diagnostics string) {
	ctx := context.Background()
	latest, _ := h.Runs.Get(ctx, run.ID)

	// Fresh workspace (container running the previous attempt is retired on transition)
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
		RunID:         run.ID + "-impl-r" + fmt.Sprint(time.Now().UnixNano()),
		ContainerName: ws.ContainerName,
		WorktreePath:  ws.WorktreePath,
		Role:          "implementer",
		Task:          task.Requirement,
		CallbackURL:   h.SelfURL,
		Provider:      latest.Provenance.Provider,
		Model:         latest.Provenance.Model,
		MaxSteps:      task.Budget.MaxSteps,
		MaxRetries:    task.Budget.MaxRetries,
		WallClockMS:   task.Budget.WallClockMS,
		Checkpoint:    cp,
		Diagnostics:   diagnostics,
	})
	if err != nil {
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, "adapter retry: "+err.Error(), time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "adapter retry failed")
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

// func (h *Handlers) RecordComplete(w http.ResponseWriter, r *http.Request) {
// 	ctx := r.Context()
// 	runID := r.PathValue("id")

// 	var body struct {
// 		Status        string           `json:"status"`
// 		FinalMessage  string           `json:"finalMessage"`
// 		StepCount     int              `json:"stepCount"`
// 		Usage         []domain.Usage   `json:"usage"`
// 		WorkspaceDiff string           `json:"workspaceDiff"`
// 		Messages      []domain.Message `json:"messages"`
// 	}
// 	_ = json.NewDecoder(r.Body).Decode(&body)

// 	run, err := h.Runs.Get(ctx, runID)
// 	if err != nil {
// 		http.NotFound(w, r)
// 		return
// 	}
// 	task, err := h.Tasks.Get(ctx, run.TaskID)
// 	if err != nil {
// 		http.NotFound(w, r)
// 		return
// 	}

// 	// Persist final checkpoint
// 	cp := &domain.Checkpoint{
// 		Messages: body.Messages, StepCount: body.StepCount,
// 		Usage: body.Usage, Status: body.Status,
// 		WorkspaceDiff: body.WorkspaceDiff, SavedAt: time.Now().UTC(),
// 	}
// 	_ = h.Runs.SaveCheckpoint(ctx, runID, cp, time.Now().UTC())

// 	// Agent-reported blocked/error: no verification, just block.
// 	if body.Status != "complete" {
// 		_ = h.Runs.UpdateStatus(ctx, runID, domain.RunBlocked, body.FinalMessage, time.Now().UTC())
// 		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, body.FinalMessage)
// 		_ = h.Events.Append(ctx, &domain.Event{RunID: runID, Type: "complete", Reason: body.FinalMessage, At: time.Now().UTC()})
// 		w.WriteHeader(http.StatusNoContent)
// 		return
// 	}

// 	// Agent claims complete → verify.
// 	_ = h.Runs.UpdateStatus(ctx, runID, domain.RunVerifying, "", time.Now().UTC())
// 	if err := h.SM.Transition(ctx, task, domain.StatusVerifying, "agent reported complete"); err != nil {
// 		http.Error(w, err.Error(), http.StatusConflict)
// 		return
// 	}
// 	_ = h.Events.Append(ctx, &domain.Event{RunID: runID, Type: "verification.start", At: time.Now().UTC()})

// 	// Respond immediately; verification runs asynchronously.
// 	w.WriteHeader(http.StatusNoContent)

// 	go h.runVerification(run, task)
// }

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
