package main

import (
	"context"
	"net"
	"path/filepath"

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
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/delivery"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/orchestration"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/runner"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/statemachine"
	mongo_store "github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store/mongo"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/verification"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func main() {
	loadDotenv()
	// `forge users …` / `forge tokens …`: administration, then exit.
	if len(os.Args) > 1 {
		os.Exit(runAdmin(os.Args[1:]))
	}
	// ---- config ----
	// Localhost unless told otherwise (Phase 13.1): exposing Forge is a choice.
	addr := envOr("CONTROL_PLANE_ADDR", "127.0.0.1:8080")
	adapterToken := os.Getenv("FORGE_ADAPTER_TOKEN")
	if adapterToken == "" {
		log.Fatal("FORGE_ADAPTER_TOKEN is not set: the adapter only accepts runs from a control plane that presents it. " +
			"Put the same random value in both services' environment (e.g. FORGE_ADAPTER_TOKEN=$(openssl rand -hex 32) in .env)")
	}
	consoleOrigins := strings.Split(envOr("FORGE_CONSOLE_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173,http://localhost:5174,http://127.0.0.1:5174"), ",")
	selfURL := envOr("CONTROL_PLANE_SELF_URL", "http://localhost:8080")
	adapterURL := envOr("ADAPTER_URL", "http://localhost:8081")
	workerImage := envOr("WORKER_IMAGE", "forge-worker:dev")
	mongoURI := envOr("MONGODB_URI", "mongodb://127.0.0.1:27017")
	mongoDB := envOr("MONGODB_DATABASE", "forge_ai")
	// One control plane per database; its containers and workspaces are
	// labeled with it so the reaper only touches its own.
	workspace.Instance = mongoDB
	agentsDir := envOr("AGENTS_DIR", "agents")
	skillsDir := envOr("SKILLS_DIR", "skills")

	staleAfter := parseDuration(envOr("STALE_AFTER", "60s"), 60*time.Second)
	allowed := strings.Split(envOr("FORGE_ALLOWED_PROVIDERS", "ollama"), ",")

	log.Println("allowed - [%]", allowed)
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

	// Signs each run's callback token; kept in Mongo unless set, so tokens
	// handed to running agents stay valid across restarts.
	callbackKey := []byte(os.Getenv("FORGE_CALLBACK_SECRET"))
	if len(callbackKey) == 0 {
		if callbackKey, err = mongo_store.LoadOrCreateSecret(ctx, db, "callback-key"); err != nil {
			log.Fatalf("callback key: %v", err)
		}
	}

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
	reviewRepo := mongo_store.NewReviewRepo(db)
	approvalRepo := mongo_store.NewApprovalRepo(db)
	evalResultRepo := mongo_store.NewEvalResultRepo(db)

	// ---- services ----
	sm := &statemachine.Service{Tasks: taskRepo, Events: eventRepo}
	adapterClient := &adapter.Client{
		BaseURL: adapterURL, Token: adapterToken,
		CallbackToken: func(runID string) string { return api.RunToken(callbackKey, runID) },
	}

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
	h.Reviews = reviewRepo
	h.CallbackKey = callbackKey
	h.Users, h.Sessions, h.Tokens = mongo_store.NewUserRepo(db), mongo_store.NewSessionRepo(db), mongo_store.NewTokenRepo(db)
	h.BootstrapToken = os.Getenv("FORGE_BOOTSTRAP_TOKEN")
	h.SessionTTL = parseDuration(envOr("FORGE_SESSION_TTL", "12h"), 12*time.Hour)
	h.SecureCookies = parseBool(envOr("FORGE_SECURE_COOKIES", "false"))
	if n, err := h.Users.Count(ctx); err == nil && n == 0 {
		if h.BootstrapToken == "" {
			log.Printf("no users yet: create the first with `forge users add <name>` (or set FORGE_BOOTSTRAP_TOKEN and POST /users)")
		} else {
			log.Printf("no users yet: FORGE_BOOTSTRAP_TOKEN acts as an instance admin until the first user is created")
		}
	}
	h.Threads = mongo_store.NewThreadRepo(db)
	h.Delivery = delivery.NewInspector(envOr("FORGE_GH", "gh"))
	h.MaxReviewRounds = parseInt(envOr("MAX_REVIEW_ROUNDS", "2"), 2)
	h.MaxRevisions = parseInt(envOr("MAX_REVISIONS", "3"), 3)
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
		MaxConcurrentRuns:   parseInt(envOr("MAX_CONCURRENT_RUNS", "3"), 3),
	}
	go scheduler.Run(ctx)
	h.DefaultProvider, h.DefaultModel = scheduler.DefaultProvider, scheduler.DefaultModel

	// Every route and who may call it: internal/controlplane/api/routes.go.
	mux := h.Mux()

	// ---- watcher ----
	watcher := runner.NewWatcher(runRepo, taskRepo, sm, staleAfter)
	// A reviewer gets its wall clock plus a grace period before the watcher
	// records it as an error and moves the review round on.
	watcher.ReviewTimeout = time.Duration(h.ReviewerWallClockMS)*time.Millisecond +
		parseDuration(envOr("REVIEW_TIMEOUT_GRACE", "60s"), 60*time.Second)
	watcher.OnReviewTimeout = h.HandleReviewTimeout
	go watcher.Run(ctx)

	// ---- pull request watcher: merged/closed on the forge -> MERGED/REJECTED ----
	go func() {
		t := time.NewTicker(parseDuration(envOr("PR_POLL_INTERVAL", "60s"), 60*time.Second))
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.PollPullRequests(ctx)
			}
		}
	}()

	// ---- reaper: removes containers/workspaces nothing can use any more ----
	reaper := runner.NewReaper(runRepo, taskRepo,
		parseDuration(envOr("ORPHAN_CONTAINER_GRACE", "10m"), 10*time.Minute))
	go reaper.Run(ctx)

	// ---- server ----
	srv := &http.Server{Addr: addr, Handler: api.WithCORS(consoleOrigins, mux)}
	go func() {
		log.Printf("control-plane listening on %s (adapter=%s, worker=%s, allowed=%v, console origins=%v)", addr, adapterURL, workerImage, allowed, consoleOrigins)
		if !loopback(addr) {
			log.Printf("WARNING: %s is reachable from other machines, and the API has no sign-in yet (Phase 13.2). Use 127.0.0.1:<port> unless that is intended.", addr)
		}
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

func loadDotenv() {
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	for {
		envPath := filepath.Join(dir, ".env")
		if _, err := os.Stat(envPath); err == nil {
			if err := godotenv.Load(envPath); err != nil {
				log.Printf("dotenv: %v", err)
			}
			log.Printf("loaded env from %s", envPath)
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

// loopback reports whether a listen address only accepts local connections.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
