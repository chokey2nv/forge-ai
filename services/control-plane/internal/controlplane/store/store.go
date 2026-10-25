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
}

type RunRepo interface {
	Create(ctx context.Context, r *domain.WorkflowRun) error
	Get(ctx context.Context, id string) (*domain.WorkflowRun, error)
	UpdateStatus(ctx context.Context, id string, status domain.RunStatus, reason string, at time.Time) error
	SaveCheckpoint(ctx context.Context, id string, cp *domain.Checkpoint, at time.Time) error
	MarkStale(ctx context.Context, before time.Time) ([]*domain.WorkflowRun, error)
}

type EventRepo interface {
	Append(ctx context.Context, e *domain.Event) error
	ListByRun(ctx context.Context, runID string) ([]*domain.Event, error)
}
