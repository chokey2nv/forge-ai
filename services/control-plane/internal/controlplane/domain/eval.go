package domain

import "time"

type EvalResult struct {
	ID         string    `bson:"_id" json:"id"`
	CaseID     string    `bson:"caseId" json:"caseId"`
	RunID      string    `bson:"runId" json:"runId"`
	TaskID     string    `bson:"taskId" json:"taskId"`
	Provider   string    `bson:"provider" json:"provider"`
	Model      string    `bson:"model" json:"model"`
	Passed     bool      `bson:"passed" json:"passed"`
	Status     string    `bson:"status" json:"status"`  // terminal task status
	Reason     string    `bson:"reason,omitempty" json:"reason,omitempty"`
	StartedAt  time.Time `bson:"startedAt" json:"startedAt"`
	FinishedAt time.Time `bson:"finishedAt" json:"finishedAt"`
	DurationMS int       `bson:"durationMs" json:"durationMs"`
}