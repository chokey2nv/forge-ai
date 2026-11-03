package api

import (
	"strings"
	"testing"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/statemachine"
)

func TestFollowUpGraphMustHandleEveryOpenThread(t *testing.T) {
	threads := []*domain.Thread{
		{ID: "thr-a", Anchor: &domain.Anchor{Path: "src/price.js", Side: "new", Line: 3}},
		{ID: "thr-b"},
	}
	cases := []struct {
		name      string
		addresses map[string][]string
		responses []graphResponse
		err       string
		byTask    map[string]string // submitted task -> thread IDs
		answered  string
	}{
		{name: "addressed by handle and by ID",
			addresses: map[string][]string{"t1": {"T1"}, "t2": {"thr-b"}},
			byTask:    map[string]string{"t1": "thr-a", "t2": "thr-b"}},
		{name: "one addressed, one answered",
			addresses: map[string][]string{"t1": {"T1"}},
			responses: []graphResponse{{Thread: "T2", Body: "Kept: the API needs it."}},
			byTask:    map[string]string{"t1": "thr-a"}, answered: "thr-b"},
		{name: "a thread left unhandled",
			addresses: map[string][]string{"t1": {"T1"}},
			err:       "neither addressed by a subtask nor answered: T2 (general)"},
		{name: "nothing handled",
			err: "T1 (src/price.js:3), T2 (general)"},
		{name: "unknown thread",
			addresses: map[string][]string{"t1": {"T1", "T9"}},
			err:       `task "t1" addresses unknown thread "T9"`},
		{name: "empty answer",
			addresses: map[string][]string{"t1": {"T1"}},
			responses: []graphResponse{{Thread: "T2", Body: " "}},
			err:       `empty response to thread "T2"`},
	}
	for _, c := range cases {
		byTask, answers, err := followUpGraph(threads, c.addresses, c.responses)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err %v, want %q", c.name, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		for task, want := range c.byTask {
			if got := strings.Join(byTask[task], ","); got != want {
				t.Errorf("%s: %s addresses %q, want %q", c.name, task, got, want)
			}
		}
		if _, ok := answers[c.answered]; c.answered != "" && !ok {
			t.Errorf("%s: %s not answered: %v", c.name, c.answered, answers)
		}
	}
	// With no open threads any graph will do.
	if _, _, err := followUpGraph(nil, nil, nil); err != nil {
		t.Errorf("no threads: %v", err)
	}
}

func TestFinalReviewIDsAfterEscalationRetries(t *testing.T) {
	p := &domain.Task{ID: "p", IntegrationBranch: "forge/p"}
	// r1's final review escalated and was sent back twice.
	p.FollowUps = []domain.FollowUp{{N: 1, Kind: domain.FollowUpEscalationRetry, Revision: 0}}
	if got := finalReviewRunID(p); got != "run-p-final-review-a2" {
		t.Errorf("after one retry: %s", got)
	}
	p.FollowUps = append(p.FollowUps, domain.FollowUp{N: 2, Kind: domain.FollowUpEscalationRetry, Revision: 0})
	if got, vr := finalReviewRunID(p), combinedVerificationID(p); got != "run-p-final-review-a3" || vr != "vr-p-combined-a3" {
		t.Errorf("after two retries: %s %s", got, vr)
	}
	// Accepted as r1; then changes requested: r2's first final review has no suffix.
	p.Revisions = []domain.Revision{{N: 1, FinalReviewRunID: "run-p-final-review-a3"}}
	p.FollowUps = append(p.FollowUps, domain.FollowUp{N: 3, Kind: domain.FollowUpRequestChanges, Revision: 1})
	if got := finalReviewRunID(p); got != "run-p-final-review-r2" {
		t.Errorf("r2: %s", got)
	}
	if got := approvedReviewRunID(p); got != "run-p-final-review-a3" {
		t.Errorf("approval of r1: %s", got)
	}
}

func TestRevisionBudget(t *testing.T) {
	h := &Handlers{}
	task := &domain.Task{}
	for n, want := range []bool{true, true, false} { // default 3 revisions: two follow-ups
		if got := h.followUpAllowed(task); got != want {
			t.Errorf("after %d follow-ups: allowed %v, want %v", n, got, want)
		}
		task.FollowUps = append(task.FollowUps, domain.FollowUp{N: n + 1})
	}
	task = &domain.Task{Budget: domain.Budget{MaxRevisions: 1}}
	if h.followUpAllowed(task) {
		t.Error("a budget of 1 revision allows no follow-up")
	}
	h.MaxRevisions = 5
	if got := h.maxRevisions(&domain.Task{}); got != 5 {
		t.Errorf("control-plane default: %d", got)
	}
	if !strings.Contains(h.budgetSpent(task), "approve or reject") {
		t.Error(h.budgetSpent(task))
	}
}

func TestFollowUpPrompt(t *testing.T) {
	task := &domain.Task{Requirement: "Fix applyDiscount", Acceptance: []string{"npm test passes"}}
	threads := []*domain.Thread{
		{ID: "thr-a", Revision: 1, Anchor: &domain.Anchor{Path: "src/price.js", Side: "new", Line: 3, LineText: "  return Math.round(x);"},
			Comments: []domain.Comment{{Author: "dana", Kind: "human", Body: "Name the 100."}}},
	}
	children := []*domain.Task{{Title: "Fix applyDiscount", AssignedAgent: "implementer", Status: domain.StatusIntegrated}}
	p := buildFollowUpTask(task, &domain.FollowUp{Kind: domain.FollowUpRequestChanges, Revision: 1, Reviewer: "dana", Summary: "Also add a test."},
		children, threads, "diff --git a/src/price.js b/src/price.js\n", "", nil)
	for _, want := range []string{
		"revision 1 of the combined change", "# Subtasks already done", "- Fix applyDiscount (implementer, INTEGRATED)",
		"# What dana asked for\n\nAlso add a test.", "## T1 — src/price.js:3 (revision 1)", "`return Math.round(x);`",
		"**dana** (human): Name the 100.", "```diff\ndiff --git a/src/price.js", "`addresses`", "`responses`",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	brief := threadBrief(threads, []string{"thr-a"})
	if !strings.Contains(brief, "src/price.js:3 (`return Math.round(x);`)") || !strings.Contains(brief, "dana: Name the 100.") {
		t.Errorf("brief: %s", brief)
	}
}

func TestFollowUpTransitions(t *testing.T) {
	for _, e := range [][2]domain.TaskStatus{
		{domain.StatusReviewPending, domain.StatusDecomposing},
		{domain.StatusEscalated, domain.StatusDecomposing},
		{domain.StatusDecomposing, domain.StatusReviewPending},
		{domain.StatusDecomposing, domain.StatusEscalated},
	} {
		if !statemachine.CanTransition(e[0], e[1]) {
			t.Errorf("%s -> %s must be allowed", e[0], e[1])
		}
	}
}
