package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

func TestValidateEscalation(t *testing.T) {
	child := &domain.Task{ID: "c", ParentID: "p", Status: domain.StatusEscalated}
	parent := &domain.Task{ID: "p", IntegrationBranch: "forge/p", Status: domain.StatusEscalated}
	running := &domain.Task{ID: "c", ParentID: "p", Status: domain.StatusRunning}

	cases := []struct {
		name string
		task *domain.Task
		req  escalationRequest
		want int
	}{
		{"accept child", child, escalationRequest{Decision: "accept", Reviewer: "ana"}, 0},
		{"retry child", child, escalationRequest{Decision: "retry", Reviewer: "ana", Guidance: "inline it"}, 0},
		{"reject child", child, escalationRequest{Decision: "reject", Reviewer: "ana"}, 0},
		{"accept parent", parent, escalationRequest{Decision: "accept", Reviewer: "ana"}, 0},
		{"retry parent: a head engineer follow-up", parent, escalationRequest{Decision: "retry", Reviewer: "ana"}, 0},
		{"unknown decision", child, escalationRequest{Decision: "approve", Reviewer: "ana"}, http.StatusBadRequest},
		{"not escalated", running, escalationRequest{Decision: "accept", Reviewer: "ana"}, http.StatusConflict},
	}
	for _, c := range cases {
		if got, msg := validateEscalation(c.task, c.req); got != c.want {
			t.Errorf("%s: got %d (%s), want %d", c.name, got, msg, c.want)
		}
	}
}

func TestEscalatedRunID(t *testing.T) {
	if got := escalatedRunID(&domain.Task{ID: "c", ParentID: "p", LatestRunID: "run-c"}); got != "run-c" {
		t.Errorf("child: got %s", got)
	}
	if got := escalatedRunID(&domain.Task{ID: "p", IntegrationBranch: "forge/p", LatestRunID: "run-he"}); got != "run-p-final-review" {
		t.Errorf("parent: got %s, want its final-review run", got)
	}
}

func TestEscalationMessageCarriesDefectsAndGuidance(t *testing.T) {
	round := []*domain.Review{
		{Reviewer: "reviewer", Verdict: domain.VerdictFail, Defects: []domain.Defect{{Severity: "major", Description: "rounding is untested"}}},
		{Reviewer: "test-reviewer", Verdict: domain.VerdictPass},
	}
	msg := escalationMessage(round, escalationRequest{Reviewer: "ana", Guidance: "Add a Math.floor regression test."})
	for _, want := range []string{"## reviewer (fail)", "rounding is untested", "## Guidance from ana (human reviewer)", "Add a Math.floor regression test."} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
}
