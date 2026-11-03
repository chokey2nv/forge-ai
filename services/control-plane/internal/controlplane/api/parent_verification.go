package api

import (
	"context"
	"fmt"
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

	vr := h.Verifier.Run(ctx, ws.ContainerName, ws.WorktreePath, nil)
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

func parentProvenance(ctx context.Context, h *Handlers, parent *domain.Task) (provider, model string) {
	if run, err := h.Runs.Get(ctx, parent.LatestRunID); err == nil {
		return run.Provenance.Provider, run.Provenance.Model
	}
	return "", ""
}

// finalReviewRunID is the final review of the revision being prepared, the
// one after the last recorded. Revision 1 keeps the IDs used before
// revisions existed, so older tasks still read.
func finalReviewRunID(parent *domain.Task) string {
	return revisionID("run-"+parent.ID+"-final-review", len(parent.Revisions)+1)
}

func combinedVerificationID(parent *domain.Task) string {
	return revisionID("vr-"+parent.ID+"-combined", len(parent.Revisions)+1)
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
