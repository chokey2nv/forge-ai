package domain

import "time"

type RunStatus string

const (
	RunDispatched RunStatus = "DISPATCHED"
	// RunProvisioning: the workspace is being created (clone, dependency
	// install). No agent is running yet, so the stale-run watcher ignores it.
	RunProvisioning RunStatus = "PROVISIONING"
	RunRunning      RunStatus = "RUNNING"
	RunVerifying    RunStatus = "VERIFYING"
	RunResumable    RunStatus = "RESUMABLE"
	RunBlocked      RunStatus = "BLOCKED"
	RunComplete     RunStatus = "COMPLETE"
	RunError        RunStatus = "ERROR"
	RunEscalated    RunStatus = "ESCALATED" // reviewers kept rejecting; awaiting a human decision
)

type WorkflowRun struct {
	ID                  string         `bson:"_id" json:"id"`
	TaskID              string         `bson:"taskId" json:"taskId"`
	Status              RunStatus      `bson:"status" json:"status"`
	WorktreePath        string         `bson:"worktreePath" json:"worktreePath"`
	ContainerName       string         `bson:"containerName" json:"containerName"`
	BaseCommit          string         `bson:"baseCommit" json:"baseCommit"`
	Provenance          Provenance     `bson:"provenance" json:"provenance"`
	StepCount           int            `bson:"stepCount" json:"stepCount"`
	VerificationAttempt int            `bson:"verificationAttempt" json:"verificationAttempt"`
	VerificationPassed  bool           `bson:"verificationPassed" json:"verificationPassed"`
	ReviewRound         int            `bson:"reviewRound" json:"reviewRound"` // reviewer rejections so far
	ReviewPending       *ReviewPending `bson:"reviewPending,omitempty" json:"reviewPending,omitempty"`
	LastCheckpoint      *Checkpoint    `bson:"lastCheckpoint,omitempty" json:"lastCheckpoint,omitempty"`
	LastHeartbeat       time.Time      `bson:"lastHeartbeat" json:"lastHeartbeat"`
	BlockedReason       string         `bson:"blockedReason,omitempty" json:"blockedReason,omitempty"`
	FinalMessage        string         `bson:"finalMessage,omitempty" json:"finalMessage,omitempty"`
	CreatedAt           time.Time      `bson:"createdAt" json:"createdAt"`
	UpdatedAt           time.Time      `bson:"updatedAt" json:"updatedAt"`

	// WorkspaceReleasedAt is set once the run's container and workspace
	// directory have been removed.
	WorkspaceReleasedAt *time.Time `bson:"workspaceReleasedAt,omitempty" json:"workspaceReleasedAt,omitempty"`

	Plan            *Plan  `bson:"plan,omitempty" json:"plan,omitempty"`
	ReviewerVerdict string `bson:"reviewerVerdict,omitempty" json:"reviewerVerdict,omitempty"` // "pass" | "fail" | ""
}

type Plan struct {
	Steps     []PlanStep `bson:"steps" json:"steps"`
	Risks     []string   `bson:"risks,omitempty" json:"risks,omitempty"`
	CreatedAt time.Time  `bson:"createdAt" json:"createdAt"`
}

type PlanStep struct {
	Description string   `bson:"description" json:"description"`
	Files       []string `bson:"files,omitempty" json:"files,omitempty"`
}

type Checkpoint struct {
	Messages      []Message `bson:"messages" json:"messages"`
	StepCount     int       `bson:"stepCount" json:"stepCount"`
	Usage         []Usage   `bson:"usage" json:"usage"`
	Status        string    `bson:"status" json:"status"` // running | complete | blocked | error
	WorkspaceDiff string    `bson:"workspaceDiff" json:"workspaceDiff"`
	SavedAt       time.Time `bson:"savedAt" json:"savedAt"`
}

type Message struct {
	Role       string `bson:"role" json:"role"`
	Content    string `bson:"content" json:"content"`
	ToolCallID string `bson:"toolCallId,omitempty" json:"toolCallId,omitempty"`
	ToolName   string `bson:"toolName,omitempty" json:"toolName,omitempty"`
	// ToolCalls must round-trip: a resumed history with tool results but no
	// matching assistant tool calls is rejected by the model API.
	ToolCalls []ToolCall `bson:"toolCalls,omitempty" json:"toolCalls,omitempty"`
}

type ToolCall struct {
	ID        string         `bson:"id" json:"id"`
	Name      string         `bson:"name" json:"name"`
	Arguments map[string]any `bson:"arguments" json:"arguments"`
}

type Usage struct {
	InputTokens  int `bson:"inputTokens,omitempty" json:"inputTokens,omitempty"`
	OutputTokens int `bson:"outputTokens,omitempty" json:"outputTokens,omitempty"`
	WallMS       int `bson:"wallMs" json:"wallMs"`
}

type Event struct {
	ID      string    `bson:"_id" json:"id"`
	RunID   string    `bson:"runId" json:"runId"`
	TaskID  string    `bson:"taskId,omitempty" json:"taskId,omitempty"`
	Type    string    `bson:"type" json:"type"` // state.transition | checkpoint | complete | error
	From    string    `bson:"from,omitempty" json:"from,omitempty"`
	To      string    `bson:"to,omitempty" json:"to,omitempty"`
	Reason  string    `bson:"reason,omitempty" json:"reason,omitempty"`
	Payload any       `bson:"payload,omitempty" json:"payload,omitempty"`
	At      time.Time `bson:"at" json:"at"`
}
