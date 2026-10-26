package main

import (
	"context"

	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/adapter"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/agent"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/api"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/orchestration"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/runner"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/statemachine"
	mongo_store "github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store/mongo"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/verification"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func main() {
	// ---- config ----
	addr := envOr("CONTROL_PLANE_ADDR", ":8080")
	selfURL := envOr("CONTROL_PLANE_SELF_URL", "http://localhost:8080")
	adapterURL := envOr("ADAPTER_URL", "http://localhost:8081")
	workerImage := envOr("WORKER_IMAGE", "forge-worker:dev")
	mongoURI := envOr("MONGODB_URI", "mongodb://127.0.0.1:27017")
	mongoDB := envOr("MONGODB_DATABASE", "forge_ai")
	agentsDir := envOr("AGENTS_DIR", "agents")
	skillsDir := envOr("SKILLS_DIR", "skills")

	staleAfter := parseDuration(envOr("STALE_AFTER", "60s"), 60*time.Second)
	allowed := strings.Split(envOr("FORGE_ALLOWED_PROVIDERS", "ollama"), ",")
	for i := range allowed {
		allowed[i] = strings.TrimSpace(allowed[i])
	}

	// ---- mongo ----
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mc, err := mongo.Connect(options.Client().ApplyURI(mongoURI)) // ctx, options.Client().ApplyURI(mongoURI)
	if err != nil {
		log.Fatalf("mongo connect: %v", err)
	}
	defer func() { _ = mc.Disconnect(context.Background()) }()

	if err := mc.Ping(ctx, nil); err != nil {
		log.Fatalf("mongo ping: %v", err)
	}
	db := mc.Database(mongoDB)

	registryColl := db.Collection("agentRegistry")
	registry := agent.NewRegistry(agentsDir, skillsDir, registryColl)
	if err := registry.Load(ctx); err != nil {
		log.Printf("agent registry load failed: %v — starting with empty registry", err)
		// Don't fatal — a broken agent file shouldn't take down the whole control plane.
		// Failures surface via /agents/health.
	} else {
		log.Printf("agent registry loaded: %d agents, %d skills", len(registry.ListAgents()), len(registry.ListSkills()))
	}

	// ---- store ----
	taskRepo := mongo_store.NewTaskRepo(db)
	runRepo := mongo_store.NewRunRepo(db)
	eventRepo := mongo_store.NewEventRepo(db)
	verificationRepo := mongo_store.NewVerificationRepo(db)
	approvalRepo := mongo_store.NewApprovalRepo(db)
	evalResultRepo := mongo_store.NewEvalResultRepo(db)

	// ---- services ----
	sm := &statemachine.Service{Tasks: taskRepo, Events: eventRepo}
	adapterClient := &adapter.Client{BaseURL: adapterURL}

	// ---- handler & router ----
	verifier := verification.NewRunner()

	h := api.NewHandlers(
		taskRepo, runRepo, eventRepo, verificationRepo,
		approvalRepo, evalResultRepo,
		sm, adapterClient, verifier,
		workerImage, selfURL, defaultImplementerSystemPrompt(),
		allowed,
		parseInt(envOr("MAX_VERIFICATION_ATTEMPTS", "2"), 2),
		parseDuration(envOr("VERIFICATION_SETUP_TIMEOUT_SECONDS", "120s"), 120*time.Second),
	)

	h.Registry = registry
	h.PlannerEnabled = parseBool(envOr("PLANNER_ENABLED", "true"))
	h.ReviewerEnabled = parseBool(envOr("REVIEWER_ENABLED", "true"))
	h.PlannerMaxSteps = parseInt(envOr("PLANNER_MAX_STEPS", "10"), 10)
	h.PlannerWallClockMS = int(parseDuration(envOr("PLANNER_TIMEOUT", "120s"), 120*time.Second) / time.Millisecond)
	h.ReviewerMaxSteps = parseInt(envOr("REVIEWER_MAX_STEPS", "15"), 15)
	h.ReviewerWallClockMS = int(parseDuration(envOr("REVIEWER_TIMEOUT", "180s"), 180*time.Second) / time.Millisecond)

	integrator := &orchestration.Integrator{
		Tasks:  taskRepo,
		Events: eventRepo,
		SM:     sm,
	}
	h.Integrator = integrator

	orchestration.ParentVerificationHook = h.RunParentVerification

	scheduler := &orchestration.Scheduler{
		Tasks:               taskRepo,
		Runs:                runRepo,
		Events:              eventRepo,
		SM:                  sm,
		Registry:            registry,
		Adapter:             adapterClient,
		WorkerImage:         workerImage,
		SelfURL:             selfURL,
		SetupTimeoutSeconds: int(h.SetupTimeout.Seconds()),
		DefaultProvider:     envOr("DEFAULT_PROVIDER", "ollama"),
		DefaultModel:        envOr("DEFAULT_MODEL", os.Getenv("MODEL_NAME")),
	}
	go scheduler.Run(ctx)

	mux := http.NewServeMux()

	// tasks
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("GET  /agents", h.ListAgents)
	mux.HandleFunc("GET  /agents/health", h.AgentHealth)
	mux.HandleFunc("GET  /agents/{name}", h.GetAgent)
	mux.HandleFunc("POST /agents/reload", h.ReloadAgents)
	mux.HandleFunc("GET  /skills", h.ListSkills)
	mux.HandleFunc("GET  /skills/{name}", h.GetSkill)

	mux.HandleFunc("POST /tasks", h.CreateTask)
	mux.HandleFunc("GET /tasks/{id}", h.GetTask)
	mux.HandleFunc("POST /tasks/{id}/runs", h.DispatchRun)
	mux.HandleFunc("GET  /tasks", h.ListTasks)
	mux.HandleFunc("GET  /tasks/{id}/runs", h.ListTaskRuns)
	mux.HandleFunc("GET  /tasks/{id}/approvals", h.ListApprovals)
	mux.HandleFunc("POST /tasks/{id}/approve", h.ApproveTask)
	mux.HandleFunc("POST /tasks/{id}/reject", h.RejectTask)

	mux.HandleFunc("POST /evals/results", h.RecordEval)
	mux.HandleFunc("GET  /evals/results", h.ListEvals)
	mux.HandleFunc("GET  /evals/results/{id}", h.GetEval)

	// runs
	mux.HandleFunc("GET /runs/{id}/verification", h.GetVerification)
	mux.HandleFunc("GET /runs/{id}", h.GetRun)
	mux.HandleFunc("GET /runs/{id}/events", h.ListEvents)
	mux.HandleFunc("GET /runs/{id}/diff", h.GetDiff)
	mux.HandleFunc("POST /runs/{id}/resume", h.ResumeRun)

	// callbacks from adapter
	mux.HandleFunc("POST /runs/{id}/checkpoint", h.RecordCheckpoint)
	mux.HandleFunc("POST /runs/{id}/complete", h.RecordComplete)

	// ---- watcher ----
	watcher := runner.NewWatcher(runRepo, taskRepo, sm, staleAfter)
	go watcher.Run(ctx)

	// ---- server ----
	srv := &http.Server{Addr: addr, Handler: api.WithCORS(mux)}
	go func() {
		log.Printf("control-plane listening on %s (adapter=%s, worker=%s, allowed=%v)", addr, adapterURL, workerImage, allowed)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	// ---- graceful shutdown ----
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("shutting down...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	cancel()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDuration(s string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		log.Printf("invalid duration %q, using %s", s, fallback)
		return fallback
	}
	return d
}

func defaultImplementerSystemPrompt() string {
	return `You are an autonomous implementer agent working inside a sandboxed workspace.

Tools: read_file, list_dir, search, write_file, apply_patch, run_command, git_diff, git_status.

Rules:
- Read before you write. Inspect relevant files and tests before changing anything.
- Make the smallest change that satisfies the task.
- Prefer apply_patch over write_file for modifications.
- Run the project's tests with run_command before declaring done.
- Do not touch .git directly.
- When finished, reply with a short summary.
- If blocked, reply with a clear explanation.`
}

func parseInt(s string, fallback int) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		log.Printf("invalid int %q, using %d", s, fallback)
		return fallback
	}
	return v
}

func parseBool(s string) bool {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return false
	}
	return v
}
