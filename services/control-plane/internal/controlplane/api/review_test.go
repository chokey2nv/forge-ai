package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

func TestParseReview(t *testing.T) {
	sub := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	cases := []struct {
		name    string
		body    AgentCompleteBody
		verdict string
		defects int
	}{
		{"pass", AgentCompleteBody{Status: "complete", Submission: sub(map[string]any{"verdict": "Pass", "summary": "ok"})}, domain.VerdictPass, 0},
		{"fail with defects", AgentCompleteBody{Status: "complete", Submission: sub(map[string]any{
			"verdict": "fail", "summary": "no", "defects": []map[string]string{{"severity": "major", "description": "x"}},
		})}, domain.VerdictFail, 1},
		{"unknown verdict rejects", AgentCompleteBody{Status: "complete", Submission: sub(map[string]any{"verdict": "maybe"})}, domain.VerdictFail, 0},
		{"no submission", AgentCompleteBody{Status: "complete", FinalMessage: "looks fine"}, domain.VerdictError, 0},
		{"agent blocked", AgentCompleteBody{Status: "blocked", Submission: sub(map[string]any{"verdict": "pass"})}, domain.VerdictError, 0},
		{"garbage", AgentCompleteBody{Status: "complete", Submission: json.RawMessage(`"pass"`)}, domain.VerdictError, 0},
	}
	for _, c := range cases {
		verdict, _, defects := parseReview(c.body)
		if verdict != c.verdict || len(defects) != c.defects {
			t.Errorf("%s: got %s with %d defects, want %s with %d", c.name, verdict, len(defects), c.verdict, c.defects)
		}
	}
}

func TestRoundPassedNeedsEveryReviewer(t *testing.T) {
	rv := func(v string) *domain.Review { return &domain.Review{Reviewer: "r", Verdict: v} }
	if !roundPassed(nil) {
		t.Error("a round with no reviewers passes")
	}
	if !roundPassed([]*domain.Review{rv("pass"), rv("pass")}) {
		t.Error("all pass must pass")
	}
	for _, other := range []string{domain.VerdictFail, domain.VerdictError} {
		if roundPassed([]*domain.Review{rv("pass"), rv(other)}) {
			t.Errorf("one %s must reject the round", other)
		}
	}
}

func TestRejectionFeedbackGroupsEveryRejectingReviewer(t *testing.T) {
	round := []*domain.Review{
		{Reviewer: "reviewer", Verdict: domain.VerdictPass, Summary: "fine"},
		{Reviewer: "test-reviewer", Verdict: domain.VerdictFail, Summary: "missing test",
			Defects: []domain.Defect{{Severity: "major", Description: "no test for page 0", Location: "tests/p.test.js"}}},
		{Reviewer: "security-reviewer", Verdict: domain.VerdictError, Summary: "reviewer could not run: start: boom"},
	}
	got := rejectionFeedback(round)
	for _, want := range []string{"## test-reviewer (fail)", "- [major] no test for page 0 (tests/p.test.js)", "## security-reviewer (error)"} {
		if !strings.Contains(got, want) {
			t.Errorf("feedback missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "## reviewer (") {
		t.Errorf("a passing reviewer must not appear in the feedback:\n%s", got)
	}
}

func TestAcceptsOnlyThePendingReviewer(t *testing.T) {
	p := &domain.ReviewPending{Round: 1, Index: 1, Reviewers: []string{"reviewer", "test-reviewer"}}
	cases := []struct {
		p        *domain.ReviewPending
		reviewer string
		want     bool
	}{
		{p, "test-reviewer", true},
		{p, "reviewer", false}, // stale: the previous reviewer reporting again
		{p, "implementer", false},
		{p, "", false},
		{nil, "reviewer", false}, // round already decided
		{&domain.ReviewPending{Index: 5, Reviewers: []string{"reviewer"}}, "reviewer", false},
	}
	for i, c := range cases {
		if got := acceptsReviewCallback(c.p, c.reviewer); got != c.want {
			t.Errorf("case %d (%q): got %v, want %v", i, c.reviewer, got, c.want)
		}
	}
}
