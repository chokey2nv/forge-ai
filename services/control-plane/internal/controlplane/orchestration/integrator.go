package orchestration

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/statemachine"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

type Integrator struct {
	Tasks  store.TaskRepo
	Events store.EventRepo
	SM     *statemachine.Service

	// One integration at a time per parent: siblings finishing together
	// would otherwise apply and commit in the same worktree concurrently.
	// In-process, so one control plane per database.
	locks sync.Map // parent ID -> *sync.Mutex
}

func (i *Integrator) lock(parentID string) func() {
	m, _ := i.locks.LoadOrStore(parentID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (i *Integrator) Integrate(ctx context.Context, child *domain.Task, childDiff string) error {
	defer i.lock(child.ParentID)()

	parent, err := i.Tasks.Get(ctx, child.ParentID)
	if err != nil {
		return fmt.Errorf("get parent: %w", err)
	}

	if err := workspace.ApplyDiff3Way(parent.IntegrationWorktree, childDiff); err != nil {
		_ = i.SM.Transition(ctx, child, domain.StatusBlocked, "integration conflict: "+err.Error())
		_ = i.SM.Transition(ctx, parent, domain.StatusBlocked, "child "+child.ID+" failed to integrate")
		return err
	}

	msg := fmt.Sprintf("integrate(%s): %s", child.ID, child.Title)
	if err := workspace.CommitAll(parent.IntegrationWorktree, msg); err != nil {
		workspace.ResetHard(parent.IntegrationWorktree)
		return fmt.Errorf("commit: %w", err)
	}

	if err := i.SM.Transition(ctx, child, domain.StatusIntegrated, "diff applied to parent branch"); err != nil {
		return err
	}
	_ = i.Events.Append(ctx, &domain.Event{
		TaskID: child.ID, Type: "child.integrated", Reason: msg, At: time.Now().UTC(),
	})
	return nil
}
