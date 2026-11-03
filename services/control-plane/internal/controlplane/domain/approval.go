package domain

import "time"

type Approval struct {
	ID        string `bson:"_id" json:"id"`
	TaskID    string `bson:"taskId" json:"taskId"`
	RunID     string `bson:"runId" json:"runId"`
	Action    string `bson:"action" json:"action"` // "approve" | "reject"
	Reviewer  string `bson:"reviewer" json:"reviewer"`
	Rationale string `bson:"rationale,omitempty" json:"rationale,omitempty"`
	// UnresolvedThreads are the review threads still open when the decision was made.
	UnresolvedThreads []ThreadSummary `bson:"unresolvedThreads,omitempty" json:"unresolvedThreads,omitempty"`
	DecidedAt         time.Time       `bson:"decidedAt" json:"decidedAt"`
}
