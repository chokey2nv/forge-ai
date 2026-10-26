package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/adapter"
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

	now := time.Now().UTC()
	task := &domain.Task{
		ID:          "task-" + uuid.NewString()[:8],
		Requirement: body.Requirement,
		Acceptance:  body.Acceptance,
		RepoPath:    body.RepoPath,
		BaseCommit:  body.BaseCommit,
		Status:      domain.StatusCreated,
		Budget:      body.Budget,
		DiffScope:   body.DiffScope,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := h.Tasks.Create(ctx, task); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

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

	// Provider allow-list (Phase 2 pattern; becomes Project.allowed_providers in Phase 6)
	if !slices.Contains(h.AllowedProviders, body.Provider) {
		http.Error(w, "provider not allowed: "+body.Provider, http.StatusForbidden)
		return
	}

	// Transition CREATED → PLANNED → DISPATCHED
	if err := h.SM.Transition(ctx, task, domain.StatusPlanned, "run requested"); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	runID := "run-" + uuid.NewString()[:8]
	// ws, err := workspace.Provision(ctx, task.RepoPath, task.BaseCommit, h.WorkerImage, runID)
	// if err != nil {
	// 	http.Error(w, err.Error(), http.StatusInternalServerError)
	// 	return
	// }
	ws, err := workspace.ProvisionWithSetup(ctx, task.RepoPath, task.BaseCommit, h.WorkerImage, runID, h.SetupTimeout)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	run := &domain.WorkflowRun{
		ID:            runID,
		TaskID:        task.ID,
		Status:        domain.RunDispatched,
		WorktreePath:  ws.WorktreePath,
		ContainerName: ws.ContainerName,
		BaseCommit:    task.BaseCommit,
		Provenance:    domain.Provenance{AgentDefinition: "implementer@v1", Model: body.Model, Provider: body.Provider},
		LastHeartbeat: time.Now().UTC(),
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := h.Runs.Create(ctx, run); err != nil {
		workspace.Cleanup(ctx, ws, task.RepoPath)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	_ = h.Tasks.UpdateLatestRun(ctx, task.ID, runID, time.Now().UTC())

	if h.PlannerEnabled {
		if _, err := h.Adapter.StartRun(ctx, &adapter.StartRunRequest{
			RunID:         runID + "-plan",
			ContainerName: ws.ContainerName,
			WorktreePath:  ws.WorktreePath,
			Role:          "planner",
			Task:          buildPlannerTask(task),
			CallbackURL:   h.SelfURL,
			Provider:      body.Provider,
			Model:         body.Model,
			MaxSteps:      h.PlannerMaxSteps,
			MaxRetries:    task.Budget.MaxRetries,
			WallClockMS:   h.PlannerWallClockMS,
		}); err != nil {
			_ = h.Runs.UpdateStatus(ctx, runID, domain.RunBlocked, err.Error(), time.Now().UTC())
			_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "planner invocation failed")
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	} else {
		if err := h.SM.Transition(ctx, task, domain.StatusRunning, "adapter accepted"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h.invokeImplementer(run, task, task.Requirement)
	}

	if _, err := h.Adapter.StartRun(ctx, &adapter.StartRunRequest{
		RunID:         runID,
		ContainerName: ws.ContainerName,
		WorktreePath:  ws.WorktreePath,
		Task:          task.Requirement,
		SystemPrompt:  h.SystemPrompt,
		CallbackURL:   h.SelfURL,
		Provider:      body.Provider,
		Model:         body.Model,
		MaxSteps:      task.Budget.MaxSteps,
		MaxRetries:    task.Budget.MaxRetries,
		WallClockMS:   task.Budget.WallClockMS,
	}); err != nil {
		_ = h.Runs.UpdateStatus(ctx, runID, domain.RunBlocked, err.Error(), time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, err.Error())
		workspace.Cleanup(ctx, ws, task.RepoPath)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	if err := h.SM.Transition(ctx, task, domain.StatusDispatched, "adapter accepted"); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"runId": runID, "status": string(domain.StatusDispatched)})
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
