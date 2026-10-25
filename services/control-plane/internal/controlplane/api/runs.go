package api

import "net/http"

func (h *Handlers) GetRun(w http.ResponseWriter, r *http.Request) {
	run, err := h.Runs.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *Handlers) ListEvents(w http.ResponseWriter, r *http.Request) {
	events, err := h.Events.ListByRun(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (h *Handlers) GetDiff(w http.ResponseWriter, r *http.Request) {
	run, err := h.Runs.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	diff := ""
	if run.LastCheckpoint != nil {
		diff = run.LastCheckpoint.WorkspaceDiff
	}
	writeJSON(w, http.StatusOK, map[string]string{"runId": run.ID, "diff": diff})
}
