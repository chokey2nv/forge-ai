package domain

import "time"

type VerificationRun struct {
	ID            string       `bson:"_id" json:"id"`
	RunID         string       `bson:"runId" json:"runId"`
	TaskID        string       `bson:"taskId" json:"taskId"`
	Attempt       int          `bson:"attempt" json:"attempt"`
	Passed        bool         `bson:"passed" json:"passed"`
	Gates         []GateResult `bson:"gates" json:"gates"`
	WorkspaceDiff string       `bson:"workspaceDiff" json:"workspaceDiff"`
	StartedAt     time.Time    `bson:"startedAt" json:"startedAt"`
	FinishedAt    time.Time    `bson:"finishedAt" json:"finishedAt"`
	DurationMS    int          `bson:"durationMs" json:"durationMs"`
}

type GateResult struct {
	Name       string `bson:"name" json:"name"`
	Passed     bool   `bson:"passed" json:"passed"`
	ExitCode   int    `bson:"exitCode" json:"exitCode"`
	Output     string `bson:"output" json:"output"`
	DurationMS int    `bson:"durationMs" json:"durationMs"`
	Skipped    bool   `bson:"skipped" json:"skipped"`
	SkipReason string `bson:"skipReason,omitempty" json:"skipReason,omitempty"`
}
