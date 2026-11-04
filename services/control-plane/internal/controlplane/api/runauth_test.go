package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
)

type oneRun struct {
	store.RunRepo
	run *domain.WorkflowRun
}

func (o oneRun) Get(_ context.Context, id string) (*domain.WorkflowRun, error) {
	if o.run == nil || id != o.run.ID {
		return nil, context.Canceled
	}
	return o.run, nil
}

func TestRunCallbacksNeedTheRunsToken(t *testing.T) {
	key := []byte("k")
	run := &domain.WorkflowRun{ID: "run-a", Status: domain.RunRunning}
	h := &Handlers{CallbackKey: key, Runs: oneRun{run: run}}
	reached := false
	guarded := h.RequireRunToken(func(w http.ResponseWriter, _ *http.Request) { reached = true; w.WriteHeader(http.StatusNoContent) })
	call := func(runID, token string) int {
		reached = false
		req := httptest.NewRequest(http.MethodPost, "/runs/"+runID+"/complete", nil)
		req.SetPathValue("id", runID)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		guarded(w, req)
		return w.Code
	}

	if RunToken(key, "run-a") == RunToken(key, "run-b") || RunToken(key, "run-a") == RunToken([]byte("other"), "run-a") {
		t.Fatal("tokens must differ per run and per key")
	}
	if code := call("run-a", ""); code != 401 || reached {
		t.Errorf("no token: %d", code)
	}
	if code := call("run-a", RunToken(key, "run-b")); code != 401 || reached {
		t.Errorf("another run's token: %d", code)
	}
	if code := call("run-a", RunToken([]byte("guess"), "run-a")); code != 401 || reached {
		t.Errorf("token under another key: %d", code)
	}
	if code := call("run-a", RunToken(key, "run-a")); code != 204 || !reached {
		t.Errorf("the run's token: %d", code)
	}
	if code := call("run-x", RunToken(key, "run-x")); code != 404 || reached {
		t.Errorf("unknown run: %d", code)
	}
	run.Status = domain.RunComplete
	if code := call("run-a", RunToken(key, "run-a")); code != 409 || reached {
		t.Errorf("complete run: %d", code)
	}
	h.CallbackKey = nil
	run.Status = domain.RunRunning
	if code := call("run-a", RunToken(nil, "run-a")); code != 401 || reached {
		t.Errorf("no key configured must refuse everything: %d", code)
	}
}

func TestCORSAllowList(t *testing.T) {
	h := WithCORS([]string{"http://localhost:5173 ", "http://console.local/"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	do := func(method, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/tasks", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if w := do("GET", "http://localhost:5173"); w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" || w.Code != http.StatusTeapot {
		t.Errorf("allowed origin: %d %q", w.Code, w.Header().Get("Access-Control-Allow-Origin"))
	}
	if w := do("GET", "http://console.local"); w.Header().Get("Access-Control-Allow-Origin") != "http://console.local" {
		t.Error("allowed origin given with a trailing slash")
	}
	if w := do("GET", "https://evil.example"); w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("another origin must get no CORS headers")
	}
	if w := do("OPTIONS", "https://evil.example"); w.Code != http.StatusForbidden {
		t.Errorf("preflight from another origin: %d", w.Code)
	}
	if w := do("OPTIONS", "http://localhost:5173"); w.Code != http.StatusOK {
		t.Errorf("preflight from the console: %d", w.Code)
	}
	if w := do("GET", ""); w.Code != http.StatusTeapot || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("a request without Origin (curl, the adapter) passes untouched")
	}
}
