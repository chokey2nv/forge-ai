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
	// CompareAndSetStatus moves a task from `from` to `to` only if it is still
	// in `from`. It reports false when another writer got there first.
	CompareAndSetStatus(ctx context.Context, id string, from, to domain.TaskStatus, at time.Time) (bool, error)
	UpdateLatestRun(ctx context.Context, id, runID string, at time.Time) error
	// List returns tasks, newest first; with projectIDs non-nil, only those
	// projects' (Phase 13.4).
	List(ctx context.Context, status domain.TaskStatus, projectIDs []string, limit int) ([]*domain.Task, error)
	// Migration to projects (13.4): repositories of tasks without a project,
	// and assigning those tasks to one.
	UnassignedRepoPaths(ctx context.Context) ([]string, error)
	AssignProject(ctx context.Context, repoPath, projectID string) (int64, error)

	// Phase 8 additions:
	ListByStatus(ctx context.Context, status domain.TaskStatus, limit int) ([]*domain.Task, error)
	ListChildren(ctx context.Context, parentID string) ([]*domain.Task, error)
	UpdateIntegration(ctx context.Context, id, worktree, branch, baseCommit string, at time.Time) error
	UpdateDelivery(ctx context.Context, id string, d *domain.Delivery, at time.Time) error
	// AddRevision appends rev unless a revision with its number exists.
	AddRevision(ctx context.Context, id string, rev domain.Revision, at time.Time) error
	// AddFollowUp appends f unless a follow-up with its number exists.
	AddFollowUp(ctx context.Context, id string, f domain.FollowUp, at time.Time) error
	SetFollowUpOutcome(ctx context.Context, id string, n int, outcome string, at time.Time) error
	// SetSpendLimits changes a task's token and cost caps (13.6).
	SetSpendLimits(ctx context.Context, id string, l domain.Limits, at time.Time) error
}

type RunRepo interface {
	Create(ctx context.Context, r *domain.WorkflowRun) error
	Get(ctx context.Context, id string) (*domain.WorkflowRun, error)
	UpdateStatus(ctx context.Context, id string, status domain.RunStatus, reason string, at time.Time) error
	SaveCheckpoint(ctx context.Context, id string, cp *domain.Checkpoint, at time.Time) error
	Heartbeat(ctx context.Context, id string, at time.Time) error
	MarkStale(ctx context.Context, before time.Time) ([]*domain.WorkflowRun, error)

	// Phase 4 additions:
	UpdateWorkspace(ctx context.Context, id, worktreePath, containerName string, at time.Time) error
	UpdateVerification(ctx context.Context, id string, attempt int, passed bool, status domain.RunStatus, reason string, at time.Time) error

	SavePlan(ctx context.Context, id string, plan *domain.Plan, at time.Time) error
	SaveReviewVerdict(ctx context.Context, id, verdict string, at time.Time) error
	// StartReviewRound records a reviewer rejection and resets verification
	// attempts for the reworked change.
	StartReviewRound(ctx context.Context, id string, round int, at time.Time) error
	// SetReviewPending records the reviewer the run waits for; nil clears it.
	SetReviewPending(ctx context.Context, id string, p *domain.ReviewPending, at time.Time) error
	// ClaimReviewSlot removes index from the round's outstanding reviewers;
	// the one caller that gets true settles that reviewer.
	ClaimReviewSlot(ctx context.Context, id string, round, index int) (bool, error)
	// MarkReviewRecorded counts a saved verdict and returns the new count.
	MarkReviewRecorded(ctx context.Context, id string, round int) (int, error)
	// ListReviewsPendingSince returns runs waiting on a reviewer started before cutoff.
	ListReviewsPendingSince(ctx context.Context, cutoff time.Time) ([]*domain.WorkflowRun, error)

	ListByTask(ctx context.Context, taskID string) ([]*domain.WorkflowRun, error)

	// Workspace lifecycle: runs whose container/workspace have not been removed yet.
	ListUnreleasedWorkspaces(ctx context.Context) ([]*domain.WorkflowRun, error)
	MarkWorkspaceReleased(ctx context.Context, id string, at time.Time) error
}

type EventRepo interface {
	Append(ctx context.Context, e *domain.Event) error
	ListByRun(ctx context.Context, runID string) ([]*domain.Event, error)
	// ListByTask returns a task's events, oldest first. State transitions are
	// recorded per task, not per run.
	ListByTask(ctx context.Context, taskID string) ([]*domain.Event, error)
}
type ReviewRepo interface {
	Create(ctx context.Context, r *domain.Review) error
	ListByRun(ctx context.Context, runID string) ([]*domain.Review, error)
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

// ThreadRepo holds human review threads on a parent's combined change.
type ThreadRepo interface {
	Create(ctx context.Context, t *domain.Thread) error
	Get(ctx context.Context, id string) (*domain.Thread, error)
	// ListByTask returns a task's threads, oldest first.
	ListByTask(ctx context.Context, taskID string) ([]*domain.Thread, error)
	AddComment(ctx context.Context, id string, c domain.Comment) error
	// SetStatus records a status change; by is who resolved it (empty otherwise).
	SetStatus(ctx context.Context, id string, status domain.ThreadStatus, by string, at time.Time) error
	// SetAddressedBy records the follow-up subtask that takes the thread on.
	SetAddressedBy(ctx context.Context, id, taskID string, at time.Time) error
	// SavePlacement records where a thread sits on a new revision: moved to
	// anchor on shownOn at commit, or outdated on revision outdatedOn.
	SavePlacement(ctx context.Context, id string, anchor *domain.Anchor, shownOn int, commit string, outdatedOn int, at time.Time) error
}

// Phase 13.2: people, their console sessions and their API tokens.
type UserRepo interface {
	Create(ctx context.Context, u *domain.User) error // fails if the name is taken
	Get(ctx context.Context, id string) (*domain.User, error)
	GetByName(ctx context.Context, name string) (*domain.User, error)
	List(ctx context.Context) ([]*domain.User, error)
	Count(ctx context.Context) (int64, error)
	SetRole(ctx context.Context, id string, role domain.Role, instanceAdmin bool) error
}

type SessionRepo interface {
	Create(ctx context.Context, s *domain.Session) error
	Get(ctx context.Context, hash string) (*domain.Session, error)
	Touch(ctx context.Context, hash string, seen, expires time.Time) error
	Delete(ctx context.Context, hash string) error
}

type TokenRepo interface {
	Create(ctx context.Context, t *domain.APIToken) error
	GetByHash(ctx context.Context, hash string) (*domain.APIToken, error)
	ListByUser(ctx context.Context, userID string) ([]*domain.APIToken, error)
	Touch(ctx context.Context, id string, at time.Time) error
	Revoke(ctx context.Context, id, userID string, at time.Time) (bool, error)
}

// ProjectRepo holds projects (Phase 13.4).
type ProjectRepo interface {
	Create(ctx context.Context, p *domain.Project) error
	Get(ctx context.Context, id string) (*domain.Project, error)
	List(ctx context.Context) ([]*domain.Project, error)
	// ListForUser returns the projects the user is a member of.
	ListForUser(ctx context.Context, userID string) ([]*domain.Project, error)
	// ByRepo is the project a repository is registered in.
	ByRepo(ctx context.Context, path string) (*domain.Project, error)
	AddRepo(ctx context.Context, id string, repo domain.ProjectRepo) error
	RemoveRepo(ctx context.Context, id, path string) error
	// SetMember adds the member or changes their role.
	SetMember(ctx context.Context, id string, m domain.ProjectMember) error
	RemoveMember(ctx context.Context, id, userID string) error
	SetBudget(ctx context.Context, id string, l domain.Limits) error
}

// SpendRepo keeps each agent invocation's model usage (Phase 13.6).
type SpendRepo interface {
	// Record writes the invocation's latest totals.
	Record(ctx context.Context, r *domain.SpendRecord) error
	// Total sums the records whose field ("rootTaskId", "taskId" or
	// "projectId") is value.
	Total(ctx context.Context, field, value string) (domain.Spend, error)
}
