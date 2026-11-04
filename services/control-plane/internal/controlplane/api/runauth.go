package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

// RunToken is the credential the adapter presents on a run's checkpoint and
// completion callbacks: an HMAC of the run ID under the control plane's
// callback key. The same run always gets the same token, so a retry,
// resume or reviewer started later for it reports with what it was given.
func RunToken(key []byte, runID string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("run:" + runID))
	return hex.EncodeToString(m.Sum(nil))
}

// bearer is a request's "Authorization: Bearer" credential.
func bearer(r *http.Request) string {
	const p = "Bearer "
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, p) {
		return strings.TrimSpace(a[len(p):])
	}
	return ""
}

// RequireRunToken guards a run callback (Phase 13.1): only the holder of
// the run's token, the adapter that was started for it, may report on it,
// and nothing more is accepted once the run is complete.
func (h *Handlers) RequireRunToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("id")
		want := RunToken(h.CallbackKey, runID)
		if got := bearer(r); len(h.CallbackKey) == 0 || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			http.Error(w, "missing or wrong run token", http.StatusUnauthorized)
			return
		}
		run, err := h.Runs.Get(r.Context(), runID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if run.Status == domain.RunComplete {
			http.Error(w, "run "+runID+" is complete", http.StatusConflict)
			return
		}
		next(w, r)
	}
}
