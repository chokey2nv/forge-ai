package statemachine

import (
	"errors"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

var ErrInvalidTransition = errors.New("invalid state transition")

var taskTransitions = map[domain.TaskStatus][]domain.TaskStatus{
	domain.StatusCreated:       {domain.StatusPlanned, domain.StatusRejected},
	domain.StatusPlanned:       {domain.StatusDispatched, domain.StatusRejected},
	domain.StatusDispatched:    {domain.StatusRunning, domain.StatusBlocked, domain.StatusRejected},
	domain.StatusRunning:       {domain.StatusReviewPending, domain.StatusVerifying, domain.StatusBlocked, domain.StatusResumable, domain.StatusRejected},
	domain.StatusVerifying:     {domain.StatusReviewPending, domain.StatusRunning, domain.StatusBlocked}, // Phase 4
	domain.StatusReviewPending: {domain.StatusApproved, domain.StatusRejected},
	domain.StatusApproved:      {domain.StatusMerged},
	domain.StatusBlocked:       {domain.StatusRunning},
	domain.StatusResumable:     {domain.StatusRunning},
	// Merged, Rejected terminal
}

func CanTransition(from, to domain.TaskStatus) bool {
	for _, allowed := range taskTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}
