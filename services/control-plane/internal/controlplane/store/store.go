package store

import (
	"context"

	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

type TaskRepo interface {
	Create(ctx context.Context, t *domain.Task) error
	Get(ctx context.Context, id string) (*domain.Task, error)
	UpdateStatus(ctx context.Context, id string, status domain.TaskStatus, at time.Time) error
	UpdateLatestRun(ctx context.Context, id, runID string, at time.Time) error
	List(ctx context.Context, status domain.TaskStatus, limit int) ([]*domain.Task, error)
}

type RunRepo interface {
	Create(ctx context.Context, r *domain.WorkflowRun) error
	Get(ctx context.Context, id string) (*domain.WorkflowRun, error)
	UpdateStatus(ctx context.Context, id string, status domain.RunStatus, reason string, at time.Time) error
	SaveCheckpoint(ctx context.Context, id string, cp *domain.Checkpoint, at time.Time) error
	MarkStale(ctx context.Context, before time.Time) ([]*domain.WorkflowRun, error)

	// Phase 4 additions:
	UpdateWorkspace(ctx context.Context, id, worktreePath, containerName string, at time.Time) error
	UpdateVerification(ctx context.Context, id string, attempt int, passed bool, status domain.RunStatus, reason string, at time.Time) error

	SavePlan(ctx context.Context, id string, plan *domain.Plan, at time.Time) error
	SaveReviewVerdict(ctx context.Context, id, verdict string, at time.Time) error

	ListByTask(ctx context.Context, taskID string) ([]*domain.WorkflowRun, error)
}

type EventRepo interface {
	Append(ctx context.Context, e *domain.Event) error
	ListByRun(ctx context.Context, runID string) ([]*domain.Event, error)
}
type VerificationRepo interface {
	Create(ctx context.Context, vr *domain.VerificationRun) error
	LatestForRun(ctx context.Context, runID string) (*domain.VerificationRun, error)
}

type ApprovalRepo interface {
	Create(ctx context.Context, a *domain.Approval) error
	ListByTask(ctx context.Context, taskID string) ([]*domain.Approval, error)
}

type EvalResultRepo interface {
	Create(ctx context.Context, e *domain.EvalResult) error
	List(ctx context.Context, model string, limit int) ([]*domain.EvalResult, error)
	Get(ctx context.Context, id string) (*domain.EvalResult, error)
}
