package statemachine

import (
	"context"
	"log"
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
	at := time.Now().UTC()
	ok, err := s.Tasks.CompareAndSetStatus(ctx, task.ID, from, to, at)
	if err != nil {
		return err
	}
	if !ok {
		return ErrStaleTransition
	}
	task.Status = to
	task.UpdatedAt = at
	// The transition has happened; failing to record its event must not
	// make the caller believe it did not.
	if err := s.Events.Append(ctx, &domain.Event{
		TaskID: task.ID,
		Type:   "state.transition",
		From:   string(from),
		To:     string(to),
		Reason: reason,
		At:     at,
	}); err != nil {
		log.Printf("statemachine: %s %s -> %s happened but its event was not recorded: %v", task.ID, from, to, err)
	}
	return nil
}
