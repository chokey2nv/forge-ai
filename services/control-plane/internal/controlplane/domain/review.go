package domain

import "time"

// Review verdicts. "error" covers a reviewer that could not run or ended
// without submitting a verdict; it counts as a rejection.
const (
	VerdictPass  = "pass"
	VerdictFail  = "fail"
	VerdictError = "error"
)

// Review is one reviewer's verdict in one review round of a run.
type Review struct {
	ID              string    `bson:"_id" json:"id"` // <runId>-r<round>-<index>
	TaskID          string    `bson:"taskId" json:"taskId"`
	RunID           string    `bson:"runId" json:"runId"`
	Round           int       `bson:"round" json:"round"` // 1-based
	Index           int       `bson:"index" json:"index"` // position in the reviewers list
	Reviewer        string    `bson:"reviewer" json:"reviewer"`
	AgentDefinition string    `bson:"agentDefinition,omitempty" json:"agentDefinition,omitempty"`
	Verdict         string    `bson:"verdict" json:"verdict"`
	Summary         string    `bson:"summary,omitempty" json:"summary,omitempty"`
	Defects         []Defect  `bson:"defects,omitempty" json:"defects,omitempty"`
	StartedAt       time.Time `bson:"startedAt" json:"startedAt"`
	FinishedAt      time.Time `bson:"finishedAt" json:"finishedAt"`
}

type Defect struct {
	Severity    string `bson:"severity" json:"severity"`
	Description string `bson:"description" json:"description"`
	Location    string `bson:"location,omitempty" json:"location,omitempty"`
}

// ReviewPending is a review round in flight. Several reviewers report
// against one run ID, so a verdict is accepted only for an outstanding slot.
type ReviewPending struct {
	Round       int       `bson:"round" json:"round"`
	Reviewers   []string  `bson:"reviewers" json:"reviewers"`     // the round's reviewers, in order
	Outstanding []int     `bson:"outstanding" json:"outstanding"` // slots not yet claimed by a verdict or timeout
	Recorded    int       `bson:"recorded" json:"recorded"`       // verdicts saved; the last one decides the round
	StartedAt   time.Time `bson:"startedAt" json:"startedAt"`
}
