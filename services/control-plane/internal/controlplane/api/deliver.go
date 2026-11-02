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
		moved, err := h.mergeTargetIn(ctx, task)
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
// Reviews are not repeated: the reviewed change itself is unchanged.
func (h *Handlers) mergeTargetIn(ctx context.Context, task *domain.Task) (moved bool, err error) {
	target, err := delivery.BranchTip(ctx, task.RepoPath, task.TargetBranch)
	if err != nil {
		return false, err
	}
	work, err := delivery.BranchTip(ctx, task.RepoPath, task.IntegrationBranch)
	if err != nil {
		return false, err
	}
	if delivery.IsAncestor(ctx, task.RepoPath, target, work) {
		return false, nil
	}

	wt, err := h.integrationWorktree(ctx, task)
	if err != nil {
		return true, err
	}
	msg := fmt.Sprintf("Merge %s into %s", task.TargetBranch, task.IntegrationBranch)
	if err := workspace.MergeIntoWorktree(ctx, wt, task.TargetBranch, msg); err != nil {
		return true, fmt.Errorf("%s moved since the work started and conflicts with it: %v", task.TargetBranch, err)
	}
	_ = h.Events.Append(ctx, &domain.Event{TaskID: task.ID, Type: "delivery.target_merged", Reason: msg, At: time.Now().UTC()})

	ws, err := workspace.ProvisionParentContainer(ctx, task.RepoPath, wt, h.WorkerImage, task.ID+"-delivery", int(h.SetupTimeout.Seconds()))
	if err != nil {
		return true, fmt.Errorf("re-verify after merging %s: %w", task.TargetBranch, err)
	}
	defer workspace.Cleanup(ctx, &workspace.Workspace{ContainerName: ws.ContainerName}, task.RepoPath)
	vr := h.Verifier.Run(ctx, ws.ContainerName, wt, nil)
	vr.ID = fmt.Sprintf("vr-%s-delivery-%d", task.ID, time.Now().UnixMilli())
	vr.RunID, vr.TaskID = finalReviewRunID(task), task.ID
	_ = h.Verifications.Create(ctx, vr)
	if !vr.Passed {
		return true, fmt.Errorf("verification failed after merging %s:\n%s", task.TargetBranch, h.Verifier.SummaryText(vr))
	}
	return true, nil
}

func (h *Handlers) openPullRequest(ctx context.Context, task *domain.Task, preview delivery.Preview, d *domain.Delivery, save func(), fail func(string)) {
	if err := delivery.Push(ctx, task.RepoPath, preview.Remote.Name, task.IntegrationBranch); err != nil {
		fail(err.Error())
		return
	}
	url, number, err := delivery.OpenPR(ctx, h.Delivery.GH, task.RepoPath, preview.Remote,
		task.TargetBranch, task.IntegrationBranch, firstLine(task.Requirement, 72), h.pullRequestBody(ctx, task))
	if err != nil {
		fail(err.Error())
		return
	}
	d.PRURL, d.PRNumber = url, number
	save()
	_ = h.SM.Transition(ctx, task, domain.StatusPROpen, "opened "+url)
	h.removeIntegrationWorktree(ctx, task)
}

// PollPullRequests follows open pull requests: merged on the forge → MERGED,
// closed unmerged → REJECTED. Called periodically.
func (h *Handlers) PollPullRequests(ctx context.Context) {
	tasks, err := h.Tasks.ListByStatus(ctx, domain.StatusPROpen, 0)
	if err != nil {
		log.Printf("pr watcher: %v", err)
		return
	}
	for _, task := range tasks {
		d := task.Delivery
		if d == nil || d.PRURL == "" {
			continue
		}
		state, merge, err := delivery.PRState(ctx, h.Delivery.GH, task.RepoPath, d.PRURL)
		if err != nil {
			log.Printf("pr watcher %s: %v", task.ID, err)
			continue
		}
		switch state {
		case "MERGED":
			d.MergeCommit, d.FinishedAt = merge, time.Now().UTC()
			_ = h.Tasks.UpdateDelivery(ctx, task.ID, d, time.Now().UTC())
			_ = h.SM.Transition(ctx, task, domain.StatusMerged, "pull request merged: "+d.PRURL)
		case "CLOSED":
			d.FinishedAt = time.Now().UTC()
			_ = h.Tasks.UpdateDelivery(ctx, task.ID, d, time.Now().UTC())
			_ = h.SM.Transition(ctx, task, domain.StatusRejected, "pull request closed without merging: "+d.PRURL)
		}
	}
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
	if reviews, err := h.Reviews.ListByRun(ctx, finalReviewRunID(task)); err == nil && len(reviews) > 0 {
		b.WriteString("\n## Final review\n\n")
		for _, rv := range reviews {
			fmt.Fprintf(&b, "- **%s**: %s\n", rv.Reviewer, rv.Verdict)
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
