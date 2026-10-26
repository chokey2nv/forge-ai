package orchestration

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
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
}

func (i *Integrator) Integrate(ctx context.Context, child *domain.Task, childDiff string) error {
	parent, err := i.Tasks.Get(ctx, child.ParentID)
	if err != nil {
		return fmt.Errorf("get parent: %w", err)
	}

	if err := applyDiff(parent.IntegrationWorktree, childDiff); err != nil {
		_ = i.SM.Transition(ctx, child, domain.StatusBlocked, "integration conflict: "+err.Error())
		_ = i.SM.Transition(ctx, parent, domain.StatusBlocked, "child "+child.ID+" failed to integrate")
		return err
	}

	msg := fmt.Sprintf("integrate(%s): %s", child.ID, child.Title)
	if err := workspace.CommitAll(parent.IntegrationWorktree, msg); err != nil {
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

func applyDiff(worktree, diff string) error {
	if diff == "" {
		return nil
	}
	cmd := exec.Command("git", "-C", worktree, "apply", "--whitespace=nowarn")
	cmd.Stdin = strings.NewReader(diff)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w", out, err)
	}
	return nil
}
