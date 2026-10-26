package statemachine

import (
	"errors"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

var ErrInvalidTransition = errors.New("invalid state transition")

var taskTransitions = map[domain.TaskStatus][]domain.TaskStatus{
	domain.StatusCreated: {
		domain.StatusDecomposing, // head engineer dispatch (Phase 8)
		domain.StatusPlanned,     // direct dispatch (backward compat)
		domain.StatusRejected,
	},
	domain.StatusDecomposing: {
		domain.StatusDispatched, // graph materialized
		domain.StatusBlocked,    // decomposition failed
		domain.StatusRejected,
	},
	domain.StatusPlanned: {
		domain.StatusDispatched,
		domain.StatusRunning,
		domain.StatusRejected,
	},
	domain.StatusDispatched: {
		domain.StatusRunning,     // child dispatched
		domain.StatusIntegrating, // all children done (parent)
		domain.StatusBlocked,     // child failed, or all blocked
		domain.StatusRejected,
	},
	domain.StatusRunning: {
		domain.StatusVerifying,
		domain.StatusBlocked,
		domain.StatusResumable,
		domain.StatusRejected,
	},
	domain.StatusVerifying: {
		domain.StatusAgentReview,
		domain.StatusReviewPending,
		domain.StatusRunning,
		domain.StatusBlocked,
	},
	domain.StatusAgentReview: {
		domain.StatusIntegrated,    // child: pass, back to parent (Phase 8)
		domain.StatusReviewPending, // root: pass, human approval
		domain.StatusRunning,       // reject, retry
		domain.StatusBlocked,
	},
	domain.StatusIntegrating: {
		domain.StatusVerifying, // combined verification
		domain.StatusBlocked,
	},
	domain.StatusReviewPending: {
		domain.StatusApproved,
		domain.StatusRejected,
	},
	domain.StatusApproved:  {domain.StatusMerged},
	domain.StatusBlocked:   {domain.StatusRunning},
	domain.StatusResumable: {domain.StatusRunning},
	// Integrated, Merged, Rejected terminal
}

func CanTransition(from, to domain.TaskStatus) bool {
	for _, allowed := range taskTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}
