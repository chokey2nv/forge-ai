package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/adapter"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

// Spend caps (Phase 13.6). The adapter reports each agent invocation's model
// usage with every checkpoint and completion; the control plane prices it,
// keeps one record per invocation, and answers a checkpoint with "stop" once
// the task's or the project's cap is reached. A run that would start over a
// cap is refused before it starts.

// Price is what a model costs, in US dollars per million tokens.
type Price struct {
	In, Out float64
	// Above LongAbove input tokens in one call, LongIn and LongOut apply.
	LongAbove       int
	LongIn, LongOut float64
}

// DefaultPrices are Anthropic's first-party API prices (October 2026).
// FORGE_MODEL_PRICES adds to or overrides them. Models without a price
// (local ones, for example) count towards token caps only.
var DefaultPrices = map[string]Price{
	"claude-fable-5-1":  {In: 10, Out: 50},
	"claude-fable-5":    {In: 10, Out: 50},
	"claude-opus-5-5":   {In: 4, Out: 20},
	"claude-opus-5":     {In: 5, Out: 25},
	"claude-opus-4-8":   {In: 5, Out: 25},
	"claude-opus-4-7":   {In: 5, Out: 25},
	"claude-opus-4-6":   {In: 5, Out: 25},
	"claude-sonnet-5-5": {In: 2, Out: 10},
	"claude-sonnet-5":   {In: 2, Out: 10},
	"claude-sonnet-4-6": {In: 3, Out: 15},
	"claude-haiku-5-5":  {In: 0.10, Out: 0.50, LongAbove: 100_000, LongIn: 0.50, LongOut: 2.50},
	"claude-haiku-4-5":  {In: 1, Out: 5},
}

// ParsePrices reads FORGE_MODEL_PRICES: "model=in/out,model=in/out", dollars
// per million input and output tokens, on top of DefaultPrices.
func ParsePrices(spec string) (map[string]Price, error) {
	out := make(map[string]Price, len(DefaultPrices))
	for k, v := range DefaultPrices {
		out[k] = v
	}
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		model, rest, ok := strings.Cut(item, "=")
		in, outS, ok2 := strings.Cut(rest, "/")
		pi, err1 := strconv.ParseFloat(strings.TrimSpace(in), 64)
		po, err2 := strconv.ParseFloat(strings.TrimSpace(outS), 64)
		if !ok || !ok2 || err1 != nil || err2 != nil || pi < 0 || po < 0 {
			return nil, fmt.Errorf("FORGE_MODEL_PRICES: %q is not model=in/out", item)
		}
		out[strings.TrimSpace(model)] = Price{In: pi, Out: po}
	}
	return out, nil
}

// spendOf prices one invocation's model calls.
func spendOf(prices map[string]Price, model string, usage []domain.Usage) domain.Spend {
	var s domain.Spend
	p, priced := prices[model]
	for _, u := range usage {
		in, out := int64(u.InputTokens), int64(u.OutputTokens)
		s.InputTokens += in
		s.OutputTokens += out
		if !priced {
			s.UnpricedTokens += in + out
			continue
		}
		pin, pout := p.In, p.Out
		if p.LongAbove > 0 && u.InputTokens > p.LongAbove {
			pin, pout = p.LongIn, p.LongOut
		}
		s.CostUSD += (float64(in)*pin + float64(out)*pout) / 1e6
	}
	return s
}

// SpendReport is the adapter's account of one invocation so far.
type SpendReport struct {
	Invocation string         `json:"invocation"`
	Model      string         `json:"model"`
	Usage      []domain.Usage `json:"usage"` // this invocation's calls only, not a resumed history's
}

// rootTask is the top-level task whose budget covers t.
func (h *Handlers) rootTask(ctx context.Context, t *domain.Task) *domain.Task {
	for i := 0; t.ParentID != "" && i < 8; i++ {
		p, err := h.Tasks.Get(ctx, t.ParentID)
		if err != nil {
			break
		}
		t = p
	}
	return t
}

// recordSpend stores an invocation's usage and returns the cap it has
// reached, if any.
func (h *Handlers) recordSpend(ctx context.Context, run *domain.WorkflowRun, task *domain.Task, role string, rep *SpendReport) string {
	if h.Spend == nil || rep == nil || rep.Invocation == "" || task == nil {
		return ""
	}
	root := h.rootTask(ctx, task)
	rec := &domain.SpendRecord{
		ID: rep.Invocation, RunID: run.ID, TaskID: task.ID, RootTaskID: root.ID, ProjectID: root.ProjectID,
		Role: role, Model: rep.Model, Calls: len(rep.Usage), Spend: spendOf(h.Prices, rep.Model, rep.Usage),
		UpdatedAt: time.Now().UTC(),
	}
	if err := h.Spend.Record(ctx, rec); err != nil {
		log.Printf("spend %s: %v", run.ID, err)
		return ""
	}
	return h.overBudget(ctx, root)
}

// overBudget names the cap task's top-level task or its project has
// reached, or "".
func (h *Handlers) overBudget(ctx context.Context, task *domain.Task) string {
	if h.Spend == nil {
		return ""
	}
	root := h.rootTask(ctx, task)
	if l := root.Budget.Limits(); l != (domain.Limits{}) {
		if s, err := h.Spend.Total(ctx, "rootTaskId", root.ID); err == nil {
			if reason := l.Exceeded(s, "task "+root.ID); reason != "" {
				return reason
			}
		}
	}
	if root.ProjectID == "" || h.Projects == nil {
		return ""
	}
	p, err := h.Projects.Get(ctx, root.ProjectID)
	if err != nil || p.Budget == (domain.Limits{}) {
		return ""
	}
	if s, err := h.Spend.Total(ctx, "projectId", p.ID); err == nil {
		return p.Budget.Exceeded(s, "project "+p.Name)
	}
	return ""
}

// BudgetError refuses to start an agent: a spend cap is reached.
type BudgetError struct{ Reason string }

func (e *BudgetError) Error() string { return e.Reason + "; raise the cap, then retry" }

// BudgetGate is the adapter client's gate: no agent starts for a task over
// its or its project's cap.
func (h *Handlers) BudgetGate(ctx context.Context, req *adapter.StartRunRequest) error {
	id := req.CallbackRunID
	if id == "" {
		id = req.RunID
	}
	run, err := h.Runs.Get(ctx, id)
	if err != nil {
		return nil
	}
	task, err := h.Tasks.Get(ctx, run.TaskID)
	if err != nil {
		return nil
	}
	if reason := h.overBudget(ctx, task); reason != "" {
		return &BudgetError{Reason: reason}
	}
	return nil
}

type spendView struct {
	Spend  domain.Spend  `json:"spend"`
	Limits domain.Limits `json:"limits"`
}

// GetTaskSpend: the spend of the task's top-level task (with its subtasks)
// and of its project, against their caps.
func (h *Handlers) GetTaskSpend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	task, err := h.Tasks.Get(ctx, r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	root := h.rootTask(ctx, task)
	out := map[string]any{"rootTaskId": root.ID}
	s, err := h.Spend.Total(ctx, "rootTaskId", root.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out["task"] = spendView{Spend: s, Limits: root.Budget.Limits()}
	if root.ID != task.ID {
		if own, err := h.Spend.Total(ctx, "taskId", task.ID); err == nil {
			out["subtask"] = own
		}
	}
	if p, err := h.Projects.Get(ctx, root.ProjectID); err == nil {
		if ps, err := h.Spend.Total(ctx, "projectId", p.ID); err == nil {
			out["project"] = spendView{Spend: ps, Limits: p.Budget}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handlers) GetProjectSpend(w http.ResponseWriter, r *http.Request) {
	p := projectFrom(r.Context())
	s, err := h.Spend.Total(r.Context(), "projectId", p.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, spendView{Spend: s, Limits: p.Budget})
}

func decodeLimits(w http.ResponseWriter, r *http.Request) (domain.Limits, bool) {
	var l domain.Limits
	if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return l, false
	}
	if l.MaxTokens < 0 || l.MaxCostUSD < 0 {
		http.Error(w, "caps cannot be negative (0 removes one)", http.StatusBadRequest)
		return l, false
	}
	return l, true
}

// SetTaskBudget changes a top-level task's token and cost caps. Raising them
// does not restart anything: retry or resume what the cap blocked.
// SetTaskMaxSteps changes how many steps each of a task's agents gets, for the
// task and its subtasks; a resume then gives that many more.
func (h *Handlers) SetTaskMaxSteps(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	task, err := h.Tasks.Get(ctx, r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if task.ParentID != "" {
		http.Error(w, "a subtask shares its parent's budget: set it on "+task.ParentID, http.StatusBadRequest)
		return
	}
	var body struct {
		MaxSteps int `json:"maxSteps"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.MaxSteps < 1 {
		http.Error(w, "maxSteps must be at least 1", http.StatusBadRequest)
		return
	}
	if err := h.Tasks.SetMaxSteps(ctx, task.ID, body.MaxSteps, time.Now().UTC()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a := actorOf(r)
	_ = h.Events.Append(ctx, &domain.Event{TaskID: task.ID, Type: "budget.changed", Payload: body,
		Actor: a.Name, ActorID: a.ID, At: time.Now().UTC()})
	writeJSON(w, http.StatusOK, body)
}

func (h *Handlers) SetTaskBudget(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	task, err := h.Tasks.Get(ctx, r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if task.ParentID != "" {
		http.Error(w, "a subtask shares its parent's budget: set it on "+task.ParentID, http.StatusBadRequest)
		return
	}
	l, ok := decodeLimits(w, r)
	if !ok {
		return
	}
	if err := h.Tasks.SetSpendLimits(ctx, task.ID, l, time.Now().UTC()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a := actorOf(r)
	_ = h.Events.Append(ctx, &domain.Event{TaskID: task.ID, Type: "budget.changed", Payload: l,
		Actor: a.Name, ActorID: a.ID, At: time.Now().UTC()})
	writeJSON(w, http.StatusOK, l)
}

func (h *Handlers) SetProjectBudget(w http.ResponseWriter, r *http.Request) {
	p := projectFrom(r.Context())
	l, ok := decodeLimits(w, r)
	if !ok {
		return
	}
	if err := h.Projects.SetBudget(r.Context(), p.ID, l); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, l)
}
