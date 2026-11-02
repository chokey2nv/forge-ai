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
