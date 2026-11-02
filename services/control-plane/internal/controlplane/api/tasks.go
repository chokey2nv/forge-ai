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
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/agent"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/delivery"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
	"github.com/google/uuid"
)

func (h *Handlers) CreateTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body struct {
		Requirement string        `json:"requirement"`
		Acceptance  []string      `json:"acceptance"`
		RepoPath    string        `json:"repoPath"`
		BaseCommit  string        `json:"baseCommit"`
		Budget      domain.Budget `json:"budget"`
		DiffScope   []string      `json:"diffScope,omitempty"`
		// TargetBranch is where the work is delivered. Defaults to the branch
		// baseCommit names, or the repository's current branch for HEAD.
		TargetBranch string `json:"targetBranch,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Requirement == "" || body.RepoPath == "" {
		http.Error(w, "requirement and repoPath are required", http.StatusBadRequest)
		return
	}
	if body.BaseCommit == "" {
		body.BaseCommit = "HEAD"
	}
	if body.Budget.MaxSteps == 0 {
		body.Budget = domain.Budget{MaxSteps: 20, MaxRetries: 2, WallClockMS: 1_800_000}
	}

	// The task's work lives on its own branch from the start: resolve the
	// branch it is for, and create the parent branch at that branch's tip.
	target, tip, err := delivery.ResolveTarget(ctx, body.RepoPath, body.TargetBranch, body.BaseCommit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	task := &domain.Task{
		ID:          "task-" + uuid.NewString()[:8],
		Requirement: body.Requirement,
		Acceptance:  body.Acceptance,
		RepoPath:    body.RepoPath,
		BaseCommit:  tip,
		Status:      domain.StatusCreated,
		Budget:      body.Budget,
		DiffScope:   body.DiffScope,
		CreatedAt:   now,
		UpdatedAt:   now,

		TargetBranch:          target,
		IntegrationBaseCommit: tip,
	}
	task.IntegrationBranch = delivery.ParentBranchName(task.ID, task.Requirement)
	if err := delivery.CreateBranch(ctx, task.RepoPath, task.IntegrationBranch, tip); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.Tasks.Create(ctx, task); err != nil {
		_ = delivery.DeleteBranch(context.WithoutCancel(ctx), task.RepoPath, task.IntegrationBranch)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = h.Events.Append(ctx, &domain.Event{
		TaskID: task.ID, Type: "branch.created",
		Reason: fmt.Sprintf("%s from %s at %s", task.IntegrationBranch, target, shortSHA(tip)),
		At:     now,
	})

	writeJSON(w, http.StatusCreated, task)
}
func buildPlannerTask(task *domain.Task) string {
	var b strings.Builder
	b.WriteString(task.Requirement)
	b.WriteString("\n\nAcceptance criteria (ALL must be satisfied):\n")
	for i, a := range task.Acceptance {
		fmt.Fprintf(&b, "%d. %s\n", i+1, a)
	}
	b.WriteString("\nProduce an implementation plan: which files to change and in what order. Call submit_plan when done.")
	return b.String()
}

func (h *Handlers) GetTask(w http.ResponseWriter, r *http.Request) {
	t, err := h.Tasks.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, t)
}
func (h *Handlers) DispatchRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	taskID := r.PathValue("id")

	var body struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	task, err := h.Tasks.Get(ctx, taskID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Transition CREATED → DECOMPOSING
	if err := h.SM.Transition(ctx, task, domain.StatusDecomposing, "head-engineer requested"); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	// Compose head-engineer from registry.
	he, ok := h.Registry.GetAgent("head-engineer")
	if !ok {
		http.Error(w, "head-engineer agent not registered", http.StatusInternalServerError)
		return
	}
	sysPrompt, tools, err := agent.Compose(he, h.Registry.GetSkill)
	if err != nil {
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "compose head-engineer: "+err.Error())
		http.Error(w, "compose head-engineer: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Provision the decomposition workspace.
	runID := "run-" + uuid.NewString()[:8]
	ws, err := workspace.ProvisionWithSetup(ctx, task.RepoPath, task.BaseCommit, h.WorkerImage, runID, h.SetupTimeout)
	if err != nil {
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "provision head-engineer: "+err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build the task prompt including available agents.
	taskPrompt := buildHeadEngineerTask(task, h.assignableAgents())

	// Create the run record.
	run := &domain.WorkflowRun{
		ID: runID, TaskID: task.ID,
		Status:       domain.RunRunning,
		WorktreePath: ws.WorktreePath, ContainerName: ws.ContainerName,
		BaseCommit: task.BaseCommit,
		Provenance: domain.Provenance{
			AgentDefinition: "head-engineer@v" + fmt.Sprint(he.Version),
			Model:           body.Model, Provider: body.Provider,
		},
		LastHeartbeat: time.Now().UTC(),
		CreatedAt:     time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := h.Runs.Create(ctx, run); err != nil {
		workspace.Cleanup(ctx, ws, task.RepoPath)
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "create head-engineer run: "+err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = h.Tasks.UpdateLatestRun(ctx, task.ID, runID, time.Now().UTC())

	_, err = h.Adapter.StartRun(ctx, &adapter.StartRunRequest{
		RunID:          runID + "-he",
		CallbackRunID:  runID,
		ContainerName:  ws.ContainerName,
		WorktreePath:   ws.WorktreePath,
		Role:           "head-engineer",
		Task:           taskPrompt,
		SystemPrompt:   sysPrompt,
		Tools:          tools,
		SubmissionTool: "submit_task_graph",
		CallbackURL:    h.SelfURL,
		Provider:       body.Provider,
		Model:          body.Model,
		MaxSteps:       h.PlannerMaxSteps * 2, // head engineer gets more room
		MaxRetries:     task.Budget.MaxRetries,
		WallClockMS:    h.PlannerWallClockMS * 2,
	})
	if err != nil {
		_ = h.Runs.UpdateStatus(ctx, runID, domain.RunBlocked, err.Error(), time.Now().UTC())
		workspace.Cleanup(ctx, ws, task.RepoPath)
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "head-engineer dispatch failed")
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// The task stays DECOMPOSING until handleHeadEngineerDone has created the
	// children and the integration branch; only then may the scheduler see it.
	writeJSON(w, http.StatusAccepted, map[string]string{"runId": runID, "status": string(domain.StatusDecomposing)})
}

func buildHeadEngineerTask(task *domain.Task, agents []*agent.AgentDefinition) string {
	var b strings.Builder
	b.WriteString("# Requirement\n\n")
	b.WriteString(task.Requirement)
	b.WriteString("\n\n# Acceptance criteria\n\n")
	for i, a := range task.Acceptance {
		fmt.Fprintf(&b, "%d. %s\n", i+1, a)
	}
	b.WriteString("\n\n# Available agents\n\n")
	for _, a := range agents {
		fmt.Fprintf(&b, "- **%s** — %s\n", a.Name, a.Description)
	}
	b.WriteString("\nUse only the agents listed above. Call submit_task_graph when done.")
	return b.String()
}

func (h *Handlers) ListTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := domain.TaskStatus(q.Get("status"))
	limit := 100
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	tasks, err := h.Tasks.List(r.Context(), status, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (h *Handlers) ListTaskRuns(w http.ResponseWriter, r *http.Request) {
	// Simple: query all runs, filter by task in memory. Fine at Phase 6 scale.
	runs, err := h.Runs.ListByTask(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (h *Handlers) ApproveTask(w http.ResponseWriter, r *http.Request) { h.decideTask(w, r, "approve") }
func (h *Handlers) RejectTask(w http.ResponseWriter, r *http.Request)  { h.decideTask(w, r, "reject") }

func (h *Handlers) decideTask(w http.ResponseWriter, r *http.Request, action string) {
	ctx := r.Context()
	taskID := r.PathValue("id")

	var body struct {
		Reviewer  string `json:"reviewer"`
		Rationale string `json:"rationale,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Reviewer == "" {
		http.Error(w, "reviewer is required", http.StatusBadRequest)
		return
	}

	task, err := h.Tasks.Get(ctx, taskID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if task.Status != domain.StatusReviewPending {
		http.Error(w, "task is not in REVIEW_PENDING (current: "+string(task.Status)+")", http.StatusConflict)
		return
	}

	var to domain.TaskStatus
	if action == "approve" {
		to = domain.StatusApproved
	} else {
		to = domain.StatusRejected
	}
	if err := h.SM.Transition(ctx, task, to, body.Reviewer+": "+body.Rationale); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	_ = h.Approvals.Create(ctx, &domain.Approval{
		TaskID:    taskID,
		RunID:     task.LatestRunID,
		Action:    action,
		Reviewer:  body.Reviewer,
		Rationale: body.Rationale,
		DecidedAt: time.Now().UTC(),
	})
	_ = h.Events.Append(ctx, &domain.Event{
		RunID: task.LatestRunID, TaskID: taskID,
		Type: "approval." + action, Reason: body.Rationale,
		At: time.Now().UTC(),
	})

	// Approval delivers: a pull request when the repository has a remote,
	// otherwise a local merge into the target branch.
	if to == domain.StatusApproved && canDeliver(task) {
		if err := h.startDelivery(ctx, task, "approved by "+body.Reviewer); err != nil {
			log.Printf("delivery of %s not started: %v", taskID, err)
		} else {
			to = domain.StatusDelivering
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"taskId": taskID, "status": string(to)})
}

func (h *Handlers) ListApprovals(w http.ResponseWriter, r *http.Request) {
	as, err := h.Approvals.ListByTask(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, as)
}

// ListTaskChildren serves a decomposed parent's subtasks, oldest first.
func (h *Handlers) ListTaskChildren(w http.ResponseWriter, r *http.Request) {
	children, err := h.Tasks.ListChildren(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, children)
}

// ListTaskEvents serves a task's events, oldest first, including its state
// transitions (which carry no run ID).
func (h *Handlers) ListTaskEvents(w http.ResponseWriter, r *http.Request) {
	events, err := h.Events.ListByTask(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// GetDelivery previews what approving the task will do: open a pull request
// with gh when the repository has a remote, otherwise merge locally.
func (h *Handlers) GetDelivery(w http.ResponseWriter, r *http.Request) {
	task, err := h.Tasks.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, h.Delivery.Preview(r.Context(), task))
}
