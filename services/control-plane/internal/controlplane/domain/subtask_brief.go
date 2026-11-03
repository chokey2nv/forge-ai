package domain

import (
	"fmt"
	"strings"
)

// SubtaskBrief tells an agent working on, or reviewing, one subtask where
// its boundaries are: the files it may change and what its siblings own.
// Without it a reviewer can demand work a sibling is assigned (tests, say),
// and an implementer that complies fails the diff-scope gate. Empty for a
// task that is not a subtask.
func SubtaskBrief(task *Task, siblings []*Task, forReviewer bool) string {
	if task.ParentID == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Scope of this subtask\n")
	b.WriteString("This is one subtask of a larger requirement.\n")
	if len(task.DiffScope) > 0 {
		fmt.Fprintf(&b, "It may change only: %s. Changes to other files fail verification.\n", strings.Join(task.DiffScope, ", "))
	} else {
		b.WriteString("No file scope was declared for it.\n")
	}
	var others []*Task
	for _, s := range siblings {
		if s.ID != task.ID {
			others = append(others, s)
		}
	}
	if len(others) > 0 {
		b.WriteString("\nSibling subtasks own the rest of the requirement:\n")
		for _, s := range others {
			title := s.Title
			if title == "" {
				title = s.ID
			}
			files := "no declared files"
			if len(s.DiffScope) > 0 {
				files = strings.Join(s.DiffScope, ", ")
			}
			fmt.Fprintf(&b, "- %s (%s; %s)\n", title, files, s.Status)
		}
	}
	if forReviewer {
		b.WriteString("\nJudge this subtask only on what it may change. Do not fail it for work a sibling subtask owns; mention such a gap as a minor defect at most.\n")
	} else {
		b.WriteString("\nDo not change files outside this subtask's scope; work a sibling owns will be done there.\n")
	}
	// A follow-up subtask answers the review threads it addresses (Phase 12.4).
	if len(task.Addresses) > 0 {
		if forReviewer {
			b.WriteString("\nThe review comments in the requirement are answered by the implementer separately, with a tool reviewers do not have; judge only the code change.\n")
		} else {
			b.WriteString("\nWhen your change is done, reply on each review comment listed in the requirement with reply_to_thread (thread T1, T2, …): what you changed, or why you did not.\n")
		}
	}
	return b.String()
}
