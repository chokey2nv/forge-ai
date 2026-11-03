package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/agent"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

// defaultMaxRevisions applies when neither the task's budget nor the
// control plane (MAX_REVISIONS) sets one.
const defaultMaxRevisions = 3

// followUpDiffLimit bounds the combined diff shown to the head engineer.
const followUpDiffLimit = 40000

func (h *Handlers) maxRevisions(task *domain.Task) int {
	switch {
	case task.Budget.MaxRevisions > 0:
		return task.Budget.MaxRevisions
	case h.MaxRevisions > 0:
		return h.MaxRevisions
	}
	return defaultMaxRevisions
}

// followUpAllowed: every follow-up aims at one more revision, so a task
// may be sent back maxRevisions-1 times (refused follow-ups count too:
// they cost model time all the same).
func (h *Handlers) followUpAllowed(task *domain.Task) bool {
	return len(task.FollowUps) < h.maxRevisions(task)-1
}

func (h *Handlers) budgetSpent(task *domain.Task) string {
	return fmt.Sprintf("the task's revision budget is spent (%d revisions, %d follow-ups so far); approve or reject it",
		h.maxRevisions(task), len(task.FollowUps))
}

// RequestChanges: POST /tasks/{id}/request-changes {reviewer, summary}.
// The third decision on a combined change: the head engineer plans
// follow-up work for the open threads and the summary, and the change comes
// back for review as the next revision.
func (h *Handlers) RequestChanges(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Reviewer string `json:"reviewer"`
		Summary  string `json:"summary,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	body.Summary = strings.TrimSpace(body.Summary)
	if strings.TrimSpace(body.Reviewer) == "" {
		http.Error(w, "reviewer is required", http.StatusBadRequest)
		return
	}
	task, err := h.Tasks.Get(ctx, r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !isParentReview(task) {
		http.Error(w, "changes can be requested on a parent task's combined change; reject a single task instead", http.StatusBadRequest)
		return
	}
	if task.Status != domain.StatusReviewPending {
		http.Error(w, "task is not in REVIEW_PENDING (current: "+string(task.Status)+")", http.StatusConflict)
		return
	}
	open := h.unresolvedThreads(ctx, task.ID)
	if len(open) == 0 && body.Summary == "" {
		http.Error(w, "say what to change: open a thread on the change or give a summary", http.StatusBadRequest)
		return
	}
	if !h.followUpAllowed(task) {
		http.Error(w, h.budgetSpent(task), http.StatusConflict)
		return
	}

	now := time.Now().UTC()
	_ = h.Approvals.Create(ctx, &domain.Approval{
		TaskID: task.ID, RunID: approvedReviewRunID(task), Action: "request_changes",
		Reviewer: body.Reviewer, Rationale: body.Summary, UnresolvedThreads: open, DecidedAt: now,
	})
	_ = h.Events.Append(ctx, &domain.Event{
		TaskID: task.ID, Type: "approval.request_changes",
		Reason: fmt.Sprintf("%d open thread(s); %s", len(open), body.Summary), At: now,
	})
	f, code, err := h.startFollowUp(context.WithoutCancel(ctx), task, domain.FollowUpRequestChanges, body.Reviewer, body.Summary, "")
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"taskId": task.ID, "status": string(task.Status), "followUp": f.N, "runId": f.RunID, "openThreads": len(open),
	})
}

// startFollowUp sends a parent's combined change back to the head engineer:
// it records the follow-up, moves the task to DECOMPOSING and starts the
// head engineer on the change as it stands. extra is more context for the
// prompt (an escalated final review's rejections).
func (h *Handlers) startFollowUp(ctx context.Context, task *domain.Task, kind, reviewer, summary, extra string) (*domain.FollowUp, int, error) {
	tip, err := workspace.ResolveCommit(ctx, task.RepoPath, task.IntegrationBranch)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("resolve the parent branch: %w", err)
	}
	diff, err := workspace.DiffCommits(task.RepoPath, task.IntegrationBaseCommit, tip)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	children, _ := h.Tasks.ListChildren(ctx, task.ID)
	threads := h.openThreads(ctx, task.ID)

	n := len(task.FollowUps) + 1
	f := domain.FollowUp{
		N: n, Kind: kind, Revision: len(task.Revisions), Reviewer: reviewer, Summary: summary,
		RunID: fmt.Sprintf("run-%s-revise-%d", task.ID, n), At: time.Now().UTC(),
	}
	prompt := buildFollowUpTask(task, &f, children, threads, diff, extra, h.assignableAgents())

	if err := h.SM.Transition(ctx, task, domain.StatusDecomposing, fmt.Sprintf("follow-up %d (%s) by %s", n, kind, reviewer)); err != nil {
		return nil, http.StatusConflict, err
	}
	if err := h.Tasks.AddFollowUp(ctx, task.ID, f, f.At); err != nil {
		h.refuseFollowUp(ctx, task, &f, "record follow-up: "+err.Error())
		return nil, http.StatusInternalServerError, err
	}
	task.FollowUps = append(task.FollowUps, f)

	provider, model := parentProvenance(ctx, h, task)
	if code, err := h.startHeadEngineer(ctx, task, f.RunID, tip, provider, model, prompt); err != nil {
		h.refuseFollowUp(ctx, task, &f, err.Error())
		return nil, code, err
	}
	return &task.FollowUps[len(task.FollowUps)-1], 0, nil
}

// refuseFollowUp records why a follow-up produced no work and hands the
// change back to the human: REVIEW_PENDING for requested changes, ESCALATED
// for a retried escalation. The revision under review is unchanged.
func (h *Handlers) refuseFollowUp(ctx context.Context, task *domain.Task, f *domain.FollowUp, reason string) {
	now := time.Now().UTC()
	_ = h.Tasks.SetFollowUpOutcome(ctx, task.ID, f.N, "refused: "+reason, now)
	back := domain.StatusReviewPending
	if f.Kind == domain.FollowUpEscalationRetry {
		back = domain.StatusEscalated
	}
	_ = h.Events.Append(ctx, &domain.Event{TaskID: task.ID, RunID: f.RunID, Type: "followup.refused", Reason: reason, At: now})
	_ = h.SM.Transition(ctx, task, back, fmt.Sprintf("follow-up %d refused: %s", f.N, reason))
}

// currentFollowUp is the follow-up run is working on, if it is one.
func currentFollowUp(task *domain.Task, runID string) *domain.FollowUp {
	for i := len(task.FollowUps) - 1; i >= 0; i-- {
		if task.FollowUps[i].RunID == runID {
			return &task.FollowUps[i]
		}
	}
	return nil
}

// openThreads are the threads a follow-up must handle, in the order the
// head engineer is shown them (T1, T2, …).
func (h *Handlers) openThreads(ctx context.Context, taskID string) []*domain.Thread {
	if h.Threads == nil {
		return nil
	}
	all, err := h.Threads.ListByTask(ctx, taskID)
	if err != nil {
		return nil
	}
	var out []*domain.Thread
	for _, t := range all {
		if t.Unresolved() {
			out = append(out, t)
		}
	}
	return out
}

// threadRefs resolves the head engineer's thread references: the short
// handle it was shown (T1) or the thread's ID.
func threadRefs(threads []*domain.Thread) map[string]string {
	refs := make(map[string]string, 2*len(threads))
	for i, t := range threads {
		refs[fmt.Sprintf("T%d", i+1)] = t.ID
		refs[t.ID] = t.ID
	}
	return refs
}

func buildFollowUpTask(task *domain.Task, f *domain.FollowUp, children []*domain.Task, threads []*domain.Thread, diff, extra string, agents []*agent.AgentDefinition) string {
	var b strings.Builder
	b.WriteString("# Follow-up on a reviewed change\n\n")
	if f.Kind == domain.FollowUpEscalationRetry {
		b.WriteString("The final review of the combined change below was escalated to a human, who sent it back for more work. ")
	} else {
		fmt.Fprintf(&b, "A human reviewed revision %d of the combined change below and asked for changes. ", f.Revision)
	}
	b.WriteString("Plan only the follow-up work: the subtasks already done are merged, and new subtasks start from the change as it stands.\n\n")
	b.WriteString("# Requirement\n\n")
	b.WriteString(task.Requirement)
	b.WriteString("\n\n# Acceptance criteria\n\n")
	for i, a := range task.Acceptance {
		fmt.Fprintf(&b, "%d. %s\n", i+1, a)
	}
	if len(children) > 0 {
		b.WriteString("\n# Subtasks already done\n\n")
		for _, c := range children {
			fmt.Fprintf(&b, "- %s (%s, %s)\n", c.Title, c.AssignedAgent, c.Status)
		}
	}
	if f.Summary != "" {
		fmt.Fprintf(&b, "\n# What %s asked for\n\n%s\n", f.Reviewer, f.Summary)
	}
	if extra != "" {
		b.WriteString("\n" + extra + "\n")
	}
	if len(threads) > 0 {
		b.WriteString("\n# Open review threads\n\n")
		for i, t := range threads {
			fmt.Fprintf(&b, "## T%d — %s (revision %d)\n\n", i+1, t.Where(), t.Revision)
			if t.Anchor != nil {
				fmt.Fprintf(&b, "On the line: `%s`\n\n", strings.TrimSpace(t.Anchor.LineText))
			}
			for _, c := range t.Comments {
				fmt.Fprintf(&b, "- **%s** (%s): %s\n", c.Author, c.Kind, c.Body)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n# The combined change so far\n\n```diff\n")
	if len(diff) > followUpDiffLimit {
		b.WriteString(diff[:followUpDiffLimit])
		b.WriteString("\n… (diff truncated; read the files for the rest)\n")
	} else {
		b.WriteString(diff)
	}
	b.WriteString("```\n\n# Available agents\n\n")
	for _, a := range agents {
		fmt.Fprintf(&b, "- **%s** — %s\n", a.Name, a.Description)
	}
	b.WriteString("\n# How to answer\n\nCall submit_task_graph with the follow-up subtasks (at least one). ")
	if len(threads) > 0 {
		b.WriteString("Every open thread must be handled: list it in the `addresses` of the subtask that deals with it, " +
			"or, when it needs no code change, answer it in `responses` as {\"thread\": \"T1\", \"body\": \"…\"}. " +
			"Refer to threads as T1, T2, …. A graph that leaves an open thread unhandled is refused. ")
	}
	b.WriteString("Use only the agents listed above.")
	return b.String()
}

// followUpGraph checks a follow-up graph's thread handling and returns, per
// submitted task ID, the threads it addresses, plus the responses by
// thread ID.
func followUpGraph(threads []*domain.Thread, addresses map[string][]string, responses []graphResponse) (byTask map[string][]string, answers map[string]string, err error) {
	refs := threadRefs(threads)
	handled := map[string]bool{}
	byTask = map[string][]string{}
	for taskID, list := range addresses {
		for _, ref := range list {
			id, ok := refs[strings.TrimSpace(ref)]
			if !ok {
				return nil, nil, fmt.Errorf("task %q addresses unknown thread %q", taskID, ref)
			}
			byTask[taskID] = append(byTask[taskID], id)
			handled[id] = true
		}
	}
	answers = map[string]string{}
	for _, r := range responses {
		id, ok := refs[strings.TrimSpace(r.Thread)]
		if !ok {
			return nil, nil, fmt.Errorf("response to unknown thread %q", r.Thread)
		}
		if strings.TrimSpace(r.Body) == "" {
			return nil, nil, fmt.Errorf("empty response to thread %q", r.Thread)
		}
		answers[id] = r.Body
		handled[id] = true
	}
	var missing []string
	for i, t := range threads {
		if !handled[t.ID] {
			missing = append(missing, fmt.Sprintf("T%d (%s)", i+1, t.Where()))
		}
	}
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("open thread(s) neither addressed by a subtask nor answered: %s", strings.Join(missing, ", "))
	}
	return byTask, answers, nil
}

type graphResponse struct {
	Thread string `json:"thread"`
	Body   string `json:"body"`
}

// threadBrief is what a follow-up subtask is told about the threads it addresses.
func threadBrief(threads []*domain.Thread, ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	byID := map[string]*domain.Thread{}
	for _, t := range threads {
		byID[t.ID] = t
	}
	var b strings.Builder
	b.WriteString("\n\nReview comments this subtask addresses:\n")
	for i, id := range ids {
		t := byID[id]
		if t == nil {
			continue
		}
		fmt.Fprintf(&b, "- T%d: %s", i+1, t.Where())
		if t.Anchor != nil {
			fmt.Fprintf(&b, " (`%s`)", strings.TrimSpace(t.Anchor.LineText))
		}
		for _, c := range t.Comments {
			fmt.Fprintf(&b, "\n  - %s: %s", c.Author, c.Body)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nWhen your change is done, reply on each of these threads with reply_to_thread (thread T1, T2, …): what you changed, or why you did not.\n")
	return b.String()
}
