package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

func (h *Handlers) RecordCheckpoint(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := r.PathValue("id")

	var cp domain.Checkpoint
	if err := json.NewDecoder(r.Body).Decode(&cp); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cp.SavedAt = time.Now().UTC()

	run, err := h.Runs.Get(ctx, runID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// First checkpoint: DISPATCHED → RUNNING on the task
	if run.Status == domain.RunDispatched {
		task, _ := h.Tasks.Get(ctx, run.TaskID)
		_ = h.SM.Transition(ctx, task, domain.StatusRunning, "adapter started")
		_ = h.Runs.UpdateStatus(ctx, runID, domain.RunRunning, "", time.Now().UTC())
	}

	_ = h.Runs.SaveCheckpoint(ctx, runID, &cp, time.Now().UTC())
	_ = h.Events.Append(ctx, &domain.Event{
		RunID: runID, Type: "checkpoint",
		Payload: map[string]any{"stepCount": cp.StepCount},
		At:      time.Now().UTC(),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) RecordComplete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := r.PathValue("id")

	var body struct {
		Status        string           `json:"status"` // complete | blocked | error
		FinalMessage  string           `json:"finalMessage"`
		StepCount     int              `json:"stepCount"`
		Usage         []domain.Usage   `json:"usage"`
		WorkspaceDiff string           `json:"workspaceDiff"`
		Messages      []domain.Message `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	run, err := h.Runs.Get(ctx, runID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	task, _ := h.Tasks.Get(ctx, run.TaskID)

	cp := &domain.Checkpoint{
		Messages: body.Messages, StepCount: body.StepCount,
		Usage: body.Usage, Status: body.Status,
		WorkspaceDiff: body.WorkspaceDiff, SavedAt: time.Now().UTC(),
	}
	_ = h.Runs.SaveCheckpoint(ctx, runID, cp, time.Now().UTC())

	switch body.Status {
	case "complete":
		_ = h.Runs.UpdateStatus(ctx, runID, domain.RunComplete, "", time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusReviewPending, "agent reported complete")
	case "blocked", "error":
		_ = h.Runs.UpdateStatus(ctx, runID, domain.RunBlocked, body.FinalMessage, time.Now().UTC())
		_ = h.SM.Transition(ctx, task, domain.StatusBlocked, body.FinalMessage)
	}

	_ = h.Events.Append(ctx, &domain.Event{
		RunID: runID, Type: "complete", Reason: body.FinalMessage, At: time.Now().UTC(),
	})
	w.WriteHeader(http.StatusNoContent)
}
