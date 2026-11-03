package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/delivery"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

// Rollback (Phase 11.4) undoes a delivery by reverting it, never by
// rewriting history: locally, a revert commit lands on the target the same
// way the merge did; with a remote, a revert branch is pushed and a revert
// pull request opened. A rollback that fails leaves the task MERGED with
// the reason, and can be asked for again.

// RollbackTask: POST /tasks/{id}/rollback {reviewer, rationale} on a MERGED task.
func (h *Handlers) RollbackTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Reviewer  string `json:"reviewer"`
		Rationale string `json:"rationale,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Reviewer == "" {
		http.Error(w, "reviewer is required", http.StatusBadRequest)
		return
	}
	task, err := h.Tasks.Get(ctx, r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if task.Status != domain.StatusMerged || task.Delivery == nil || task.Delivery.MergeCommit == "" {
		http.Error(w, "only a delivered (MERGED) task can be rolled back (status "+string(task.Status)+")", http.StatusConflict)
		return
	}
	if err := h.SM.Transition(ctx, task, domain.StatusRollingBack, "rollback by "+body.Reviewer+": "+body.Rationale); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	_ = h.Approvals.Create(ctx, &domain.Approval{
		TaskID: task.ID, RunID: task.LatestRunID, Action: "rollback",
		Reviewer: body.Reviewer, Rationale: body.Rationale, DecidedAt: time.Now().UTC(),
	})
	rb := &domain.Rollback{Reviewer: body.Reviewer, Rationale: body.Rationale, StartedAt: time.Now().UTC()}
	go h.rollback(context.WithoutCancel(ctx), task, rb)
	writeJSON(w, http.StatusAccepted, map[string]string{"taskId": task.ID, "status": string(domain.StatusRollingBack)})
}

func (h *Handlers) rollback(ctx context.Context, task *domain.Task, rb *domain.Rollback) {
	d := task.Delivery
	d.Rollback = rb
	save := func() { _ = h.Tasks.UpdateDelivery(ctx, task.ID, d, time.Now().UTC()) }
	fail := func(reason string) {
		log.Printf("rollback %s: %s", task.ID, reason)
		rb.Error, rb.FinishedAt = reason, time.Now().UTC()
		save()
		_ = h.SM.Transition(ctx, task, domain.StatusMerged, "rollback failed: "+reason)
	}
	save()
	msg := fmt.Sprintf("Revert \"Forge: %s\" (task %s)\n\nThis reverts %s.", firstLine(task.Requirement, 60), task.ID, d.MergeCommit)
	if rb.Rationale != "" {
		msg += "\n\n" + rb.Rationale
	}

	if d.Mode != delivery.ModePullRequest {
		revert, err := delivery.RevertLocally(ctx, task.RepoPath, d.TargetBranch, d.MergeCommit, msg)
		if errors.Is(err, delivery.ErrTargetDirty) {
			fail(fmt.Sprintf("%v; commit or stash those changes, then roll back again", err))
			return
		}
		if err != nil {
			fail(err.Error())
			return
		}
		rb.RevertCommit, rb.FinishedAt = revert, time.Now().UTC()
		save()
		_ = h.SM.Transition(ctx, task, domain.StatusRolledBack,
			fmt.Sprintf("reverted %s on %s (%s)", shortSHA(d.MergeCommit), d.TargetBranch, shortSHA(revert)))
		return
	}

	preview := h.Delivery.Preview(ctx, task)
	if preview.Remote == nil || preview.Remote.Name != d.Remote {
		fail(fmt.Sprintf("the remote %s it was delivered to is no longer configured", d.Remote))
		return
	}
	rb.Branch = h.revertBranchName(ctx, task)
	revert, err := delivery.RevertBranch(ctx, task.RepoPath, d.Remote, d.TargetBranch, d.MergeCommit, rb.Branch, msg)
	if err != nil {
		fail(err.Error())
		return
	}
	rb.RevertCommit = revert
	if err := delivery.Push(ctx, task.RepoPath, d.Remote, rb.Branch); err != nil {
		fail(err.Error())
		return
	}
	body := fmt.Sprintf("Reverts %s (Forge task `%s`), requested by %s.\n\n%s\n", d.PRURL, task.ID, rb.Reviewer, rb.Rationale)
	url, number, err := delivery.OpenPR(ctx, h.Delivery.GH, task.RepoPath, preview.Remote, d.TargetBranch, rb.Branch,
		"Revert: "+firstLine(task.Requirement, 64), body)
	if err != nil {
		fail(err.Error())
		return
	}
	rb.PRURL, rb.PRNumber = url, number
	save()
	_ = h.SM.Transition(ctx, task, domain.StatusRevertPROpen, "opened revert "+url)
}

// revertBranchName: <parent branch>-revert, numbered when an earlier
// rollback attempt left one behind.
func (h *Handlers) revertBranchName(ctx context.Context, task *domain.Task) string {
	name := task.IntegrationBranch + "-revert"
	for i := 2; ; i++ {
		if _, err := delivery.BranchTip(ctx, task.RepoPath, name); err != nil {
			return name
		}
		name = fmt.Sprintf("%s-revert-%d", task.IntegrationBranch, i)
	}
}
