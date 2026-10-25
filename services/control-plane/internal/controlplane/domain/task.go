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
	DiffScope []string `bson:"diffScope,omitempty" json:"diffScope,omitempty"`
}

