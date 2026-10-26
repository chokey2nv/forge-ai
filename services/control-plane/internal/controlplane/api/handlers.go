package api

import (
	"encoding/json"
	"github/chokey2nv/forge-ai/services/control-plane/internal/controlplane/agent"
	"net/http"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/adapter"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/statemachine"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/verification"
)

type Handlers struct {
	Tasks                   store.TaskRepo
	Runs                    store.RunRepo
	Events                  store.EventRepo
	Verifications           store.VerificationRepo
	Approvals               store.ApprovalRepo
	EvalResults             store.EvalResultRepo
	SM                      *statemachine.Service
	Adapter                 *adapter.Client
	Verifier                *verification.Runner
	WorkerImage             string
	SelfURL                 string
	SystemPrompt            string
	AllowedProviders        []string
	MaxVerificationAttempts int
	SetupTimeout            time.Duration

	PlannerEnabled      bool
	ReviewerEnabled     bool
	PlannerMaxSteps     int
	PlannerWallClockMS  int
	ReviewerMaxSteps    int
	ReviewerWallClockMS int

	Registry *agent.Registry
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
