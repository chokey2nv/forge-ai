package api

import "net/http"

func (h *Handlers) ListAgents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.Registry.ListAgents())
}

func (h *Handlers) GetAgent(w http.ResponseWriter, r *http.Request) {
	a, ok := h.Registry.GetAgent(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (h *Handlers) ListSkills(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.Registry.ListSkills())
}

func (h *Handlers) GetSkill(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Registry.GetSkill(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *Handlers) ReloadAgents(w http.ResponseWriter, r *http.Request) {
	if err := h.Registry.Load(r.Context()); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, h.Registry.Status())
}

func (h *Handlers) AgentHealth(w http.ResponseWriter, _ *http.Request) {
	status := h.Registry.Status()
	// Healthy iff at least one agent and one skill exist.
	healthy := status["agentCount"].(int) > 0 && status["skillCount"].(int) > 0
	code := http.StatusOK
	if !healthy {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{
		"healthy": healthy,
		"status":  status,
	})
}