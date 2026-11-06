package api

import (
	"testing"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

func TestBoardLaneFor(t *testing.T) {
	created := &domain.Task{Status: domain.StatusCreated}
	merged := &domain.Task{Status: domain.StatusMerged}
	cases := []struct {
		name   string
		task   *domain.Task
		column string
		lane   domain.BoardLane
		ok     bool
	}{
		{"not started: to todo", created, "todo", domain.LaneTodo, true},
		{"not started: back to backlog", created, "backlog", "", true},
		{"merged: closed", merged, "closed", domain.LaneClosed, true},
		{"merged: reopened", merged, "done", "", true},
		{"not started can't close", created, "closed", "", false},
		{"merged can't go to todo", merged, "todo", "", false},
		{"running can't be moved", &domain.Task{Status: domain.StatusRunning}, "todo", "", false},
		{"waiting for approval can't skip to done", &domain.Task{Status: domain.StatusReviewPending}, "done", "", false},
		{"rejected can't be reopened", &domain.Task{Status: domain.StatusRejected}, "done", "", false},
		{"a subtask follows its parent", &domain.Task{Status: domain.StatusCreated, ParentID: "p"}, "todo", "", false},
		{"unknown column", created, "progress", "", false},
	}
	for _, c := range cases {
		lane, why := boardLaneFor(c.task, c.column)
		if (why == "") != c.ok || lane != c.lane {
			t.Errorf("%s: got lane %q, why %q", c.name, lane, why)
		}
	}
}
