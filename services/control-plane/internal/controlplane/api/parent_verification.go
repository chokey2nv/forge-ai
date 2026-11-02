package api

import (
	"context"
	"fmt"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/adapter"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/agent"
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
	vr.ID = fmt.Sprintf("vr-%s-combined", parent.ID)
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

	// Determine reviewer: use head-engineer's reviewers[0].
	he, ok := h.Registry.GetAgent("head-engineer")
	if !ok || len(he.Reviewers) == 0 {
		_ = h.SM.Transition(ctx, parent, domain.StatusReviewPending, "no reviewer configured")
		return nil
	}
	reviewerName := he.Reviewers[0]
	reviewer, ok := h.Registry.GetAgent(reviewerName)
	if !ok {
		_ = h.SM.Transition(ctx, parent, domain.StatusReviewPending, "reviewer not registered")
		return nil
	}
	sysPrompt, tools, err := agent.Compose(reviewer, h.Registry.GetSkill)
	if err != nil {
		_ = h.SM.Transition(ctx, parent, domain.StatusReviewPending, "compose reviewer: "+err.Error())
		return nil
	}

	diff, err := workspace.DiffFromBase(parent.IntegrationWorktree, parent.IntegrationBaseCommit)
	if err != nil {
		_ = h.SM.Transition(ctx, parent, domain.StatusBlocked, "combined diff: "+err.Error())
		workspace.Cleanup(ctx, &workspace.Workspace{ContainerName: ws.ContainerName}, parent.RepoPath)
		return err
	}

	toolSchemas := make([]agent.ToolSchema, len(tools))
	for i, t := range tools {
		toolSchemas[i] = agent.ToolSchema{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}
	}

	// The reviewer's completion callback needs a run record to land on.
	// WorktreePath is left empty so review cleanup only removes the container,
	// never the parent's integration worktree.
	provider, model := parentProvenance(ctx, h, parent)
	runID := "run-" + parent.ID + "-final-review"
	now := time.Now().UTC()
	if err := h.Runs.Create(ctx, &domain.WorkflowRun{
		ID: runID, TaskID: parent.ID,
		Status:        domain.RunVerifying,
		ContainerName: ws.ContainerName,
		BaseCommit:    parent.IntegrationBaseCommit,
		Provenance: domain.Provenance{
			AgentDefinition: fmt.Sprintf("%s@v%d", reviewer.Name, reviewer.Version),
			Provider:        provider, Model: model,
		},
		LastHeartbeat: now, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		_ = h.SM.Transition(ctx, parent, domain.StatusReviewPending, "create review run: "+err.Error())
		return nil
	}

	_, err = h.Adapter.StartRun(ctx, &adapter.StartRunRequest{
		RunID:          runID,
		CallbackRunID:  runID,
		ContainerName:  ws.ContainerName,
		WorktreePath:   ws.WorktreePath,
		Role:           reviewer.Name,
		Task:           buildReviewerPrompt(parent, diff),
		SystemPrompt:   sysPrompt,
		Tools:          toolSchemas,
		SubmissionTool: reviewer.SubmissionTool,
		CallbackURL:    h.SelfURL,
		Provider:       provider,
		Model:          model,
		MaxSteps:       h.ReviewerMaxSteps,
		MaxRetries:     parent.Budget.MaxRetries,
		WallClockMS:    h.ReviewerWallClockMS,
	})
	if err != nil {
		_ = h.SM.Transition(ctx, parent, domain.StatusReviewPending, "reviewer invocation failed: "+err.Error())
	}
	return nil
}

func parentProvenanceAgent(parent *domain.Task) string {
	return "head-engineer" // Phase 8: parent review is delegated to head-engineer's reviewers
}

// parentProvenance reuses the provider/model the head-engineer ran with.
func parentProvenance(ctx context.Context, h *Handlers, parent *domain.Task) (provider, model string) {
	if run, err := h.Runs.Get(ctx, parent.LatestRunID); err == nil {
		return run.Provenance.Provider, run.Provenance.Model
	}
	return "", ""
}
