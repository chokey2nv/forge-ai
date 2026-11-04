package api

import "net/http"

// Access is who may call a route.
type Access int

const (
	Public      Access = iota // anyone: health, sign-in
	RunCallback               // the adapter, with the run's token (13.1)
	SignedIn                  // a signed-in user (13.2); roles come in 13.3
	AdminOnly                 // a signed-in instance admin
)

// Route is one API endpoint and who may call it. Every route is listed
// here, so a test can check none is left open by mistake.
type Route struct {
	Pattern string
	Access  Access
	Handler http.HandlerFunc
}

func (h *Handlers) Routes() []Route {
	return []Route{
		{"GET /health", Public, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}},
		{"POST /auth/login", Public, h.Login},
		{"POST /auth/logout", SignedIn, h.Logout},
		{"GET /auth/me", SignedIn, h.Me},
		{"GET /auth/tokens", SignedIn, h.ListTokens},
		{"POST /auth/tokens", SignedIn, h.CreateToken},
		{"DELETE /auth/tokens/{tid}", SignedIn, h.RevokeToken},
		{"GET /users", AdminOnly, h.ListUsers},
		{"POST /users", AdminOnly, h.CreateUser},

		{"GET /agents", SignedIn, h.ListAgents},
		{"GET /agents/health", SignedIn, h.AgentHealth},
		{"GET /agents/{name}", SignedIn, h.GetAgent},
		{"POST /agents/reload", AdminOnly, h.ReloadAgents},
		{"GET /skills", SignedIn, h.ListSkills},
		{"GET /skills/{name}", SignedIn, h.GetSkill},

		{"POST /tasks", SignedIn, h.CreateTask},
		{"GET /repos/inspect", SignedIn, h.InspectRepo},
		{"GET /config", SignedIn, h.GetConfig},
		{"GET /tasks/{id}", SignedIn, h.GetTask},
		{"POST /tasks/{id}/runs", SignedIn, h.DispatchRun},
		{"GET /tasks", SignedIn, h.ListTasks},
		{"GET /tasks/{id}/runs", SignedIn, h.ListTaskRuns},
		{"GET /tasks/{id}/approvals", SignedIn, h.ListApprovals},
		{"POST /tasks/{id}/approve", SignedIn, h.ApproveTask},
		{"POST /tasks/{id}/reject", SignedIn, h.RejectTask},
		{"POST /tasks/{id}/request-changes", SignedIn, h.RequestChanges},
		{"POST /tasks/{id}/escalation", SignedIn, h.DecideEscalation},
		{"GET /tasks/{id}/children", SignedIn, h.ListTaskChildren},
		{"GET /tasks/{id}/events", SignedIn, h.ListTaskEvents},
		{"GET /tasks/{id}/delivery", SignedIn, h.GetDelivery},
		{"GET /tasks/{id}/diff", SignedIn, h.GetTaskDiff},
		{"GET /tasks/{id}/threads", SignedIn, h.ListThreads},
		{"POST /tasks/{id}/threads", SignedIn, h.CreateThread},
		{"POST /tasks/{id}/threads/{tid}/comments", SignedIn, h.AddThreadComment},
		{"POST /tasks/{id}/threads/{tid}/resolve", SignedIn, h.ResolveThread},
		{"POST /tasks/{id}/threads/{tid}/reopen", SignedIn, h.ReopenThread},
		{"POST /tasks/{id}/deliver", SignedIn, h.RetryDelivery},
		{"POST /tasks/{id}/rollback", SignedIn, h.RollbackTask},

		{"POST /evals/results", SignedIn, h.RecordEval},
		{"GET /evals/results", SignedIn, h.ListEvals},
		{"GET /evals/results/{id}", SignedIn, h.GetEval},

		{"GET /runs/{id}/verification", SignedIn, h.GetVerification},
		{"GET /runs/{id}", SignedIn, h.GetRun},
		{"GET /runs/{id}/events", SignedIn, h.ListEvents},
		{"GET /runs/{id}/diff", SignedIn, h.GetDiff},
		{"GET /runs/{id}/reviews", SignedIn, h.ListRunReviews},
		{"POST /runs/{id}/resume", SignedIn, h.ResumeRun},

		{"POST /runs/{id}/checkpoint", RunCallback, h.RecordCheckpoint},
		{"POST /runs/{id}/complete", RunCallback, h.RecordComplete},
	}
}

// Mux registers every route behind the check its Access names.
func (h *Handlers) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	for _, r := range h.Routes() {
		mux.HandleFunc(r.Pattern, h.guard(r))
	}
	return mux
}

func (h *Handlers) guard(r Route) http.HandlerFunc {
	switch r.Access {
	case Public:
		return r.Handler
	case RunCallback:
		return h.RequireRunToken(r.Handler)
	case AdminOnly:
		return h.InstanceAdmin(r.Handler)
	default:
		return h.Authenticated(r.Handler)
	}
}
