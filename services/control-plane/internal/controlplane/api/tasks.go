package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
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
