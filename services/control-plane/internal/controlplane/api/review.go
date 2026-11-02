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

// Review engine (Phase 9). Every reviewer the producing agent lists reviews
// the change, one after another: they share the implementer's container and
// its git index. Every reviewer runs each round, even after a rejection, so
// one round collects all the feedback. A round passes only when every
// verdict is "pass".

// startReviews begins the next review round for a run whose change passed
// verification. The task is already in AGENT_REVIEW.
func (h *Handlers) startReviews(ctx context.Context, run *domain.WorkflowRun, task *domain.Task) {
	reviewers := h.reviewersFor(run, task)
	if len(reviewers) == 0 {
		h.finishReviewRound(ctx, run, task, nil)
		return
	}
	h.startReviewer(ctx, run, task, &domain.ReviewPending{
		Round:     run.ReviewRound + 1,
		Index:     0,
		Reviewers: reviewers,
		StartedAt: time.Now().UTC(),
	})
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

func (h *Handlers) startReviewer(ctx context.Context, run *domain.WorkflowRun, task *domain.Task, p *domain.ReviewPending) {
	p.StartedAt = time.Now().UTC()
	name := p.Reviewer()
	// Recorded before the adapter starts, so even an instant callback is accepted.
	if err := h.Runs.SetReviewPending(ctx, run.ID, p, p.StartedAt); err != nil {
		log.Printf("review %s: record pending reviewer %s: %v", run.ID, name, err)
	}

	fail := func(reason string) {
		log.Printf("review %s: reviewer %s could not run: %s", run.ID, name, reason)
		h.recordReview(ctx, run, task, p, "", domain.VerdictError, "reviewer could not run: "+reason, nil)
		h.advanceReview(ctx, run, task, p)
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
		RunID:          fmt.Sprintf("%s-rev%d-%d", run.ID, p.Round, p.Index),
		CallbackRunID:  run.ID,
		ContainerName:  latest.ContainerName,
		WorktreePath:   worktree,
		Role:           def.Name,
		Task:           buildReviewerPrompt(task, diff, h.latestVerification(ctx, run.ID)),
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

// handleReviewerDone records a reviewer's verdict and moves the round on.
func (h *Handlers) handleReviewerDone(run *domain.WorkflowRun, task *domain.Task, body AgentCompleteBody) {
	ctx := context.Background()
	latest, err := h.Runs.Get(ctx, run.ID)
	if err != nil {
		log.Printf("review %s: load run: %v", run.ID, err)
		return
	}
	p := latest.ReviewPending
	if !acceptsReviewCallback(p, body.Role) {
		log.Printf("review %s: ignoring verdict from %q (waiting for %q)", run.ID, body.Role, p.Reviewer())
		return
	}
	// The watcher's reviewer timeout may be settling this slot right now;
	// exactly one of them claims it.
	if ok, err := h.Runs.ClaimReviewPending(ctx, run.ID, p.Round, p.Index); err != nil || !ok {
		log.Printf("review %s: verdict from %s arrived after its slot was settled", run.ID, body.Role)
		return
	}
	verdict, summary, defects := parseReview(body)
	h.recordReview(ctx, latest, task, p, agentDefinitionOf(h, body.Role), verdict, summary, defects)
	h.advanceReview(ctx, latest, task, p)
}

// HandleReviewTimeout settles a reviewer that never called back (its adapter
// died, or the model call hung past the wall clock) as an error verdict, so
// the round moves on instead of leaving the task in AGENT_REVIEW forever.
// Called by the watcher.
func (h *Handlers) HandleReviewTimeout(ctx context.Context, run *domain.WorkflowRun, waited time.Duration) {
	p := run.ReviewPending
	if p == nil {
		return
	}
	if ok, err := h.Runs.ClaimReviewPending(ctx, run.ID, p.Round, p.Index); err != nil || !ok {
		return // the verdict arrived meanwhile
	}
	task, err := h.Tasks.Get(ctx, run.TaskID)
	if err != nil {
		log.Printf("review timeout %s: load task: %v", run.ID, err)
		return
	}
	reviewerRunID := fmt.Sprintf("%s-rev%d-%d", run.ID, p.Round, p.Index)
	_ = h.Adapter.CancelRun(ctx, reviewerRunID) // best effort; a late verdict is ignored anyway
	log.Printf("review %s: reviewer %s timed out after %s", run.ID, p.Reviewer(), waited.Round(time.Second))
	h.recordReview(ctx, run, task, p, agentDefinitionOf(h, p.Reviewer()), domain.VerdictError,
		fmt.Sprintf("reviewer timed out after %s without a verdict", waited.Round(time.Second)), nil)
	h.advanceReview(ctx, run, task, p)
}

// acceptsReviewCallback rejects stale and duplicate callbacks: several
// reviewers report against one run ID, and only the pending one counts.
func acceptsReviewCallback(p *domain.ReviewPending, reviewer string) bool {
	return p != nil && reviewer != "" && p.Reviewer() == reviewer
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

func (h *Handlers) recordReview(ctx context.Context, run *domain.WorkflowRun, task *domain.Task, p *domain.ReviewPending, agentDef, verdict, summary string, defects []domain.Defect) {
	rv := &domain.Review{
		ID:              fmt.Sprintf("%s-r%d-%d", run.ID, p.Round, p.Index),
		TaskID:          task.ID,
		RunID:           run.ID,
		Round:           p.Round,
		Index:           p.Index,
		Reviewer:        p.Reviewer(),
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

// advanceReview starts the round's next reviewer, or decides the round.
func (h *Handlers) advanceReview(ctx context.Context, run *domain.WorkflowRun, task *domain.Task, p *domain.ReviewPending) {
	if p.Index+1 < len(p.Reviewers) {
		next := *p
		next.Index++
		h.startReviewer(ctx, run, task, &next)
		return
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
		_ = h.SM.Transition(ctx, task, domain.StatusReviewPending, reason)
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
