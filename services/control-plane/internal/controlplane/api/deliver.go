package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/delivery"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

// Delivery (Phase 11.3). An approved task's parent branch reaches its target
// branch: with a remote, it is pushed and a pull request opened with gh (a
// human merges it on the forge); without one, it is merged locally. The
// mode comes from the same preview the console shows.

// canDeliver: tasks created before Phase 11 have no parent branch to deliver.
func canDeliver(task *domain.Task) bool {
	return task.TargetBranch != "" && task.IntegrationBranch != "" && task.ParentID == ""
}

// startDelivery moves an approved (or delivery-blocked) task to DELIVERING
// and delivers it in the background.
func (h *Handlers) startDelivery(ctx context.Context, task *domain.Task, reason string) error {
	if err := h.SM.Transition(ctx, task, domain.StatusDelivering, reason); err != nil {
		return err
	}
	go h.deliver(context.WithoutCancel(ctx), task)
	return nil
}

func (h *Handlers) deliver(ctx context.Context, task *domain.Task) {
	preview := h.Delivery.Preview(ctx, task)
	d := &domain.Delivery{
		Mode:         preview.Mode,
		TargetBranch: task.TargetBranch,
		Branch:       task.IntegrationBranch,
		StartedAt:    time.Now().UTC(),
	}
	if preview.Remote != nil {
		d.Remote = preview.Remote.Name
	}
	if prev := task.Delivery; prev != nil && prev.MergeCommit == "" && prev.PRURL == "" {
		// Resumed or retried: what the earlier attempt merged in may still need verifying.
		d.TargetMoved, d.VerifiedCommit = prev.TargetMoved, prev.VerifiedCommit
	}
	save := func() { _ = h.Tasks.UpdateDelivery(ctx, task.ID, d, time.Now().UTC()) }
	fail := func(reason string) {
		log.Printf("delivery %s: %s", task.ID, reason)
		d.Error, d.FinishedAt = reason, time.Now().UTC()
		save()
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "delivery failed: "+reason)
	}
	save()

	if preview.Mode == delivery.ModeUnavailable {
		fail(preview.Action)
		return
	}

	// The target may have moved since the work started (or during a local
	// merge: one retry). Merge it in and verify again before delivering.
	for attempt := 0; ; attempt++ {
		if preview.Mode == delivery.ModeLocalMerge && h.alreadyMerged(ctx, task, d) {
			return
		}
		moved, err := h.mergeTargetIn(ctx, task, d)
		if err != nil {
			fail(err.Error())
			return
		}
		d.TargetMoved = d.TargetMoved || moved
		save()

		if preview.Mode == delivery.ModePullRequest {
			h.openPullRequest(ctx, task, preview, d, save, fail)
			return
		}

		merge, err := delivery.MergeLocally(ctx, task.RepoPath, task.TargetBranch, task.IntegrationBranch,
			fmt.Sprintf("Forge: %s (task %s)", firstLine(task.Requirement, 72), task.ID))
		if errors.Is(err, delivery.ErrTargetMoved) && attempt == 0 {
			continue
		}
		if errors.Is(err, delivery.ErrTargetDirty) {
			fail(fmt.Sprintf("%v; commit or stash those changes, then retry delivery", err))
			return
		}
		if err != nil {
			fail(err.Error())
			return
		}
		d.MergeCommit, d.FinishedAt = merge, time.Now().UTC()
		save()
		_ = h.SM.Transition(ctx, task, domain.StatusMerged,
			fmt.Sprintf("merged %s into %s (%s)", task.IntegrationBranch, task.TargetBranch, shortSHA(merge)))
		h.removeIntegrationWorktree(ctx, task)
		return
	}
}

// ResumeDeliveries picks up deliveries a stopped control plane left
// DELIVERING (Phase 13.6). Every step of a delivery can be repeated: a
// half-done merge into the parent branch is undone and redone, a merge that
// landed is recognised, a pushed branch is pushed again and an open pull
// request is found rather than opened twice. A step that fails blocks the
// task with its reason, as in any delivery. Called once at startup, before
// the API serves: one control plane per database.
func (h *Handlers) ResumeDeliveries(ctx context.Context) int {
	tasks, err := h.Tasks.ListByStatus(ctx, domain.StatusDelivering, 0)
	if err != nil {
		log.Printf("resume deliveries: %v", err)
		return 0
	}
	for _, task := range tasks {
		reason := "the control plane restarted during delivery; resuming it"
		if task.IntegrationWorktree != "" && workspace.AbortUnfinishedMerge(task.IntegrationWorktree) {
			reason += " (undid a half-done merge of " + task.TargetBranch + ")"
		}
		log.Printf("delivery %s: %s", task.ID, reason)
		_ = h.Events.Append(ctx, &domain.Event{TaskID: task.ID, Type: "delivery.resumed", Reason: reason, At: time.Now().UTC()})
		go h.resumeDelivery(context.WithoutCancel(ctx), task)
	}
	return len(tasks)
}

func (h *Handlers) resumeDelivery(ctx context.Context, task *domain.Task) {
	// The pull request was opened and recorded; only the move to PR_OPEN was lost.
	if d := task.Delivery; d != nil && d.PRURL != "" && d.Error == "" {
		_ = h.SM.Transition(ctx, task, domain.StatusPROpen, "opened "+d.PRURL+" (recorded before the restart)")
		h.removeIntegrationWorktree(ctx, task)
		return
	}
	h.deliver(ctx, task)
}

// alreadyMerged: a retried delivery whose merge landed (the target holds the
// branch) completes without merging again.
func (h *Handlers) alreadyMerged(ctx context.Context, task *domain.Task, d *domain.Delivery) bool {
	target, err := delivery.BranchTip(ctx, task.RepoPath, task.TargetBranch)
	if err != nil || task.Delivery == nil || !delivery.IsAncestor(ctx, task.RepoPath, task.IntegrationBranch, target) {
		return false
	}
	d.MergeCommit = task.Delivery.MergeCommit
	if d.MergeCommit == "" {
		d.MergeCommit = target
	}
	d.FinishedAt = time.Now().UTC()
	_ = h.Tasks.UpdateDelivery(ctx, task.ID, d, time.Now().UTC())
	_ = h.SM.Transition(ctx, task, domain.StatusMerged, "already merged into "+task.TargetBranch)
	h.removeIntegrationWorktree(ctx, task)
	return true
}

// mergeTargetIn brings the target's new commits into the parent branch when
// the target advanced, then re-runs the verification gates on the result.
// Reviews are not repeated: the reviewed change itself is unchanged. A merge
// an earlier attempt made whose verification did not pass (it failed, or
// the control plane stopped during it) is verified now. d.VerifiedCommit
// records the tip that passed.
func (h *Handlers) mergeTargetIn(ctx context.Context, task *domain.Task, d *domain.Delivery) (moved bool, err error) {
	target, err := delivery.BranchTip(ctx, task.RepoPath, task.TargetBranch)
	if err != nil {
		return false, err
	}
	work, err := delivery.BranchTip(ctx, task.RepoPath, task.IntegrationBranch)
	if err != nil {
		return false, err
	}
	merged := delivery.IsAncestor(ctx, task.RepoPath, target, work)
	if merged && (!d.TargetMoved || d.VerifiedCommit == work) {
		return false, nil
	}

	wt, err := h.integrationWorktree(ctx, task)
	if err != nil {
		return true, err
	}
	if !merged {
		msg := fmt.Sprintf("Merge %s into %s", task.TargetBranch, task.IntegrationBranch)
		if err := workspace.MergeIntoWorktree(ctx, wt, task.TargetBranch, msg); err != nil {
			return true, fmt.Errorf("%s moved since the work started and conflicts with it: %v", task.TargetBranch, err)
		}
		_ = h.Events.Append(ctx, &domain.Event{TaskID: task.ID, Type: "delivery.target_merged", Reason: msg, At: time.Now().UTC()})
		// Recorded before verifying, so an interrupted verification is redone.
		d.TargetMoved = true
		_ = h.Tasks.UpdateDelivery(ctx, task.ID, d, time.Now().UTC())
		if work, err = delivery.BranchTip(ctx, task.RepoPath, task.IntegrationBranch); err != nil {
			return true, err
		}
	}

	ws, err := workspace.ProvisionParentContainer(ctx, task.RepoPath, wt, h.WorkerImage, task.ID+"-delivery", int(h.SetupTimeout.Seconds()))
	if err != nil {
		return true, fmt.Errorf("re-verify after merging %s: %w", task.TargetBranch, err)
	}
	defer workspace.Cleanup(ctx, &workspace.Workspace{ContainerName: ws.ContainerName}, task.RepoPath)
	vr := h.Verifier.Run(ctx, ws.ContainerName, wt, nil)
	vr.ID = fmt.Sprintf("vr-%s-delivery-%d", task.ID, time.Now().UnixMilli())
	vr.RunID, vr.TaskID = approvedReviewRunID(task), task.ID
	_ = h.Verifications.Create(ctx, vr)
	if !vr.Passed {
		return true, fmt.Errorf("verification failed after merging %s:\n%s", task.TargetBranch, h.Verifier.SummaryText(vr))
	}
	d.VerifiedCommit = work
	return true, nil
}

func (h *Handlers) openPullRequest(ctx context.Context, task *domain.Task, preview delivery.Preview, d *domain.Delivery, save func(), fail func(string)) {
	if err := delivery.Push(ctx, task.RepoPath, preview.Remote.Name, task.IntegrationBranch); err != nil {
		fail(err.Error())
		return
	}
	// One may already be open: gh opened it, then the control plane stopped
	// before recording it.
	url, number, open := delivery.FindOpenPR(ctx, h.Delivery.GH, task.RepoPath, preview.Remote, task.TargetBranch, task.IntegrationBranch)
	if !open {
		var err error
		url, number, err = delivery.OpenPR(ctx, h.Delivery.GH, task.RepoPath, preview.Remote,
			task.TargetBranch, task.IntegrationBranch, firstLine(task.Requirement, 72), h.pullRequestBody(ctx, task))
		if err != nil {
			fail(err.Error())
			return
		}
	}
	d.PRURL, d.PRNumber = url, number
	save()
	_ = h.SM.Transition(ctx, task, domain.StatusPROpen, "opened "+url)
	h.removeIntegrationWorktree(ctx, task)
}

// PollPullRequests follows open pull requests. A delivery PR merged on the
// forge -> MERGED, closed -> REJECTED; a revert PR merged -> ROLLED_BACK,
// closed -> MERGED (the delivery stands). Called periodically.
func (h *Handlers) PollPullRequests(ctx context.Context) {
	for _, status := range []domain.TaskStatus{domain.StatusPROpen, domain.StatusRevertPROpen} {
		tasks, err := h.Tasks.ListByStatus(ctx, status, 0)
		if err != nil {
			log.Printf("pr watcher: %v", err)
			return
		}
		for _, task := range tasks {
			h.pollPullRequest(ctx, task)
		}
	}
}

func (h *Handlers) pollPullRequest(ctx context.Context, task *domain.Task) {
	d := task.Delivery
	if d == nil {
		return
	}
	url := d.PRURL
	if task.Status == domain.StatusRevertPROpen {
		if d.Rollback == nil {
			return
		}
		url = d.Rollback.PRURL
	}
	if url == "" {
		return
	}
	state, merge, err := delivery.PRState(ctx, h.Delivery.GH, task.RepoPath, url)
	if err != nil {
		log.Printf("pr watcher %s: %v", task.ID, err)
		return
	}
	if state != "MERGED" && state != "CLOSED" {
		return
	}
	now := time.Now().UTC()
	var to domain.TaskStatus
	var reason string
	switch {
	case task.Status == domain.StatusPROpen && state == "MERGED":
		d.MergeCommit, d.FinishedAt = merge, now
		to, reason = domain.StatusMerged, "pull request merged: "+url
	case task.Status == domain.StatusPROpen:
		d.FinishedAt = now
		to, reason = domain.StatusRejected, "pull request closed without merging: "+url
	case state == "MERGED":
		d.Rollback.RevertCommit, d.Rollback.FinishedAt = merge, now
		to, reason = domain.StatusRolledBack, "revert pull request merged: "+url
	default:
		d.Rollback.Error, d.Rollback.FinishedAt = "revert pull request closed without merging", now
		to, reason = domain.StatusMerged, "revert pull request closed without merging: "+url
	}
	_ = h.Tasks.UpdateDelivery(ctx, task.ID, d, now)
	_ = h.SM.Transition(ctx, task, to, reason)
}

// RetryDelivery re-runs a delivery that was blocked (dirty checkout,
// conflict, push or gh error) once the cause is fixed.
func (h *Handlers) RetryDelivery(w http.ResponseWriter, r *http.Request) {
	task, err := h.Tasks.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if task.Status != domain.StatusBlocked || task.Delivery == nil {
		http.Error(w, "only a task whose delivery was blocked can be retried (status "+string(task.Status)+")", http.StatusConflict)
		return
	}
	if err := h.startDelivery(r.Context(), task, "delivery retried"); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"taskId": task.ID, "status": string(domain.StatusDelivering)})
}

func (h *Handlers) integrationWorktree(ctx context.Context, task *domain.Task) (string, error) {
	if task.IntegrationWorktree != "" {
		if _, err := os.Stat(task.IntegrationWorktree); err == nil {
			return task.IntegrationWorktree, nil
		}
	}
	wt, err := workspace.AddWorktreeForBranch(task.RepoPath, task.IntegrationBranch)
	if err != nil {
		return "", err
	}
	task.IntegrationWorktree = wt
	_ = h.Tasks.UpdateIntegration(ctx, task.ID, wt, task.IntegrationBranch, task.IntegrationBaseCommit, time.Now().UTC())
	return wt, nil
}

// removeIntegrationWorktree drops the parent's worktree once its branch is
// delivered; the branch itself stays.
func (h *Handlers) removeIntegrationWorktree(ctx context.Context, task *domain.Task) {
	if task.IntegrationWorktree == "" {
		return
	}
	workspace.RemoveIntegrationWorktree(task.RepoPath, task.IntegrationWorktree)
	_ = h.Tasks.UpdateIntegration(ctx, task.ID, "", task.IntegrationBranch, task.IntegrationBaseCommit, time.Now().UTC())
}

// pullRequestBody describes the change for the humans reviewing the PR.
func (h *Handlers) pullRequestBody(ctx context.Context, task *domain.Task) string {
	var b strings.Builder
	b.WriteString(task.Requirement)
	b.WriteString("\n\n## Acceptance criteria\n\n")
	for _, a := range task.Acceptance {
		fmt.Fprintf(&b, "- %s\n", a)
	}
	if children, err := h.Tasks.ListChildren(ctx, task.ID); err == nil && len(children) > 0 {
		b.WriteString("\n## Subtasks\n\n")
		for _, c := range children {
			fmt.Fprintf(&b, "- %s (`%s`, %s)\n", c.Title, c.AssignedAgent, c.Branch)
		}
	}
	if reviews, err := h.Reviews.ListByRun(ctx, approvedReviewRunID(task)); err == nil && len(reviews) > 0 {
		b.WriteString("\n## Final review\n\n")
		for _, rv := range reviews {
			fmt.Fprintf(&b, "- **%s**: %s\n", rv.Reviewer, rv.Verdict)
		}
	}
	if open := h.unresolvedThreads(ctx, task.ID); len(open) > 0 {
		b.WriteString("\n## Unresolved review threads\n\nApproved with these threads still open in Forge:\n\n")
		for _, t := range open {
			fmt.Fprintf(&b, "- `%s` (%s, %s): %s\n", t.Where, t.Status, t.Author, firstLine(t.Comment, 120))
		}
	}
	fmt.Fprintf(&b, "\n---\nOpened by Forge for task `%s` after human approval.\n", task.ID)
	return b.String()
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(strings.SplitN(strings.TrimSpace(s), "\n", 2)[0])
	if len(s) > max {
		s = strings.TrimSpace(s[:max-1]) + "…"
	}
	return s
}
