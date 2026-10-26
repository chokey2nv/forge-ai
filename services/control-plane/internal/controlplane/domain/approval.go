package domain

import "time"

type Approval struct {
	ID        string    `bson:"_id" json:"id"`
	TaskID    string    `bson:"taskId" json:"taskId"`
	RunID     string    `bson:"runId" json:"runId"`
	Action    string    `bson:"action" json:"action"`   // "approve" | "reject"
	Reviewer  string    `bson:"reviewer" json:"reviewer"`
	Rationale string    `bson:"rationale,omitempty" json:"rationale,omitempty"`
	DecidedAt time.Time `bson:"decidedAt" json:"decidedAt"`
}