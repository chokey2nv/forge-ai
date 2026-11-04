package api

import (
	"encoding/json"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/delivery"
	"net/http"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/adapter"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/agent"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/orchestration"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/statemachine"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/verification"
)

type Handlers struct {
	Tasks         store.TaskRepo
	Runs          store.RunRepo
	Events        store.EventRepo
	Verifications store.VerificationRepo
	Reviews       store.ReviewRepo
	Delivery      *delivery.Inspector
	Approvals     store.ApprovalRepo
	Threads       store.ThreadRepo // Phase 12: human review threads
	// CallbackKey signs each run's callback token (Phase 13.1, RunToken).
	CallbackKey []byte
	// Phase 13.2: sign-in.
	Users            store.UserRepo
	Sessions         store.SessionRepo
	Tokens           store.TokenRepo
	BootstrapToken   string        // FORGE_BOOTSTRAP_TOKEN: an instance admin until the first user exists
	SessionTTL       time.Duration // idle expiry of a console session (default 12h)
	SecureCookies    bool          // mark the session cookie Secure (behind HTTPS)
	EvalResults      store.EvalResultRepo
	SM               *statemachine.Service
	Adapter          *adapter.Client
	Verifier         *verification.Runner
	Registry         *agent.Registry
	Integrator       *orchestration.Integrator // Phase 8
	WorkerImage      string
	SelfURL          string
	SystemPrompt     string
	AllowedProviders []string
	// What the console offers when starting work (GET /config).
	DefaultProvider, DefaultModel string
	MaxVerificationAttempts       int // consecutive verification failures before blocking
	MaxReviewRounds               int // reviewer rejections before blocking
	MaxRevisions                  int // Phase 12: default revisions per parent task (0: 3)
	SetupTimeout                  time.Duration
	PlannerEnabled                bool
	ReviewerEnabled               bool
	PlannerMaxSteps               int
	PlannerWallClockMS            int
	ReviewerMaxSteps              int
	ReviewerWallClockMS           int
}

func NewHandlers(
	tasks store.TaskRepo,
	runs store.RunRepo,
	events store.EventRepo,
	verifications store.VerificationRepo,
	approvals store.ApprovalRepo,
	evalResults store.EvalResultRepo,
	sm *statemachine.Service,
	adapterClient *adapter.Client,
	verifier *verification.Runner,
	workerImage, selfURL, systemPrompt string,
	allowedProviders []string,
	maxVerificationAttempts int,
	setupTimeout time.Duration,
) *Handlers {
	return &Handlers{
		Tasks:                   tasks,
		Runs:                    runs,
		Events:                  events,
		Verifications:           verifications,
		Approvals:               approvals,
		EvalResults:             evalResults,
		SM:                      sm,
		Adapter:                 adapterClient,
		Verifier:                verifier,
		WorkerImage:             workerImage,
		SelfURL:                 selfURL,
		SystemPrompt:            systemPrompt,
		AllowedProviders:        allowedProviders,
		MaxVerificationAttempts: maxVerificationAttempts,
		SetupTimeout:            setupTimeout,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
