package domain

import (
	"fmt"
	"time"
)

// Limits caps what a task (with its subtasks) or a project may spend on
// model calls (Phase 13.6). Zero means no cap.
type Limits struct {
	MaxTokens  int64   `bson:"maxTokens,omitempty" json:"maxTokens,omitempty"`   // input + output
	MaxCostUSD float64 `bson:"maxCostUsd,omitempty" json:"maxCostUsd,omitempty"` // at the control plane's prices
}

// Spend is what model calls have used so far.
type Spend struct {
	InputTokens  int64   `bson:"inputTokens" json:"inputTokens"`
	OutputTokens int64   `bson:"outputTokens" json:"outputTokens"`
	CostUSD      float64 `bson:"costUsd" json:"costUsd"`
	// Unpriced: tokens from models with no price, which count towards a
	// token cap but not a cost cap.
	UnpricedTokens int64 `bson:"unpricedTokens" json:"unpricedTokens"`
}

func (s Spend) Tokens() int64 { return s.InputTokens + s.OutputTokens }

// Exceeded says which cap s has reached, or "" when none. what names the
// scope in the message: "task t-1", "project app".
func (l Limits) Exceeded(s Spend, what string) string {
	if l.MaxTokens > 0 && s.Tokens() >= l.MaxTokens {
		return fmt.Sprintf("token budget reached: %s used %d of %d tokens", what, s.Tokens(), l.MaxTokens)
	}
	if l.MaxCostUSD > 0 && s.CostUSD >= l.MaxCostUSD {
		return fmt.Sprintf("cost budget reached: %s spent $%.4f of $%.2f", what, s.CostUSD, l.MaxCostUSD)
	}
	return ""
}

// SpendRecord is one agent invocation's model usage: an implementer, a
// resumed implementer, a reviewer or a head engineer, as the adapter
// reported it last. Records are rewritten as the invocation goes on, so the
// sum over records is the total.
type SpendRecord struct {
	ID         string    `bson:"_id" json:"id"` // the adapter's invocation id
	RunID      string    `bson:"runId" json:"runId"`
	TaskID     string    `bson:"taskId" json:"taskId"`
	RootTaskID string    `bson:"rootTaskId" json:"rootTaskId"` // the top-level task, whose budget applies
	ProjectID  string    `bson:"projectId,omitempty" json:"projectId,omitempty"`
	Role       string    `bson:"role" json:"role"`
	Model      string    `bson:"model" json:"model"`
	Calls      int       `bson:"calls" json:"calls"`
	Spend      Spend     `bson:"spend" json:"spend"`
	UpdatedAt  time.Time `bson:"updatedAt" json:"updatedAt"`
}
