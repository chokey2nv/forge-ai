package domain

import "time"

type Approval struct {
	ID         string `bson:"_id" json:"id"`
	TaskID     string `bson:"taskId" json:"taskId"`
	RunID      string `bson:"runId" json:"runId"`
	Action     string `bson:"action" json:"action"`     // "approve" | "reject"
	Reviewer   string `bson:"reviewer" json:"reviewer"` // the signed-in user's name (free text before 13.3)
	ReviewerID string `bson:"reviewerId,omitempty" json:"reviewerId,omitempty"`
	Rationale  string `bson:"rationale,omitempty" json:"rationale,omitempty"`
	// UnresolvedThreads are the review threads still open when the decision was made.
	UnresolvedThreads []ThreadSummary `bson:"unresolvedThreads,omitempty" json:"unresolvedThreads,omitempty"`
	DecidedAt         time.Time       `bson:"decidedAt" json:"decidedAt"`
}
