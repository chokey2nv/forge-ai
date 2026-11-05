package api

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

func (h *Handlers) RunParentVerification(parent *domain.Task) error {
	ctx := context.Background()

	ws, err := workspace.ProvisionParentContainer(
		ctx, parent.RepoPath, parent.IntegrationWorktree,
		h.WorkerImage, parent.ID, int(h.SetupTimeout.Seconds()),
	)
	if err != nil {
		_ = h.SM.Transition(ctx, parent, domain.StatusBlocked, "parent container: "+err.Error())
		return err
	}

	if err := h.SM.Transition(ctx, parent, domain.StatusVerifying, "combined verification"); err != nil {
		workspace.Cleanup(ctx, &workspace.Workspace{ContainerName: ws.ContainerName}, parent.RepoPath)
		return err
	}

	var vr *domain.VerificationRun
	if child := h.verifiedSameCode(ctx, parent, ws.WorktreePath); child != "" {
		vr = h.Verifier.RunWithoutTests(ctx, ws.ContainerName, ws.WorktreePath, "the same code already passed its tests in subtask "+child)
	} else {
		vr = h.Verifier.Run(ctx, ws.ContainerName, ws.WorktreePath, nil)
	}
	vr.ID = combinedVerificationID(parent)
	vr.RunID = finalReviewRunID(parent) // so the final reviewers are shown these results
	vr.TaskID = parent.ID
	vr.Attempt = 1
	_ = h.Verifications.Create(ctx, vr)

	if !vr.Passed {
		_ = h.SM.Transition(ctx, parent, domain.StatusBlocked, "combined verification failed")
		workspace.Cleanup(ctx, &workspace.Workspace{ContainerName: ws.ContainerName}, parent.RepoPath)
		return nil
	}

	if err := h.SM.Transition(ctx, parent, domain.StatusAgentReview, "combined verification passed"); err != nil {
		return err
	}

	// The combined change is reviewed by the head engineer's reviewers. Their
	// callbacks need a run record to land on; WorktreePath stays empty so
	// cleanup only removes the container, never the integration worktree.
	provider, model := parentProvenance(ctx, h, parent)
	now := time.Now().UTC()
	run := &domain.WorkflowRun{
		ID: finalReviewRunID(parent), TaskID: parent.ID,
		Status:        domain.RunVerifying,
		ContainerName: ws.ContainerName,
		BaseCommit:    parent.IntegrationBaseCommit,
		Provenance: domain.Provenance{
			AgentDefinition: "final-review",
			Provider:        provider, Model: model,
		},
		LastHeartbeat: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := h.Runs.Create(ctx, run); err != nil {
		_ = h.SM.Transition(ctx, parent, domain.StatusBlocked, "create final review run: "+err.Error())
		workspace.Cleanup(ctx, &workspace.Workspace{ContainerName: ws.ContainerName}, parent.RepoPath)
		return err
	}
	h.startReviews(ctx, run, parent)
	return nil
}

// verifiedSameCode names a subtask whose passing verification was of exactly
// the code now on the parent branch, as it is with one subtask or the last of a
// sequence: its tests don't need another run.
func (h *Handlers) verifiedSameCode(ctx context.Context, parent *domain.Task, worktree string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", worktree, "status", "--porcelain").Output()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		return ""
	}
	tree, err := exec.CommandContext(ctx, "git", "-C", worktree, "rev-parse", "HEAD^{tree}").Output()
	if err != nil {
		return ""
	}
	children, err := h.Tasks.ListChildren(ctx, parent.ID)
	if err != nil {
		return ""
	}
	for _, c := range children {
		vr, err := h.Verifications.LatestForRun(ctx, "run-"+c.ID)
		if err != nil || vr == nil || !vr.Passed || vr.Tree == "" {
			continue
		}
		if vr.Tree == strings.TrimSpace(string(tree)) {
			return c.ID
		}
	}
	return ""
}

func parentProvenance(ctx context.Context, h *Handlers, parent *domain.Task) (provider, model string) {
	if run, err := h.Runs.Get(ctx, parent.LatestRunID); err == nil {
		return run.Provenance.Provider, run.Provenance.Model
	}
	return "", ""
}

// finalReviewRunID is the final review of the revision being prepared, the
// one after the last recorded. Revision 1 keeps the IDs used before
// revisions existed, so older tasks still read. A revision whose final
// review was escalated and sent back gets another attempt: -a2, -a3, ….
func finalReviewRunID(parent *domain.Task) string {
	return revisionID("run-"+parent.ID+"-final-review", len(parent.Revisions)+1) + attemptSuffix(parent)
}

func combinedVerificationID(parent *domain.Task) string {
	return revisionID("vr-"+parent.ID+"-combined", len(parent.Revisions)+1) + attemptSuffix(parent)
}

// attemptSuffix counts the escalations of the revision being prepared that
// a human sent back to the head engineer.
func attemptSuffix(parent *domain.Task) string {
	retries := 0
	for _, f := range parent.FollowUps {
		if f.Kind == domain.FollowUpEscalationRetry && f.Revision == len(parent.Revisions) {
			retries++
		}
	}
	if retries == 0 {
		return ""
	}
	return fmt.Sprintf("-a%d", retries+1)
}

func revisionID(base string, n int) string {
	if n <= 1 {
		return base
	}
	return fmt.Sprintf("%s-r%d", base, n)
}

// approvedReviewRunID is the final review of the latest revision put up for
// human review: the one an approval approves.
func approvedReviewRunID(task *domain.Task) string {
	if n := len(task.Revisions); n > 0 {
		return task.Revisions[n-1].FinalReviewRunID
	}
	return finalReviewRunID(task)
}
