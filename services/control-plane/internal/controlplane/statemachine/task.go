package statemachine

import (
	"errors"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

var (
	ErrInvalidTransition = errors.New("invalid state transition")
	// ErrStaleTransition means the task's stored status no longer matched the
	// caller's view, so another writer already moved it.
	ErrStaleTransition = errors.New("task status changed concurrently")
)

var taskTransitions = map[domain.TaskStatus][]domain.TaskStatus{
	domain.StatusCreated: {
		domain.StatusDecomposing, // head engineer dispatch (Phase 8)
		domain.StatusPlanned,     // direct dispatch (backward compat)
		domain.StatusDispatched,  // child claimed by the scheduler (Phase 8)
		domain.StatusBlocked,     // dispatch failed before the child was claimed
		domain.StatusRejected,
	},
	domain.StatusDecomposing: {
		domain.StatusDispatched, // graph materialized
		domain.StatusBlocked,    // decomposition failed
		domain.StatusRejected,
		domain.StatusReviewPending, // follow-up graph refused: back to the human's decision (Phase 12)
		domain.StatusEscalated,     // same, for a retried escalation
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
		domain.StatusEscalated, // someone stopped the checks to decide
		domain.StatusAgentReview,
		domain.StatusReviewPending,
		domain.StatusRunning,
		domain.StatusBlocked,
	},
	domain.StatusAgentReview: {
		domain.StatusIntegrated,    // child: pass, back to parent (Phase 8)
		domain.StatusReviewPending, // root: pass, human approval
		domain.StatusRunning,       // reject, retry
		domain.StatusEscalated,     // review rounds exhausted (Phase 9)
		domain.StatusBlocked,
	},
	domain.StatusEscalated: { // a human decides
		domain.StatusIntegrated,    // child accepted
		domain.StatusReviewPending, // root accepted
		domain.StatusRunning,       // sent back with guidance
		domain.StatusDecomposing,   // parent sent back: head engineer follow-up (Phase 12)
		domain.StatusRejected,
		domain.StatusBlocked, // accepted, but the diff no longer applies
	},
	domain.StatusIntegrating: {
		domain.StatusVerifying, // combined verification
		domain.StatusBlocked,
	},
	domain.StatusReviewPending: {
		domain.StatusApproved,
		domain.StatusRejected,
		domain.StatusDecomposing, // changes requested: head engineer follow-up (Phase 12)
	},
	domain.StatusApproved: {
		domain.StatusDelivering, // Phase 11: approval delivers
		domain.StatusMerged,
	},
	domain.StatusDelivering: {
		domain.StatusMerged,  // merged locally
		domain.StatusPROpen,  // pull request opened
		domain.StatusBlocked, // conflict with the target, gates failed, dirty checkout, push or gh error
	},
	domain.StatusPROpen: {
		domain.StatusMerged,   // merged on the forge
		domain.StatusRejected, // closed without merging
	},
	domain.StatusMerged: {domain.StatusRollingBack},
	domain.StatusRollingBack: {
		domain.StatusRolledBack,   // reverted locally
		domain.StatusRevertPROpen, // revert pull request opened
		domain.StatusMerged,       // rollback failed: the delivery stands
	},
	domain.StatusRevertPROpen: {
		domain.StatusRolledBack, // revert merged on the forge
		domain.StatusMerged,     // revert closed without merging: the delivery stands
	},
	domain.StatusBlocked: {
		domain.StatusRunning,
		domain.StatusDelivering,  // delivery retried after fixing what blocked it
		domain.StatusDispatched,  // parent: a blocked subtask was resumed (13.6)
		domain.StatusDecomposing, // blocked before any subtask was planned: started again (13.6)
		domain.StatusEscalated,   // someone asked a person to decide
	},
	domain.StatusResumable: {domain.StatusRunning},
	// Integrated, Rejected, RolledBack terminal
}

func CanTransition(from, to domain.TaskStatus) bool {
	for _, allowed := range taskTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}
