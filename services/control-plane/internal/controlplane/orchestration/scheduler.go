package orchestration

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/adapter"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/agent"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/statemachine"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

type Scheduler struct {
	Tasks               store.TaskRepo
	Runs                store.RunRepo
	Events              store.EventRepo
	SM                  *statemachine.Service
	Registry            *agent.Registry
	Adapter             *adapter.Client
	WorkerImage         string
	SelfURL             string
	SetupTimeoutSeconds int
	DefaultProvider     string
	DefaultModel        string
}

func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	parents, err := s.Tasks.ListByStatus(ctx, domain.StatusDispatched, 0)
	if err != nil {
		log.Printf("scheduler: ListByStatus: %v", err)
		return
	}
	for _, parent := range parents {
		if parent.ParentID != "" {
			continue // only roots orchestrate
		}
		if s.blockParentIfChildBlocked(ctx, parent) {
			continue
		}
		s.dispatchReadyChildren(ctx, parent)
		s.checkParentIntegrationReady(ctx, parent)
	}
}

func (s *Scheduler) dispatchReadyChildren(ctx context.Context, parent *domain.Task) {
	children, err := s.Tasks.ListChildren(ctx, parent.ID)
	if err != nil {
		return
	}

	statusByID := make(map[string]domain.TaskStatus, len(children))
	for _, c := range children {
		statusByID[c.ID] = c.Status
	}

	for _, child := range children {
		if child.Status != domain.StatusCreated {
			continue
		}
		if !depsSatisfied(child, statusByID) {
			continue
		}
		if err := s.dispatchChild(ctx, parent, child); err != nil {
			log.Printf("scheduler: dispatch child %s: %v", child.ID, err)
			if errors.Is(err, statemachine.ErrStaleTransition) {
				continue // someone else moved it; not ours to block
			}
			_ = s.SM.Transition(ctx, child, domain.StatusBlocked, "dispatch failed: "+err.Error())
		}
	}
}

func depsSatisfied(child *domain.Task, statuses map[string]domain.TaskStatus) bool {
	for _, dep := range child.DependsOn {
		if statuses[dep] != domain.StatusIntegrated {
			return false
		}
	}
	return true
}

// blockParentIfChildBlocked moves the parent to BLOCKED once any child is
// BLOCKED or REJECTED; otherwise the parent would wait on it forever.
func (s *Scheduler) blockParentIfChildBlocked(ctx context.Context, parent *domain.Task) bool {
	children, err := s.Tasks.ListChildren(ctx, parent.ID)
	if err != nil {
		return false
	}
	for _, c := range children {
		if c.Status != domain.StatusBlocked && c.Status != domain.StatusRejected {
			continue
		}
		reason := fmt.Sprintf("child %s is %s", c.ID, c.Status)
		if err := s.SM.Transition(ctx, parent, domain.StatusBlocked, reason); err != nil {
			log.Printf("scheduler: parent %s → BLOCKED: %v", parent.ID, err)
			return false
		}
		_ = s.Events.Append(ctx, &domain.Event{
			TaskID: parent.ID, Type: "parent.blocked", Reason: reason, At: time.Now().UTC(),
		})
		return true
	}
	return false
}

func (s *Scheduler) dispatchChild(ctx context.Context, parent, child *domain.Task) error {
	if parent.IntegrationBranch == "" {
		return fmt.Errorf("parent %s has no integration branch", parent.ID)
	}

	// Claim the child before any slow work. Provisioning can outlast a tick,
	// and an unclaimed child would be provisioned again, destroying the
	// running agent's worktree and container.
	if err := s.SM.Transition(ctx, child, domain.StatusDispatched, "claimed by scheduler"); err != nil {
		return fmt.Errorf("claim: %w", err)
	}

	runID := "run-" + child.ID
	if _, err := s.Runs.Get(ctx, runID); err == nil {
		return fmt.Errorf("run %s already exists for child %s", runID, child.ID)
	}

	def, ok := s.Registry.GetAgent(child.AssignedAgent)
	if !ok {
		return fmt.Errorf("agent %q not registered", child.AssignedAgent)
	}
	sysPrompt, tools, err := agent.Compose(def, s.Registry.GetSkill)
	if err != nil {
		return fmt.Errorf("compose: %w", err)
	}

	ws, err := workspace.ProvisionChild(
		ctx, parent.RepoPath, parent.IntegrationBranch, child.Branch,
		s.WorkerImage, child.ID, s.SetupTimeoutSeconds,
	)
	if err != nil {
		return fmt.Errorf("provision: %w", err)
	}

	run := &domain.WorkflowRun{
		ID:            runID,
		TaskID:        child.ID,
		Status:        domain.RunRunning,
		WorktreePath:  ws.WorktreePath,
		ContainerName: ws.ContainerName,
		BaseCommit:    child.BaseCommit,
		Provenance: domain.Provenance{
			AgentDefinition: fmt.Sprintf("%s@v%d", def.Name, def.Version),
			Model:           s.DefaultModel,
			Provider:        s.DefaultProvider,
		},
		LastHeartbeat: time.Now().UTC(),
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := s.Runs.Create(ctx, run); err != nil {
		workspace.Cleanup(ctx, ws, parent.RepoPath)
		return err
	}
	_ = s.Tasks.UpdateLatestRun(ctx, child.ID, runID, time.Now().UTC())

	// RUNNING before the adapter starts, so a fast completion callback never
	// finds the child still DISPATCHED.
	if err := s.SM.Transition(ctx, child, domain.StatusRunning, "dispatched to "+def.Name); err != nil {
		_ = s.Runs.UpdateStatus(ctx, runID, domain.RunBlocked, err.Error(), time.Now().UTC())
		workspace.Cleanup(ctx, ws, parent.RepoPath)
		return err
	}

	toolSchemas := make([]agent.ToolSchema, len(tools))
	for i, t := range tools {
		toolSchemas[i] = agent.ToolSchema{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}
	}

	_, err = s.Adapter.StartRun(ctx, &adapter.StartRunRequest{
		RunID:          runID,
		ContainerName:  ws.ContainerName,
		WorktreePath:   ws.WorktreePath,
		Role:           def.Name,
		Task:           child.Requirement + "\n\nAcceptance:\n" + bullets(child.Acceptance),
		SystemPrompt:   sysPrompt,
		Tools:          toolSchemas,
		SubmissionTool: def.SubmissionTool,
		CallbackURL:    s.SelfURL,
		Provider:       run.Provenance.Provider,
		Model:          run.Provenance.Model,
		MaxSteps:       child.Budget.MaxSteps,
		MaxRetries:     child.Budget.MaxRetries,
		WallClockMS:    child.Budget.WallClockMS,
	})
	if err != nil {
		_ = s.Runs.UpdateStatus(ctx, runID, domain.RunBlocked, err.Error(), time.Now().UTC())
		workspace.Cleanup(ctx, ws, parent.RepoPath)
		return err
	}

	_ = s.Events.Append(ctx, &domain.Event{
		TaskID: child.ID, RunID: runID,
		Type: "child.dispatched", Reason: def.Name,
		At: time.Now().UTC(),
	})
	return nil
}

func (s *Scheduler) checkParentIntegrationReady(ctx context.Context, parent *domain.Task) {
	children, err := s.Tasks.ListChildren(ctx, parent.ID)
	if err != nil || len(children) == 0 {
		return
	}
	for _, c := range children {
		if c.Status != domain.StatusIntegrated {
			return
		}
	}
	if err := s.SM.Transition(ctx, parent, domain.StatusIntegrating, "all children integrated"); err != nil {
		log.Printf("scheduler: parent %s → INTEGRATING: %v", parent.ID, err)
		return
	}
	_ = s.Events.Append(ctx, &domain.Event{
		TaskID: parent.ID, Type: "parent.integrating", At: time.Now().UTC(),
	})

	// Fire the parent verification hook outside the ticker.
	if ParentVerificationHook != nil {
		go func() {
			if err := ParentVerificationHook(parent); err != nil {
				log.Printf("parent verification for %s: %v", parent.ID, err)
			}
		}()
	}
}

func bullets(ss []string) string {
	var b strings.Builder
	for _, s := range ss {
		b.WriteString("- ")
		b.WriteString(s)
		b.WriteString("\n")
	}
	return b.String()
}
