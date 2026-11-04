package api

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/adapter"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
)

type memSpend struct {
	m map[string]*domain.SpendRecord
}

func (s *memSpend) Record(_ context.Context, r *domain.SpendRecord) error {
	cp := *r
	s.m[r.ID] = &cp
	return nil
}
func (s *memSpend) Total(_ context.Context, field, value string) (domain.Spend, error) {
	var t domain.Spend
	for _, r := range s.m {
		v := map[string]string{"rootTaskId": r.RootTaskID, "taskId": r.TaskID, "projectId": r.ProjectID}[field]
		if v == value {
			t.InputTokens += r.Spend.InputTokens
			t.OutputTokens += r.Spend.OutputTokens
			t.CostUSD += r.Spend.CostUSD
			t.UnpricedTokens += r.Spend.UnpricedTokens
		}
	}
	return t, nil
}

type spendRuns struct {
	store.RunRepo
	m map[string]*domain.WorkflowRun
}

func (r *spendRuns) Get(_ context.Context, id string) (*domain.WorkflowRun, error) {
	if x, ok := r.m[id]; ok {
		return x, nil
	}
	return nil, errors.New("no")
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSpendPricing(t *testing.T) {
	prices, err := ParsePrices("local-model=0/0, claude-opus-5-5=3/15")
	if err != nil {
		t.Fatal(err)
	}
	s := spendOf(prices, "claude-sonnet-5-5", []domain.Usage{{InputTokens: 1_000_000}, {InputTokens: 500_000, OutputTokens: 100_000}})
	if s.Tokens() != 1_600_000 || !near(s.CostUSD, 2+1+1) || s.UnpricedTokens != 0 {
		t.Errorf("sonnet: %+v", s)
	}
	if s := spendOf(prices, "claude-opus-5-5", []domain.Usage{{InputTokens: 1_000_000, OutputTokens: 1_000_000}}); !near(s.CostUSD, 18) {
		t.Errorf("an override replaces the default price: %+v", s)
	}
	// Haiku 5.5 costs more for a call whose prompt is over 100K tokens.
	s = spendOf(prices, "claude-haiku-5-5", []domain.Usage{{InputTokens: 100_000, OutputTokens: 10_000}, {InputTokens: 200_000, OutputTokens: 10_000}})
	if !near(s.CostUSD, 0.01+0.005+0.1+0.025) {
		t.Errorf("haiku long context: %v", s.CostUSD)
	}
	s = spendOf(prices, "qwen3-coder:30b", []domain.Usage{{InputTokens: 700, OutputTokens: 300}})
	if s.Tokens() != 1000 || s.UnpricedTokens != 1000 || s.CostUSD != 0 {
		t.Errorf("an unpriced model counts tokens only: %+v", s)
	}
	for _, bad := range []string{"m=1", "m=x/1", "=1/2/3", "m=-1/2"} {
		if _, err := ParsePrices(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if (domain.Limits{}).Exceeded(domain.Spend{InputTokens: 1e9}, "x") != "" {
		t.Error("no caps, never exceeded")
	}
	l := domain.Limits{MaxTokens: 1000, MaxCostUSD: 0.5}
	if r := l.Exceeded(domain.Spend{InputTokens: 600, OutputTokens: 400}, "task t1"); r != "token budget reached: task t1 used 1000 of 1000 tokens" {
		t.Error(r)
	}
	if r := l.Exceeded(domain.Spend{InputTokens: 10, CostUSD: 0.5}, "project app"); !strings.HasPrefix(r, "cost budget reached: project app spent $0.5000 of $0.50") {
		t.Error(r)
	}
}

func TestSpendCaps(t *testing.T) {
	ctx := context.Background()
	prices, _ := ParsePrices("")
	spend := &memSpend{m: map[string]*domain.SpendRecord{}}
	projects := &memProjects{m: map[string]*domain.Project{"prj-a": {ID: "prj-a", Name: "app"}, "prj-b": {ID: "prj-b", Name: "lib"}}}
	tasks := &projTasks{m: map[string]*domain.Task{
		"root": {ID: "root", ProjectID: "prj-a", Budget: domain.Budget{MaxTokens: 10_000}},
		"kid":  {ID: "kid", ParentID: "root", ProjectID: "prj-a"},
		"solo": {ID: "solo", ProjectID: "prj-a"},
		"far":  {ID: "far", ProjectID: "prj-b"},
	}}
	runs := &spendRuns{m: map[string]*domain.WorkflowRun{
		"run-kid": {ID: "run-kid", TaskID: "kid"}, "run-solo": {ID: "run-solo", TaskID: "solo"}, "run-far": {ID: "run-far", TaskID: "far"},
	}}
	h := &Handlers{Spend: spend, Prices: prices, Projects: projects, Tasks: tasks, Runs: runs}
	use := func(inv, run, task string, in, out int) string {
		return h.recordSpend(ctx, runs.m[run], tasks.m[task], "implementer",
			&SpendReport{Invocation: inv, Model: "claude-sonnet-5-5", Usage: []domain.Usage{{InputTokens: in, OutputTokens: out}}})
	}

	if r := use("i1", "run-kid", "kid", 4000, 1000); r != "" {
		t.Errorf("under the cap: %s", r)
	}
	if rec := spend.m["i1"]; rec.RootTaskID != "root" || rec.ProjectID != "prj-a" || !near(rec.Spend.CostUSD, 0.008+0.01) {
		t.Errorf("a subtask's spend counts towards its parent: %+v", rec)
	}
	// The same invocation reports again with more calls: its record is replaced, not added to.
	h.recordSpend(ctx, runs.m["run-kid"], tasks.m["kid"], "implementer", &SpendReport{Invocation: "i1", Model: "claude-sonnet-5-5",
		Usage: []domain.Usage{{InputTokens: 4000, OutputTokens: 1000}, {InputTokens: 4000, OutputTokens: 500}}})
	if s, _ := spend.Total(ctx, "rootTaskId", "root"); s.Tokens() != 9500 {
		t.Errorf("records are an invocation's totals: %d", s.Tokens())
	}
	// A reviewer, a second invocation, crosses the parent's cap.
	if r := use("i2", "run-kid", "kid", 400, 100); r != "token budget reached: task root used 10000 of 10000 tokens" {
		t.Errorf("the parent's cap: %q", r)
	}
	if r := use("i3", "run-solo", "solo", 50_000, 0); r != "" {
		t.Errorf("another task in the project has no cap of its own: %q", r)
	}
	err := h.BudgetGate(ctx, &adapter.StartRunRequest{RunID: "run-kid-r", CallbackRunID: "run-kid"})
	var be *BudgetError
	if !errors.As(err, &be) || !strings.Contains(err.Error(), "raise the cap, then retry") {
		t.Errorf("no agent starts for a task over its cap: %v", err)
	}
	if err := h.BudgetGate(ctx, &adapter.StartRunRequest{RunID: "run-solo"}); err != nil {
		t.Errorf("a task under every cap starts: %v", err)
	}

	// The project's cap covers all its tasks: 60,000 tokens and $0.13 so far.
	projects.m["prj-a"].Budget = domain.Limits{MaxCostUSD: 0.10}
	if r := h.overBudget(ctx, tasks.m["solo"]); !strings.HasPrefix(r, "cost budget reached: project app spent $0.13") {
		t.Errorf("project cost cap: %q", r)
	}
	if r := h.overBudget(ctx, tasks.m["far"]); r != "" {
		t.Errorf("another project's cap does not apply: %q", r)
	}
	// Raising the caps lets work start again.
	tasks.m["root"].Budget.MaxTokens = 20_000
	projects.m["prj-a"].Budget = domain.Limits{MaxTokens: 100_000}
	if err := h.BudgetGate(ctx, &adapter.StartRunRequest{RunID: "run-kid"}); err != nil {
		t.Errorf("after raising the caps: %v", err)
	}
	if r := use("", "run-kid", "kid", 1e6, 0); r != "" || len(spend.m) != 3 {
		t.Error("a report without an invocation id is ignored")
	}
}
