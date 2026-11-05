package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
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
		ProjectID   string        `json:"projectId"`
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
	// A task works in one of its project's repositories (13.4).
	body.RepoPath = filepath.Clean(body.RepoPath)
	project, ok := h.memberProject(ctx, w, r, body.ProjectID, domain.RoleMember)
	if !ok {
		return
	}
	if !project.HasRepo(body.RepoPath) {
		http.Error(w, body.RepoPath+" is not a repository of project "+project.Name+"; a project admin can add it", http.StatusBadRequest)
		return
	}
	for _, pr := range project.Repos {
		if filepath.Clean(pr.Path) == body.RepoPath && body.TargetBranch == "" && body.BaseCommit == "" {
			body.TargetBranch = pr.DefaultTarget
		}
	}
	if body.BaseCommit == "" {
		body.BaseCommit = "HEAD"
	}
	if body.Budget.MaxSteps == 0 {
		b := body.Budget
		body.Budget = domain.Budget{MaxSteps: 20, MaxRetries: 2, WallClockMS: 1_800_000,
			MaxRevisions: b.MaxRevisions, MaxTokens: b.MaxTokens, MaxCostUSD: b.MaxCostUSD}
	}
	if body.Budget.MaxTokens < 0 || body.Budget.MaxCostUSD < 0 {
		http.Error(w, "budget caps cannot be negative", http.StatusBadRequest)
		return
	}
	// Caps the task does not set come from FORGE_TASK_MAX_TOKENS and
	// FORGE_TASK_MAX_COST_USD (13.6).
	if body.Budget.MaxTokens == 0 {
		body.Budget.MaxTokens = h.DefaultTaskLimits.MaxTokens
	}
	if body.Budget.MaxCostUSD == 0 {
		body.Budget.MaxCostUSD = h.DefaultTaskLimits.MaxCostUSD
	}

	// The task's work lives on its own branch from the start: resolve the
	// branch it is for, and create the parent branch at that branch's tip.
	target, tip, err := delivery.ResolveTarget(ctx, body.RepoPath, body.TargetBranch, body.BaseCommit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// With a remote, the work starts from what's there: pull requests merged
	// on the forge leave the local branch behind.
	tip, startNote, err := delivery.StartingPoint(ctx, body.RepoPath, target, tip)
	if errors.Is(err, delivery.ErrDiverged) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
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
		ProjectID:   project.ID,
		CreatedBy:   actorOf(r).Name,
		CreatedByID: actorOf(r).ID,

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
	reason := fmt.Sprintf("%s from %s at %s", task.IntegrationBranch, target, shortSHA(tip))
	if startNote != "" {
		reason += ": " + startNote
	}
	_ = h.Events.Append(ctx, &domain.Event{TaskID: task.ID, Type: "branch.created", Reason: reason, At: now})

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

	// Over a spend cap the task stays CREATED: raise the cap, then start it.
	if reason := h.overBudget(ctx, task); reason != "" {
		http.Error(w, (&BudgetError{Reason: reason}).Error(), http.StatusConflict)
		return
	}

	// A task blocked before any subtask was planned (a spend cap stopped its
	// head engineer, say) can be started again; a blocked task with subtasks
	// or a delivery has its own ways on.
	reason := "head-engineer requested"
	switch task.Status {
	case domain.StatusCreated:
	case domain.StatusBlocked:
		kids, err := h.Tasks.ListChildren(ctx, task.ID)
		if err != nil || len(kids) > 0 || task.Delivery != nil || task.ParentID != "" || len(task.Revisions) > 0 {
			http.Error(w, "only a task blocked before its subtasks were planned can be started again", http.StatusConflict)
			return
		}
		reason = "started again after decomposition was blocked"
	default:
		http.Error(w, "the task has already started (status "+string(task.Status)+")", http.StatusConflict)
		return
	}

	// Transition CREATED → DECOMPOSING
	if err := h.SM.Transition(ctx, task, domain.StatusDecomposing, reason); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	runID := "run-" + uuid.NewString()[:8]
	if code, err := h.startHeadEngineer(ctx, task, runID, task.BaseCommit, body.Provider, body.Model, ""); err != nil {
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, "head-engineer: "+err.Error())
		http.Error(w, err.Error(), code)
		return
	}

	// The task stays DECOMPOSING until handleHeadEngineerDone has created the
	// children and the integration branch; only then may the scheduler see it.
	writeJSON(w, http.StatusAccepted, map[string]string{"runId": runID, "status": string(domain.StatusDecomposing)})
}

// startHeadEngineer provisions a read-only workspace at base and starts the
// head engineer under runID. An empty prompt is the first decomposition;
// a follow-up passes its own. On error it answers the HTTP status to
// report; the caller decides what becomes of the task.
func (h *Handlers) startHeadEngineer(ctx context.Context, task *domain.Task, runID, base, provider, model, prompt string) (int, error) {
	he, ok := h.Registry.GetAgent("head-engineer")
	if !ok {
		return http.StatusInternalServerError, fmt.Errorf("head-engineer agent not registered")
	}
	sysPrompt, tools, err := agent.Compose(he, h.Registry.GetSkill)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("compose head-engineer: %w", err)
	}
	// It only reads and plans, so it doesn't wait for packages to install.
	ws, err := workspace.Provision(ctx, task.RepoPath, base, h.WorkerImage, runID)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("provision head-engineer: %w", err)
	}
	if prompt == "" {
		prompt = buildHeadEngineerTask(task, h.assignableAgents())
	}

	now := time.Now().UTC()
	run := &domain.WorkflowRun{
		ID: runID, TaskID: task.ID,
		Status:       domain.RunRunning,
		WorktreePath: ws.WorktreePath, ContainerName: ws.ContainerName,
		BaseCommit: base,
		Provenance: domain.Provenance{
			AgentDefinition: "head-engineer@v" + fmt.Sprint(he.Version),
			Model:           model, Provider: provider,
		},
		LastHeartbeat: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := h.Runs.Create(ctx, run); err != nil {
		workspace.Cleanup(ctx, ws, task.RepoPath)
		return http.StatusInternalServerError, fmt.Errorf("create head-engineer run: %w", err)
	}
	_ = h.Tasks.UpdateLatestRun(ctx, task.ID, runID, now)

	_, err = h.Adapter.StartRun(ctx, &adapter.StartRunRequest{
		RunID:          runID + "-he",
		CallbackRunID:  runID,
		ContainerName:  ws.ContainerName,
		WorktreePath:   ws.WorktreePath,
		Role:           "head-engineer",
		Task:           prompt,
		SystemPrompt:   sysPrompt,
		Tools:          tools,
		SubmissionTool: "submit_task_graph",
		CallbackURL:    h.SelfURL,
		Provider:       provider,
		Model:          model,
		MaxSteps:       h.PlannerMaxSteps * 2, // head engineer gets more room
		MaxRetries:     task.Budget.MaxRetries,
		WallClockMS:    h.PlannerWallClockMS * 2,
	})
	if err != nil {
		_ = h.Runs.UpdateStatus(ctx, runID, domain.RunBlocked, err.Error(), time.Now().UTC())
		workspace.Cleanup(ctx, ws, task.RepoPath)
		return http.StatusBadGateway, fmt.Errorf("head-engineer dispatch failed: %w", err)
	}
	return 0, nil
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
	var projects []string // nil: every project (instance admins)
	if pid := q.Get("projectId"); pid != "" {
		if _, ok := h.memberProject(r.Context(), w, r, pid, domain.RoleViewer); !ok {
			return
		}
		projects = []string{pid}
	} else {
		var err error
		if projects, err = h.visibleProjectIDs(r.Context(), actorOf(r)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	tasks, err := h.Tasks.List(r.Context(), status, projects, limit)
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
		Rationale string `json:"rationale,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	who := actorOf(r) // the decision is the signed-in reviewer's (13.3)

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
	if err := h.SM.Transition(ctx, task, to, who.Name+": "+body.Rationale); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	// Approving with open threads is allowed; the record keeps what was left.
	unresolved := h.unresolvedThreads(ctx, taskID)
	_ = h.Approvals.Create(ctx, &domain.Approval{
		TaskID:            taskID,
		RunID:             task.LatestRunID,
		Action:            action,
		Reviewer:          who.Name,
		ReviewerID:        who.ID,
		Rationale:         body.Rationale,
		UnresolvedThreads: unresolved,
		DecidedAt:         time.Now().UTC(),
	})
	_ = h.Events.Append(ctx, &domain.Event{
		RunID: task.LatestRunID, TaskID: taskID,
		Type: "approval." + action, Reason: body.Rationale,
		Actor: who.Name, ActorID: who.ID, At: time.Now().UTC(),
	})

	// Approval delivers: a pull request when the repository has a remote,
	// otherwise a local merge into the target branch.
	if to == domain.StatusApproved && canDeliver(task) {
		if err := h.startDelivery(ctx, task, "approved by "+who.Name); err != nil {
			log.Printf("delivery of %s not started: %v", taskID, err)
		} else {
			to = domain.StatusDelivering
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"taskId": taskID, "status": string(to), "unresolvedThreads": len(unresolved)})
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
// InspectRepo: GET /repos/inspect?path=&targetBranch=&requirement= — what
// creating a task there would do, before it exists: the repository's
// branches, the target the task would get, the parent branch it would
// create, and what approving it would do (the same preview as a task's).
func (h *Handlers) InspectRepo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	resp := map[string]any{}
	// Only a project's repositories can be inspected (13.4).
	project, ok := h.memberProject(ctx, w, r, q.Get("projectId"), domain.RoleViewer)
	if !ok {
		return
	}
	if !project.HasRepo(q.Get("path")) {
		http.Error(w, q.Get("path")+" is not a repository of project "+project.Name, http.StatusBadRequest)
		return
	}
	repo, err := delivery.InspectRepo(ctx, q.Get("path"))
	if err != nil {
		resp["error"] = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp["repo"] = repo
	target, _, err := delivery.ResolveTarget(ctx, repo.Path, q.Get("targetBranch"), "HEAD")
	if err != nil {
		resp["error"] = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	// The real name ends in the task's ID, which does not exist yet.
	branch := strings.TrimSuffix(delivery.ParentBranchName("task-x", q.Get("requirement")), "x") + "<task id>"
	resp["delivery"] = h.Delivery.Preview(ctx, &domain.Task{RepoPath: repo.Path, TargetBranch: target, IntegrationBranch: branch})
	writeJSON(w, http.StatusOK, resp)
}

// GetConfig: GET /config — what the console needs to start work.
func (h *Handlers) GetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"providers":       h.AllowedProviders,
		"defaultProvider": h.DefaultProvider,
		"defaultModel":    h.DefaultModel,
	})
}

func (h *Handlers) GetDelivery(w http.ResponseWriter, r *http.Request) {
	task, err := h.Tasks.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, h.Delivery.Preview(r.Context(), task))
}
