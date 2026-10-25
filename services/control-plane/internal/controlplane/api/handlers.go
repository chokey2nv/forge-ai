package api

import (
	"encoding/json"
	"net/http"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/adapter"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/statemachine"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
)

type Handlers struct {
	Tasks            store.TaskRepo
	Runs             store.RunRepo
	Events           store.EventRepo
	SM               *statemachine.Service
	Adapter          *adapter.Client
	WorkerImage      string
	SelfURL          string // e.g. http://localhost:8080 — used as callback base
	SystemPrompt     string
	AllowedProviders []string
}

func NewHandlers(
	tasks store.TaskRepo,
	runs store.RunRepo,
	events store.EventRepo,
	sm *statemachine.Service,
	adapterClient *adapter.Client,
	workerImage, selfURL, systemPrompt string,
	allowedProviders []string,
) *Handlers {
	return &Handlers{
		Tasks:            tasks,
		Runs:             runs,
		Events:           events,
		SM:               sm,
		Adapter:          adapterClient,
		WorkerImage:      workerImage,
		SelfURL:          selfURL,
		SystemPrompt:     systemPrompt,
		AllowedProviders: allowedProviders,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
