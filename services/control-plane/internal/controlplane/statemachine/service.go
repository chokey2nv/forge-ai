package statemachine

import (
	"context"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
)

type Service struct {
	Tasks  store.TaskRepo
	Events store.EventRepo
}

func (s *Service) Transition(ctx context.Context, task *domain.Task, to domain.TaskStatus, reason string) error {
	if !CanTransition(task.Status, to) {
		return ErrInvalidTransition
	}
	from := task.Status
	task.Status = to
	task.UpdatedAt = time.Now().UTC()
	if err := s.Tasks.UpdateStatus(ctx, task.ID, to, task.UpdatedAt); err != nil {
		return err
	}
	return s.Events.Append(ctx, &domain.Event{
		TaskID: task.ID,
		Type:   "state.transition",
		From:   string(from),
		To:     string(to),
		Reason: reason,
		At:     task.UpdatedAt,
	})
}
