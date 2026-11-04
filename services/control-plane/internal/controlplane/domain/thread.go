package domain

import (
	"strconv"
	"time"
)

// ThreadStatus is where a review thread stands. Agents can only move a
// thread to addressed (by replying); resolving and reopening are human.
// Whether its line still exists is separate: see Thread.Outdated.
type ThreadStatus string

const (
	ThreadOpen      ThreadStatus = "open"
	ThreadAddressed ThreadStatus = "addressed"
	ThreadResolved  ThreadStatus = "resolved"
	ThreadOutdated  ThreadStatus = "outdated" // unused since 12.4: see Thread.Outdated
)

// Who wrote a comment.
const (
	CommentHuman    = "human"
	CommentAgent    = "agent"
	CommentReviewer = "reviewer"
)

// Thread is a human's comment thread on a parent task's combined change,
// opened on one revision. A thread with no anchor is a general comment.
type Thread struct {
	ID       string `bson:"_id" json:"id"`
	TaskID   string `bson:"taskId" json:"taskId"`
	Revision int    `bson:"revision" json:"revision"` // 0: the work in progress, before any revision
	// Commit is the parent branch tip the anchor refers to: the revision the
	// thread is shown on (ShownOn), which moves as new revisions keep its line.
	Commit  string  `bson:"commit" json:"commit"`
	ShownOn int     `bson:"shownOn" json:"shownOn"`
	Anchor  *Anchor `bson:"anchor,omitempty" json:"anchor,omitempty"`
	// Origin is the anchor as the thread was opened, before any move.
	Origin *Anchor `bson:"origin,omitempty" json:"origin,omitempty"`
	// Outdated: a later revision (OutdatedOn) changed or removed the line.
	// The thread keeps its last place and context.
	Outdated    bool         `bson:"outdated,omitempty" json:"outdated,omitempty"`
	OutdatedOn  int          `bson:"outdatedOn,omitempty" json:"outdatedOn,omitempty"`
	Status      ThreadStatus `bson:"status" json:"status"`
	Comments    []Comment    `bson:"comments" json:"comments"`
	AddressedBy string       `bson:"addressedBy,omitempty" json:"addressedBy,omitempty"` // subtask id
	ResolvedBy  string       `bson:"resolvedBy,omitempty" json:"resolvedBy,omitempty"`
	CreatedAt   time.Time    `bson:"createdAt" json:"createdAt"`
	UpdatedAt   time.Time    `bson:"updatedAt" json:"updatedAt"`
}

// Anchor is a line of a revision's diff. The line's text and its
// neighbours in the hunk are kept so the thread can be re-located on a
// later revision.
type Anchor struct {
	Path     string   `bson:"path" json:"path"`
	Side     string   `bson:"side" json:"side"` // "old" | "new"
	Line     int      `bson:"line" json:"line"`
	LineText string   `bson:"lineText" json:"lineText"`
	Before   []string `bson:"before,omitempty" json:"before,omitempty"`
	After    []string `bson:"after,omitempty" json:"after,omitempty"`
}

type Comment struct {
	Author   string    `bson:"author" json:"author"`                         // a user's name, or an agent's
	AuthorID string    `bson:"authorId,omitempty" json:"authorId,omitempty"` // the user, for human comments (13.3)
	Kind     string    `bson:"kind" json:"kind"`                             // human | agent | reviewer
	Body     string    `bson:"body" json:"body"`
	Revision int       `bson:"revision" json:"revision"`
	At       time.Time `bson:"at" json:"at"`
}

// Shown is the revision the thread is placed on; threads from before
// ShownOn existed are on the revision they were opened on.
func (t *Thread) Shown() int {
	if t.ShownOn == 0 {
		return t.Revision
	}
	return t.ShownOn
}

// Unresolved reports whether the thread still needs a human's attention.
func (t *Thread) Unresolved() bool { return t.Status != ThreadResolved }

// Where names the thread's place in the change: "path:line", or "general".
func (t *Thread) Where() string {
	if t.Anchor == nil {
		return "general"
	}
	w := t.Anchor.Path + ":" + strconv.Itoa(t.Anchor.Line)
	if t.Anchor.Side == "old" {
		w += " (old)"
	}
	return w
}

// ThreadSummary is an unresolved thread as an approval records it.
type ThreadSummary struct {
	ID      string       `bson:"id" json:"id"`
	Where   string       `bson:"where" json:"where"`
	Status  ThreadStatus `bson:"status" json:"status"`
	Author  string       `bson:"author" json:"author"`
	Comment string       `bson:"comment" json:"comment"` // the thread's first comment
}

func (t *Thread) Summary() ThreadSummary {
	s := ThreadSummary{ID: t.ID, Where: t.Where(), Status: t.Status}
	if len(t.Comments) > 0 {
		s.Author, s.Comment = t.Comments[0].Author, t.Comments[0].Body
	}
	return s
}
