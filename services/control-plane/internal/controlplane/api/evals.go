package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

func (h *Handlers) RecordEval(w http.ResponseWriter, r *http.Request) {
	var e domain.EvalResult
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if e.CaseID == "" || e.Model == "" {
		http.Error(w, "caseId and model are required", http.StatusBadRequest)
		return
	}
	if err := h.EvalResults.Create(r.Context(), &e); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func (h *Handlers) ListEvals(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	model := q.Get("model")
	limit := 100
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	results, err := h.EvalResults.List(r.Context(), model, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, results)
}

func (h *Handlers) GetEval(w http.ResponseWriter, r *http.Request) {
	e, err := h.EvalResults.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, e)
}
