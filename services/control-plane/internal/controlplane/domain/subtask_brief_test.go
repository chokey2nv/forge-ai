package domain

import (
	"strings"
	"testing"
)

func TestSubtaskBrief(t *testing.T) {
	fix := &Task{ID: "a", ParentID: "p", Title: "Fix paginate", DiffScope: []string{"src/paginate.js"}}
	tests := &Task{ID: "b", ParentID: "p", Title: "Add regression tests", DiffScope: []string{"tests/paginate.test.js"}, Status: StatusCreated}

	r := SubtaskBrief(fix, []*Task{fix, tests}, true)
	for _, want := range []string{
		"may change only: src/paginate.js",
		"- Add regression tests (tests/paginate.test.js; CREATED)",
		"Do not fail it for work a sibling subtask owns",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("reviewer brief missing %q:\n%s", want, r)
		}
	}
	if strings.Contains(r, "- Fix paginate") {
		t.Error("a subtask is not its own sibling")
	}

	impl := SubtaskBrief(fix, []*Task{fix, tests}, false)
	if !strings.Contains(impl, "Do not change files outside this subtask's scope") {
		t.Errorf("implementer brief missing the scope instruction:\n%s", impl)
	}

	if SubtaskBrief(&Task{ID: "root"}, nil, true) != "" {
		t.Error("a root task gets no subtask brief")
	}
	if !strings.Contains(SubtaskBrief(&Task{ID: "c", ParentID: "p"}, nil, false), "No file scope was declared") {
		t.Error("a subtask without a scope must say so")
	}
}

func TestSubtaskBriefThreadReplies(t *testing.T) {
	task := &Task{ID: "c", ParentID: "p", Addresses: []string{"thr-a"}}
	if b := SubtaskBrief(task, nil, false); !strings.Contains(b, "reply on each review comment listed in the requirement with reply_to_thread") {
		t.Errorf("implementer brief: %s", b)
	}
	if b := SubtaskBrief(task, nil, true); !strings.Contains(b, "judge only the code change") || strings.Contains(b, "reply on each") {
		t.Errorf("reviewer brief: %s", b)
	}
	task.Addresses = nil
	if b := SubtaskBrief(task, nil, false); strings.Contains(b, "reply_to_thread") {
		t.Errorf("a subtask without threads is told to reply: %s", b)
	}
}
