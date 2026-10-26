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

	StatusDecomposing TaskStatus = "DECOMPOSING" // head-engineer is running
	StatusIntegrating TaskStatus = "INTEGRATING" // applying child diffs to parent
	StatusIntegrated  TaskStatus = "INTEGRATED"  // child terminal: merged into parent

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

	ParentID      string   `bson:"parentId,omitempty" json:"parentId,omitempty"`
	DependsOn     []string `bson:"dependsOn,omitempty" json:"dependsOn,omitempty"`
	AssignedAgent string   `bson:"assignedAgent,omitempty" json:"assignedAgent,omitempty"`
	Title         string   `bson:"title,omitempty" json:"title,omitempty"`

	// Child task only: which branch it works on. Parent tasks have a branch too.
	Branch string `bson:"branch,omitempty" json:"branch,omitempty"`

	// Root task: the integration branch that children merge into.
	IntegrationWorktree   string `bson:"integrationWorktree,omitempty" json:"integrationWorktree,omitempty"`
	IntegrationBranch     string `bson:"integrationBranch,omitempty" json:"integrationBranch,omitempty"`
	IntegrationBaseCommit string `bson:"integrationBaseCommit,omitempty" json:"integrationBaseCommit,omitempty"`
}
