package domain

import "time"

type TaskStatus string

const (
	StatusCreated       TaskStatus = "CREATED"
	StatusPlanned       TaskStatus = "PLANNED"
	StatusDispatched    TaskStatus = "DISPATCHED"
	StatusRunning       TaskStatus = "RUNNING"
	StatusVerifying     TaskStatus = "VERIFYING" // reserved: Phase 4
	StatusReviewPending TaskStatus = "REVIEW_PENDING"
	StatusApproved      TaskStatus = "APPROVED"
	StatusMerged        TaskStatus = "MERGED"
	StatusBlocked       TaskStatus = "BLOCKED"
	StatusResumable     TaskStatus = "RESUMABLE"
	StatusRejected      TaskStatus = "REJECTED"
	StatusAgentReview   TaskStatus = "AGENT_REVIEW"

	StatusDecomposing  TaskStatus = "DECOMPOSING"    // head-engineer is running
	StatusIntegrating  TaskStatus = "INTEGRATING"    // applying child diffs to parent
	StatusIntegrated   TaskStatus = "INTEGRATED"     // child terminal: merged into parent
	StatusEscalated    TaskStatus = "ESCALATED"      // reviewers kept rejecting; a human decides (Phase 9)
	StatusDelivering   TaskStatus = "DELIVERING"     // approved; merging or opening the PR (Phase 11)
	StatusPROpen       TaskStatus = "PR_OPEN"        // pull request opened; a human merges it on the forge
	StatusRollingBack  TaskStatus = "ROLLING_BACK"   // a human asked to undo a delivery
	StatusRevertPROpen TaskStatus = "REVERT_PR_OPEN" // revert pull request opened; merged on the forge -> ROLLED_BACK
	StatusRolledBack   TaskStatus = "ROLLED_BACK"    // the delivery was reverted on the target

)

type Budget struct {
	MaxSteps    int `bson:"maxSteps" json:"maxSteps"`
	MaxRetries  int `bson:"maxRetries" json:"maxRetries"`
	WallClockMS int `bson:"wallClockMs" json:"wallClockMs"`
}

type Provenance struct {
	AgentDefinition string `bson:"agentDefinition" json:"agentDefinition"` // e.g. "implementer@v1"
	Model           string `bson:"model" json:"model"`
	Provider        string `bson:"provider" json:"provider"`
	StandardsCommit string `bson:"standardsCommit,omitempty" json:"standardsCommit,omitempty"`
}

type Task struct {
	ID          string     `bson:"_id" json:"id"`
	Requirement string     `bson:"requirement" json:"requirement"`
	Acceptance  []string   `bson:"acceptance" json:"acceptance"`
	RepoPath    string     `bson:"repoPath" json:"repoPath"`
	BaseCommit  string     `bson:"baseCommit" json:"baseCommit"`
	Status      TaskStatus `bson:"status" json:"status"`
	Budget      Budget     `bson:"budget" json:"budget"`
	Provenance  Provenance `bson:"provenance" json:"provenance"`
	CreatedAt   time.Time  `bson:"createdAt" json:"createdAt"`
	UpdatedAt   time.Time  `bson:"updatedAt" json:"updatedAt"`
	DiffScope   []string   `bson:"diffScope,omitempty" json:"diffScope,omitempty"`
	LatestRunID string     `bson:"latestRunId,omitempty" json:"latestRunId,omitempty"`

	ParentID  string   `bson:"parentId,omitempty" json:"parentId,omitempty"`
	DependsOn []string `bson:"dependsOn,omitempty" json:"dependsOn,omitempty"`
	// ImplicitDependsOn: added by the platform when this task's diffScope
	// overlaps an earlier independent task's (or either is undeclared), so
	// the two never run in parallel. Kept apart from the head engineer's
	// DependsOn so its intent stays visible.
	ImplicitDependsOn []string `bson:"implicitDependsOn,omitempty" json:"implicitDependsOn,omitempty"`
	AssignedAgent     string   `bson:"assignedAgent,omitempty" json:"assignedAgent,omitempty"`
	Title             string   `bson:"title,omitempty" json:"title,omitempty"`

	// Child task only: which branch it works on. Parent tasks have a branch too.
	Branch string `bson:"branch,omitempty" json:"branch,omitempty"`

	// Root task: the integration branch that children merge into.
	// TargetBranch is the branch the task's work is delivered to (Phase 11),
	// recorded at creation. IntegrationBranch, the task's parent branch, is
	// created then too, at the target's tip.
	TargetBranch string `bson:"targetBranch,omitempty" json:"targetBranch,omitempty"`
	// Delivery is set once an approved task starts delivering.
	Delivery              *Delivery `bson:"delivery,omitempty" json:"delivery,omitempty"`
	IntegrationWorktree   string    `bson:"integrationWorktree,omitempty" json:"integrationWorktree,omitempty"`
	IntegrationBranch     string    `bson:"integrationBranch,omitempty" json:"integrationBranch,omitempty"`
	IntegrationBaseCommit string    `bson:"integrationBaseCommit,omitempty" json:"integrationBaseCommit,omitempty"`

	// Revisions: each time the combined change is handed to a human for
	// review (Phase 12), the parent branch's tip is recorded, so every
	// review round can be shown and compared later.
	Revisions []Revision `bson:"revisions,omitempty" json:"revisions,omitempty"`
}

// Revision is one version of a parent's combined change put up for human
// review: the diff BaseCommit..Commit.
type Revision struct {
	N                int       `bson:"n" json:"n"` // 1-based
	Commit           string    `bson:"commit" json:"commit"`
	BaseCommit       string    `bson:"baseCommit" json:"baseCommit"`
	FinalReviewRunID string    `bson:"finalReviewRunId" json:"finalReviewRunId"`
	At               time.Time `bson:"at" json:"at"`
}

// Delivery records how an approved task reached (or failed to reach) its
// target branch.
type Delivery struct {
	Mode         string `bson:"mode" json:"mode"` // pull_request | local_merge
	TargetBranch string `bson:"targetBranch" json:"targetBranch"`
	Branch       string `bson:"branch" json:"branch"`
	Remote       string `bson:"remote,omitempty" json:"remote,omitempty"`
	// TargetMoved: the target had advanced since the work started, so it was
	// merged into the branch and the verification gates ran again.
	TargetMoved bool      `bson:"targetMoved,omitempty" json:"targetMoved,omitempty"`
	MergeCommit string    `bson:"mergeCommit,omitempty" json:"mergeCommit,omitempty"`
	PRURL       string    `bson:"prUrl,omitempty" json:"prUrl,omitempty"`
	PRNumber    int       `bson:"prNumber,omitempty" json:"prNumber,omitempty"`
	Error       string    `bson:"error,omitempty" json:"error,omitempty"`
	StartedAt   time.Time `bson:"startedAt" json:"startedAt"`
	FinishedAt  time.Time `bson:"finishedAt,omitempty" json:"finishedAt,omitzero"`
	Rollback    *Rollback `bson:"rollback,omitempty" json:"rollback,omitempty"`
}

// Rollback undoes a delivery by reverting it (never by rewriting history):
// a revert commit on the target, or a revert pull request.
type Rollback struct {
	Reviewer     string    `bson:"reviewer" json:"reviewer"`
	Rationale    string    `bson:"rationale,omitempty" json:"rationale,omitempty"`
	RevertCommit string    `bson:"revertCommit,omitempty" json:"revertCommit,omitempty"`
	Branch       string    `bson:"branch,omitempty" json:"branch,omitempty"` // the revert PR's branch
	PRURL        string    `bson:"prUrl,omitempty" json:"prUrl,omitempty"`
	PRNumber     int       `bson:"prNumber,omitempty" json:"prNumber,omitempty"`
	Error        string    `bson:"error,omitempty" json:"error,omitempty"`
	StartedAt    time.Time `bson:"startedAt" json:"startedAt"`
	FinishedAt   time.Time `bson:"finishedAt,omitempty" json:"finishedAt,omitzero"`
}
