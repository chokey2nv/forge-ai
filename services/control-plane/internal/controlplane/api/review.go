package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/adapter"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/agent"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

// Review engine. Every reviewer the producing agent lists reviews the
// change, all at the same time (Phase 10): reviewers only read, the adapter
// skips diff capture for them and their git commands take no index lock, so
// they can share the implementer's container. Every reviewer runs each
// round, even after a rejection, so one round collects all the feedback. A
// round passes only when every verdict is "pass".
//
// Each reviewer's verdict settles its slot in two atomic steps: claim the
// slot (so a verdict and the watcher's timeout cannot both settle it), save
// the verdict, then count it. Whoever records the last verdict decides the
// round, after every verdict is saved.

// startReviews begins the next review round for a run whose change passed
// verification. The task is already in AGENT_REVIEW.
func (h *Handlers) startReviews(ctx context.Context, run *domain.WorkflowRun, task *domain.Task) {
	reviewers := h.reviewersFor(run, task)
	if len(reviewers) == 0 {
		h.finishReviewRound(ctx, run, task, nil)
		return
	}
	p := &domain.ReviewPending{
		Round:     run.ReviewRound + 1,
		Reviewers: reviewers,
		StartedAt: time.Now().UTC(),
	}
	for i := range reviewers {
		p.Outstanding = append(p.Outstanding, i)
	}
	// Recorded before any reviewer starts, so even an instant callback is accepted.
	if err := h.Runs.SetReviewPending(ctx, run.ID, p, p.StartedAt); err != nil {
		log.Printf("review %s: record pending reviewers: %v", run.ID, err)
	}
	for i := range reviewers {
		h.startReviewer(ctx, run, task, p, i)
	}
}

// reviewersFor returns who reviews this run: the head engineer's reviewers
// for a parent's combined change, otherwise the producing agent's.
func (h *Handlers) reviewersFor(run *domain.WorkflowRun, task *domain.Task) []string {
	name := agentNameOf(run)
	if isParentReview(task) {
		name = "head-engineer"
	}
	if def, ok := h.Registry.GetAgent(name); ok {
		return def.Reviewers
	}
	return nil
}

// isParentReview reports whether task is a decomposed parent, whose review
// covers the combined diff on its integration branch.
func isParentReview(task *domain.Task) bool {
	return task.ParentID == "" && task.IntegrationBranch != ""
}

func reviewerRunID(run *domain.WorkflowRun, p *domain.ReviewPending, index int) string {
	return fmt.Sprintf("%s-rev%d-%d", run.ID, p.Round, index)
}

func (h *Handlers) startReviewer(ctx context.Context, run *domain.WorkflowRun, task *domain.Task, p *domain.ReviewPending, index int) {
	name := p.Reviewers[index]
	fail := func(reason string) {
		log.Printf("review %s: reviewer %s could not run: %s", run.ID, name, reason)
		h.settleReviewer(ctx, run, task, p, index, "", domain.VerdictError, "reviewer could not run: "+reason, nil)
	}

	def, ok := h.Registry.GetAgent(name)
	if !ok {
		fail("not registered")
		return
	}
	sysPrompt, tools, err := agent.Compose(def, h.Registry.GetSkill)
	if err != nil {
		fail("compose: " + err.Error())
		return
	}
	latest, err := h.Runs.Get(ctx, run.ID)
	if err != nil {
		fail("load run: " + err.Error())
		return
	}
	diff, worktree, err := reviewInput(latest, task)
	if err != nil {
		fail(err.Error())
		return
	}

	_, err = h.Adapter.StartRun(ctx, &adapter.StartRunRequest{
		RunID:          reviewerRunID(run, p, index),
		CallbackRunID:  run.ID,
		ContainerName:  latest.ContainerName,
		WorktreePath:   worktree,
		Role:           def.Name,
		Task:           buildReviewerPrompt(task, diff, h.latestVerification(ctx, run.ID)) + h.subtaskBrief(ctx, task, true),
		SystemPrompt:   sysPrompt,
		Tools:          tools,
		SubmissionTool: def.SubmissionTool,
		CallbackURL:    h.SelfURL,
		Provider:       latest.Provenance.Provider,
		Model:          latest.Provenance.Model,
		MaxSteps:       h.ReviewerMaxSteps,
		MaxRetries:     task.Budget.MaxRetries,
		WallClockMS:    h.ReviewerWallClockMS,
	})
	if err != nil {
		fail("start: " + err.Error())
	}
}

// reviewInput is the diff under review and the worktree the reviewer's
// container has mounted.
func reviewInput(run *domain.WorkflowRun, task *domain.Task) (diff, worktree string, err error) {
	if isParentReview(task) {
		diff, err = workspace.DiffFromBase(task.IntegrationWorktree, task.IntegrationBaseCommit)
		if err != nil {
			return "", "", fmt.Errorf("combined diff: %w", err)
		}
		return diff, task.IntegrationWorktree, nil
	}
	if run.LastCheckpoint != nil {
		diff = run.LastCheckpoint.WorkspaceDiff
	}
	return diff, run.WorktreePath, nil
}

// handleReviewerDone settles the reporting reviewer's slot with its verdict.
func (h *Handlers) handleReviewerDone(run *domain.WorkflowRun, task *domain.Task, body AgentCompleteBody) {
	ctx := context.Background()
	latest, err := h.Runs.Get(ctx, run.ID)
	if err != nil {
		log.Printf("review %s: load run: %v", run.ID, err)
		return
	}
	p := latest.ReviewPending
	index := pendingIndexFor(p, body.Role)
	if index < 0 {
		log.Printf("review %s: ignoring verdict from %q: not an outstanding reviewer", run.ID, body.Role)
		return
	}
	verdict, summary, defects := parseReview(body)
	h.settleReviewer(ctx, latest, task, p, index, agentDefinitionOf(h, body.Role), verdict, summary, defects)
}

// HandleReviewTimeout settles every reviewer of a round that has not called
// back (its adapter died, or the model call hung past the wall clock) as an
// error verdict, so the round is decided instead of leaving the task in
// AGENT_REVIEW forever. Called by the watcher.
func (h *Handlers) HandleReviewTimeout(ctx context.Context, run *domain.WorkflowRun, waited time.Duration) {
	p := run.ReviewPending
	if p == nil || len(p.Outstanding) == 0 {
		return
	}
	task, err := h.Tasks.Get(ctx, run.TaskID)
	if err != nil {
		log.Printf("review timeout %s: load task: %v", run.ID, err)
		return
	}
	for _, index := range p.Outstanding {
		_ = h.Adapter.CancelRun(ctx, reviewerRunID(run, p, index)) // best effort; a late verdict is ignored anyway
		log.Printf("review %s: reviewer %s timed out after %s", run.ID, p.Reviewers[index], waited.Round(time.Second))
		h.settleReviewer(ctx, run, task, p, index, agentDefinitionOf(h, p.Reviewers[index]), domain.VerdictError,
			fmt.Sprintf("reviewer timed out after %s without a verdict", waited.Round(time.Second)), nil)
	}
}

// pendingIndexFor is the slot a reporting reviewer settles: its first
// outstanding position in the round. -1 rejects stale and duplicate
// callbacks, since every reviewer reports against the same run ID.
func pendingIndexFor(p *domain.ReviewPending, reviewer string) int {
	if p == nil || reviewer == "" {
		return -1
	}
	for _, i := range p.Outstanding {
		if i >= 0 && i < len(p.Reviewers) && p.Reviewers[i] == reviewer {
			return i
		}
	}
	return -1
}

// settleReviewer records one reviewer's verdict for the round and, if it is
// the last, decides the round.
func (h *Handlers) settleReviewer(ctx context.Context, run *domain.WorkflowRun, task *domain.Task, p *domain.ReviewPending, index int, agentDef, verdict, summary string, defects []domain.Defect) {
	claimed, err := h.Runs.ClaimReviewSlot(ctx, run.ID, p.Round, index)
	if err != nil || !claimed {
		log.Printf("review %s: verdict for %s arrived after its slot was settled", run.ID, p.Reviewers[index])
		return
	}
	h.recordReview(ctx, run, task, p, index, agentDef, verdict, summary, defects)
	recorded, err := h.Runs.MarkReviewRecorded(ctx, run.ID, p.Round)
	if err != nil {
		log.Printf("review %s: count verdict: %v", run.ID, err)
		return
	}
	if recorded < len(p.Reviewers) {
		return // other reviewers are still at work
	}

	_ = h.Runs.SetReviewPending(ctx, run.ID, nil, time.Now().UTC())
	all, err := h.Reviews.ListByRun(ctx, run.ID)
	if err != nil {
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "load review verdicts: "+err.Error())
		return
	}
	var round []*domain.Review
	for _, rv := range all {
		if rv.Round == p.Round {
			round = append(round, rv)
		}
	}
	h.finishReviewRound(ctx, run, task, round)
}

// parseReview turns a reviewer's completion into a verdict. A reviewer that
// ends without calling submit_review, or with anything but "pass", rejects.
func parseReview(body AgentCompleteBody) (verdict, summary string, defects []domain.Defect) {
	if body.Status != "complete" || len(body.Submission) == 0 {
		return domain.VerdictError, "reviewer ended without a verdict: " + body.FinalMessage, nil
	}
	var sub struct {
		Verdict string          `json:"verdict"`
		Summary string          `json:"summary"`
		Defects []domain.Defect `json:"defects"`
	}
	if err := json.Unmarshal(body.Submission, &sub); err != nil {
		return domain.VerdictError, "unreadable verdict: " + err.Error(), nil
	}
	verdict = domain.VerdictFail
	if strings.EqualFold(strings.TrimSpace(sub.Verdict), domain.VerdictPass) {
		verdict = domain.VerdictPass
	}
	return verdict, sub.Summary, sub.Defects
}

func agentDefinitionOf(h *Handlers, name string) string {
	if def, ok := h.Registry.GetAgent(name); ok {
		return fmt.Sprintf("%s@v%d", def.Name, def.Version)
	}
	return name
}

func (h *Handlers) recordReview(ctx context.Context, run *domain.WorkflowRun, task *domain.Task, p *domain.ReviewPending, index int, agentDef, verdict, summary string, defects []domain.Defect) {
	rv := &domain.Review{
		ID:              fmt.Sprintf("%s-r%d-%d", run.ID, p.Round, index),
		TaskID:          task.ID,
		RunID:           run.ID,
		Round:           p.Round,
		Index:           index,
		Reviewer:        p.Reviewers[index],
		AgentDefinition: agentDef,
		Verdict:         verdict,
		Summary:         summary,
		Defects:         defects,
		StartedAt:       p.StartedAt,
		FinishedAt:      time.Now().UTC(),
	}
	if err := h.Reviews.Create(ctx, rv); err != nil {
		log.Printf("review %s: save verdict of %s: %v", run.ID, rv.Reviewer, err)
	}
	_ = h.Events.Append(ctx, &domain.Event{
		TaskID: task.ID, RunID: run.ID, Type: "review.verdict",
		Reason:  rv.Reviewer + ": " + verdict,
		Payload: map[string]any{"round": p.Round, "reviewer": rv.Reviewer, "verdict": verdict},
		At:      rv.FinishedAt,
	})
}

// roundPassed is true when every reviewer in the round passed. A round with
// no reviewers passes: nothing is configured to object.
func roundPassed(round []*domain.Review) bool {
	for _, rv := range round {
		if rv.Verdict != domain.VerdictPass {
			return false
		}
	}
	return true
}

// roundSummary is one line per reviewer, for logs and transition reasons.
func roundSummary(round []*domain.Review) string {
	parts := make([]string, 0, len(round))
	for _, rv := range round {
		parts = append(parts, rv.Reviewer+": "+rv.Verdict)
	}
	return strings.Join(parts, ", ")
}

// rejectionFeedback consolidates every rejecting reviewer's findings into
// one message for the implementer, grouped by reviewer.
func rejectionFeedback(round []*domain.Review) string {
	var b strings.Builder
	b.WriteString("Code review rejected your change. Address every defect below, then re-verify the acceptance criteria.\n")
	for _, rv := range round {
		if rv.Verdict == domain.VerdictPass {
			continue
		}
		fmt.Fprintf(&b, "\n## %s (%s)\n", rv.Reviewer, rv.Verdict)
		if rv.Summary != "" {
			b.WriteString(rv.Summary)
			b.WriteString("\n")
		}
		for _, d := range rv.Defects {
			fmt.Fprintf(&b, "- [%s] %s", d.Severity, d.Description)
			if d.Location != "" {
				fmt.Fprintf(&b, " (%s)", d.Location)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (h *Handlers) finishReviewRound(ctx context.Context, run *domain.WorkflowRun, task *domain.Task, round []*domain.Review) {
	now := time.Now().UTC()
	reviewWS := &workspace.Workspace{WorktreePath: run.WorktreePath, ContainerName: run.ContainerName}
	if isParentReview(task) {
		// Never remove the integration worktree; the run has none recorded anyway.
		reviewWS = &workspace.Workspace{ContainerName: run.ContainerName}
	}

	if roundPassed(round) {
		_ = h.Runs.SaveReviewVerdict(ctx, run.ID, domain.VerdictPass, now)
		if task.ParentID != "" {
			h.integrateChild(ctx, run, task)
			return
		}
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunComplete, "", now)
		reason := "review passed"
		if len(round) > 0 {
			reason += " (" + roundSummary(round) + ")"
		}
		if err := h.SM.Transition(ctx, task, domain.StatusReviewPending, reason); err == nil && isParentReview(task) {
			h.recordRevision(ctx, task, run.ID)
		}
		workspace.Cleanup(ctx, reviewWS, task.RepoPath)
		return
	}

	_ = h.Runs.SaveReviewVerdict(ctx, run.ID, domain.VerdictFail, now)
	summary := roundSummary(round)

	// A parent's combined change has no implementer to send back to, so a
	// rejected final review goes straight to a human (accept or reject).
	if isParentReview(task) {
		reason := "final review rejected: " + summary
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunEscalated, reason, now)
		_ = h.SM.Transition(ctx, task, domain.StatusEscalated, reason)
		_ = h.Events.Append(ctx, &domain.Event{TaskID: task.ID, RunID: run.ID, Type: "review.escalated", Reason: reason, At: now})
		workspace.Cleanup(ctx, reviewWS, task.RepoPath)
		return
	}

	rejections := run.ReviewRound + 1
	if rejections >= h.MaxReviewRounds {
		// Reviewer judgement, not an objective failure: a human decides
		// (POST /tasks/{id}/escalation). Accept integrates from the
		// checkpoint diff and retry provisions afresh, so the workspace goes.
		reason := fmt.Sprintf("review rejected the change in %d round(s): %s", rejections, summary)
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunEscalated, reason, now)
		_ = h.SM.Transition(ctx, task, domain.StatusEscalated, reason)
		_ = h.Events.Append(ctx, &domain.Event{TaskID: task.ID, RunID: run.ID, Type: "review.escalated", Reason: reason, At: now})
		workspace.Cleanup(ctx, reviewWS, task.RepoPath)
		return
	}

	// The reworked change gets a fresh set of verification attempts.
	_ = h.Runs.StartReviewRound(ctx, run.ID, rejections, now)
	_ = h.SM.Transition(ctx, task, domain.StatusRunning, "review rejected; retrying ("+summary+")")
	h.reinvokeImplementer(run, task, rejectionFeedback(round))
}

// ListRunReviews serves every reviewer verdict recorded for a run.
func (h *Handlers) ListRunReviews(w http.ResponseWriter, r *http.Request) {
	reviews, err := h.Reviews.ListByRun(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, reviews)
}

// latestVerification is the gate run the change under review passed; nil if
// none is recorded (the prompt then omits it).
func (h *Handlers) latestVerification(ctx context.Context, runID string) *domain.VerificationRun {
	vr, err := h.Verifications.LatestForRun(ctx, runID)
	if err != nil {
		return nil
	}
	return vr
}

// subtaskBrief is domain.SubtaskBrief with the task's siblings loaded.
func (h *Handlers) subtaskBrief(ctx context.Context, task *domain.Task, forReviewer bool) string {
	if task.ParentID == "" {
		return ""
	}
	siblings, _ := h.Tasks.ListChildren(ctx, task.ParentID)
	return domain.SubtaskBrief(task, siblings, forReviewer)
}
