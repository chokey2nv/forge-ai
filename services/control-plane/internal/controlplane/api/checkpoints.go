package api

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

// RecordHeartbeat: the adapter is still working on the run. A step can far
// outlast STALE_AFTER (a long model reply, a test suite), so liveness can't
// wait for the next checkpoint.
func (h *Handlers) RecordHeartbeat(w http.ResponseWriter, r *http.Request) {
	if err := h.Runs.Heartbeat(r.Context(), r.PathValue("id"), time.Now().UTC()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) RecordCheckpoint(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := r.PathValue("id")

	var body struct {
		domain.Checkpoint
		Role  string       `json:"role"`
		Spend *SpendReport `json:"spend,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cp := body.Checkpoint
	cp.SavedAt = time.Now().UTC()

	run, err := h.Runs.Get(ctx, runID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	cp.WorkspaceDiff = keptDiff(run, cp.WorkspaceDiff)
	task, _ := h.Tasks.Get(ctx, run.TaskID)
	// First checkpoint: DISPATCHED → RUNNING on the task
	if run.Status == domain.RunDispatched {
		_ = h.SM.Transition(ctx, task, domain.StatusRunning, "adapter started")
		_ = h.Runs.UpdateStatus(ctx, runID, domain.RunRunning, "", time.Now().UTC())
	}

	// Reviewers report against the implementer's run; keep its checkpoint.
	if h.completionKindOf(body.Role) == kindReview {
		_ = h.Runs.Heartbeat(ctx, runID, time.Now().UTC())
	} else {
		if err := h.Runs.SaveCheckpoint(ctx, runID, &cp, time.Now().UTC()); err != nil {
			log.Printf("checkpoint %s: save failed (diff %d bytes): %v", runID, len(cp.WorkspaceDiff), err)
		}
	}
	_ = h.Events.Append(ctx, &domain.Event{
		RunID: runID, Type: "checkpoint",
		Payload: map[string]any{"stepCount": cp.StepCount},
		At:      time.Now().UTC(),
	})
	// Over a spend cap (13.6): the adapter stops the agent before its next
	// model call, and it ends blocked with this reason.
	if reason := h.recordSpend(ctx, run, task, body.Role, body.Spend); reason != "" {
		log.Printf("checkpoint %s: %s", runID, reason)
		writeJSON(w, http.StatusOK, map[string]string{"stop": reason})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
