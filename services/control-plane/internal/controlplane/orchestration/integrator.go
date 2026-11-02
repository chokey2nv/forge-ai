package orchestration

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/delivery"
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

// Integrate commits a reviewed subtask's work on its branch (based at base,
// the commit it started from) and merges that branch into the parent branch.
func (i *Integrator) Integrate(ctx context.Context, child *domain.Task, base, childDiff string) error {
	defer i.lock(child.ParentID)()

	parent, err := i.Tasks.Get(ctx, child.ParentID)
	if err != nil {
		return fmt.Errorf("get parent: %w", err)
	}
	fail := func(reason string) error {
		_ = i.SM.Transition(ctx, child, domain.StatusBlocked, "integration failed: "+reason)
		_ = i.SM.Transition(ctx, parent, domain.StatusBlocked, "child "+child.ID+" failed to integrate")
		return fmt.Errorf("%s", reason)
	}

	branch := child.Branch
	if branch == "" {
		branch = delivery.SubtaskBranchName(parent.IntegrationBranch, child.Title)
	}
	// Created at dispatch; also covers subtasks dispatched before branches.
	if err := delivery.EnsureBranchOrAhead(ctx, parent.RepoPath, branch, base); err != nil {
		return fail(err.Error())
	}
	tip, err := delivery.BranchTip(ctx, parent.RepoPath, branch)
	if err != nil {
		return fail(err.Error())
	}
	if tip == base {
		if _, err := workspace.CommitDiffOnBranch(ctx, parent.RepoPath, branch, childDiff, child.Title); err != nil {
			return fail(err.Error())
		}
	} // else: committed by an earlier attempt; merge what is there

	msg := fmt.Sprintf("integrate(%s): %s", child.ID, child.Title)
	if err := workspace.MergeIntoWorktree(ctx, parent.IntegrationWorktree, branch, msg+"\n\nMerge branch '"+branch+"'"); err != nil {
		return fail(err.Error())
	}

	if err := i.SM.Transition(ctx, child, domain.StatusIntegrated, "merged "+branch+" into "+parent.IntegrationBranch); err != nil {
		return err
	}
	_ = i.Events.Append(ctx, &domain.Event{
		TaskID: child.ID, Type: "child.integrated", Reason: msg, At: time.Now().UTC(),
	})
	return nil
}
