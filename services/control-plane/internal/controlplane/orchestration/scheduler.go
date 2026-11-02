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

	// MaxConcurrentRuns caps live workspaces (provisioning, agent, verification
	// and review all hold one). Ready children beyond it wait in CREATED.
	// Zero or less means no cap.
	MaxConcurrentRuns int

	// Provision creates a child's workspace from base (a SHA). Defaults to
	// workspace.ProvisionChild; tests substitute it.
	Provision func(ctx context.Context, repoPath, base, childBranch, image, runID string, setupTimeoutSeconds int) (*workspace.Workspace, error)
	// ResolveCommit pins a ref to a SHA. Defaults to workspace.ResolveCommit.
	ResolveCommit func(ctx context.Context, repoPath, ref string) (string, error)
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
	capacity, err := s.freeCapacity(ctx)
	if err != nil {
		log.Printf("scheduler: count live workspaces: %v", err)
		return
	}
	// Oldest parent first (ListByStatus sorts by creation), so one wide graph
	// cannot starve another of capacity.
	for _, parent := range parents {
		if parent.ParentID != "" {
			continue // only roots orchestrate
		}
		if s.blockParentIfChildBlocked(ctx, parent) {
			continue
		}
		capacity = s.dispatchReadyChildren(ctx, parent, capacity)
		s.checkParentIntegrationReady(ctx, parent)
	}
}

// liveStatuses are task states that hold a workspace: decomposing, a child
// being provisioned (DISPATCHED), an agent at work, verification, review, and
// a parent's combined verification.
var liveStatuses = []domain.TaskStatus{
	domain.StatusDecomposing, domain.StatusDispatched, domain.StatusRunning,
	domain.StatusVerifying, domain.StatusAgentReview, domain.StatusIntegrating,
}

// freeCapacity is how many more children may be dispatched now; -1 is no cap.
func (s *Scheduler) freeCapacity(ctx context.Context) (int, error) {
	if s.MaxConcurrentRuns <= 0 {
		return -1, nil
	}
	live := 0
	for _, st := range liveStatuses {
		tasks, err := s.Tasks.ListByStatus(ctx, st, 0)
		if err != nil {
			return 0, err
		}
		for _, t := range tasks {
			// A DISPATCHED parent is waiting on its children and holds nothing.
			if st == domain.StatusDispatched && t.ParentID == "" {
				continue
			}
			live++
		}
	}
	return max(0, s.MaxConcurrentRuns-live), nil
}

// dispatchReadyChildren claims ready children up to capacity (-1: no cap) and
// returns what capacity is left.
func (s *Scheduler) dispatchReadyChildren(ctx context.Context, parent *domain.Task, capacity int) int {
	children, err := s.Tasks.ListChildren(ctx, parent.ID)
	if err != nil {
		return capacity
	}

	statusByID := make(map[string]domain.TaskStatus, len(children))
	for _, c := range children {
		statusByID[c.ID] = c.Status
	}

	for _, child := range children {
		if capacity == 0 {
			break
		}
		if child.Status != domain.StatusCreated {
			continue
		}
		if !depsSatisfied(child, statusByID) {
			continue
		}
		if capacity > 0 {
			capacity--
		}
		if err := s.dispatchChild(ctx, parent, child); err != nil {
			log.Printf("scheduler: dispatch child %s: %v", child.ID, err)
			if errors.Is(err, statemachine.ErrStaleTransition) {
				continue // someone else moved it; not ours to block
			}
			_ = s.SM.Transition(ctx, child, domain.StatusBlocked, "dispatch failed: "+err.Error())
		}
	}
	return capacity
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

// dispatchChild claims a child and records its run, then provisions the
// workspace and starts the agent in the background: provisioning (clone,
// dependency install) can take far longer than a tick.
func (s *Scheduler) dispatchChild(ctx context.Context, parent, child *domain.Task) error {
	if parent.IntegrationBranch == "" {
		return fmt.Errorf("parent %s has no integration branch", parent.ID)
	}

	// Claim the child before any slow work, so no later tick dispatches it again.
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

	// Pin the starting point. Siblings integrate while this child works, so
	// its diff must later be applied against exactly this commit.
	base, err := s.resolveCommit(ctx, parent.RepoPath, parent.IntegrationBranch)
	if err != nil {
		return fmt.Errorf("resolve integration HEAD: %w", err)
	}

	now := time.Now().UTC()
	run := &domain.WorkflowRun{
		ID:         runID,
		TaskID:     child.ID,
		Status:     domain.RunProvisioning, // the stale-run watcher ignores it
		BaseCommit: base,
		Provenance: domain.Provenance{
			AgentDefinition: fmt.Sprintf("%s@v%d", def.Name, def.Version),
			Model:           s.DefaultModel,
			Provider:        s.DefaultProvider,
		},
		LastHeartbeat: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.Runs.Create(ctx, run); err != nil {
		return err
	}
	_ = s.Tasks.UpdateLatestRun(ctx, child.ID, runID, now)

	go s.provisionAndStart(ctx, parent, child, run, def, sysPrompt, tools)
	return nil
}

func (s *Scheduler) resolveCommit(ctx context.Context, repoPath, ref string) (string, error) {
	if s.ResolveCommit != nil {
		return s.ResolveCommit(ctx, repoPath, ref)
	}
	return workspace.ResolveCommit(ctx, repoPath, ref)
}

func (s *Scheduler) provision(ctx context.Context, repoPath, base, childBranch, runID string) (*workspace.Workspace, error) {
	if s.Provision != nil {
		return s.Provision(ctx, repoPath, base, childBranch, s.WorkerImage, runID, s.SetupTimeoutSeconds)
	}
	return workspace.ProvisionChild(ctx, repoPath, base, childBranch, s.WorkerImage, runID, s.SetupTimeoutSeconds)
}

func (s *Scheduler) provisionAndStart(ctx context.Context, parent, child *domain.Task, run *domain.WorkflowRun, def *agent.AgentDefinition, sysPrompt string, tools []agent.ToolSchema) {
	fail := func(reason string, ws *workspace.Workspace) {
		log.Printf("scheduler: child %s: %s", child.ID, reason)
		_ = s.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, reason, time.Now().UTC())
		if ws != nil {
			workspace.Cleanup(ctx, ws, parent.RepoPath)
		}
		_ = s.SM.Transition(ctx, child, domain.StatusBlocked, "dispatch failed: "+reason)
	}

	ws, err := s.provision(ctx, parent.RepoPath, run.BaseCommit, child.Branch, child.ID)
	if err != nil {
		fail("provision: "+err.Error(), nil)
		return
	}
	now := time.Now().UTC()
	_ = s.Runs.UpdateWorkspace(ctx, run.ID, ws.WorktreePath, ws.ContainerName, now)
	_ = s.Runs.UpdateStatus(ctx, run.ID, domain.RunRunning, "", now)

	// RUNNING before the adapter starts, so a fast completion callback never
	// finds the child still DISPATCHED.
	if err := s.SM.Transition(ctx, child, domain.StatusRunning, "dispatched to "+def.Name); err != nil {
		fail(err.Error(), ws)
		return
	}

	toolSchemas := make([]agent.ToolSchema, len(tools))
	copy(toolSchemas, tools)
	if _, err := s.Adapter.StartRun(ctx, &adapter.StartRunRequest{
		RunID:          run.ID,
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
	}); err != nil {
		fail("start agent: "+err.Error(), ws)
		return
	}

	_ = s.Events.Append(ctx, &domain.Event{
		TaskID: child.ID, RunID: run.ID,
		Type: "child.dispatched", Reason: def.Name,
		At: time.Now().UTC(),
	})
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
