package api

import (
	"net/http"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

// Access is who may call a route.
type Access int

const (
	Public      Access = iota // anyone: health, sign-in
	RunCallback               // the adapter, with the run's token (13.1)
	SignedIn                  // any signed-in user: their own account and tokens (13.2)
	Viewer                    // reads tasks, runs, diffs, threads (13.3)
	Member                    // also creates and starts work, comments
	Reviewer                  // also decides: approve, reject, request changes, escalations, roll back, resolve threads
	AdminOnly                 // an instance admin
)

// accessRole is the role each level needs.
var accessRole = map[Access]domain.Role{Viewer: domain.RoleViewer, Member: domain.RoleMember, Reviewer: domain.RoleReviewer}

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
		{"POST /users/{uid}/role", AdminOnly, h.SetUserRole},

		{"GET /agents", Viewer, h.ListAgents},
		{"GET /agents/health", Viewer, h.AgentHealth},
		{"GET /agents/{name}", Viewer, h.GetAgent},
		{"POST /agents/reload", AdminOnly, h.ReloadAgents},
		{"GET /skills", Viewer, h.ListSkills},
		{"GET /skills/{name}", Viewer, h.GetSkill},

		{"POST /tasks", Member, h.CreateTask},
		{"GET /repos/inspect", Viewer, h.InspectRepo},
		{"GET /config", Viewer, h.GetConfig},
		{"GET /tasks/{id}", Viewer, h.GetTask},
		{"POST /tasks/{id}/runs", Member, h.DispatchRun},
		{"GET /tasks", Viewer, h.ListTasks},
		{"GET /tasks/{id}/runs", Viewer, h.ListTaskRuns},
		{"GET /tasks/{id}/approvals", Viewer, h.ListApprovals},
		{"POST /tasks/{id}/approve", Reviewer, h.ApproveTask},
		{"POST /tasks/{id}/reject", Reviewer, h.RejectTask},
		{"POST /tasks/{id}/request-changes", Reviewer, h.RequestChanges},
		{"POST /tasks/{id}/escalation", Reviewer, h.DecideEscalation},
		{"GET /tasks/{id}/children", Viewer, h.ListTaskChildren},
		{"GET /tasks/{id}/events", Viewer, h.ListTaskEvents},
		{"GET /tasks/{id}/delivery", Viewer, h.GetDelivery},
		{"GET /tasks/{id}/diff", Viewer, h.GetTaskDiff},
		{"GET /tasks/{id}/threads", Viewer, h.ListThreads},
		{"POST /tasks/{id}/threads", Member, h.CreateThread},
		{"POST /tasks/{id}/threads/{tid}/comments", Member, h.AddThreadComment},
		{"POST /tasks/{id}/threads/{tid}/resolve", Reviewer, h.ResolveThread},
		{"POST /tasks/{id}/threads/{tid}/reopen", Reviewer, h.ReopenThread},
		{"POST /tasks/{id}/deliver", Reviewer, h.RetryDelivery},
		{"POST /tasks/{id}/rollback", Reviewer, h.RollbackTask},

		{"POST /evals/results", Member, h.RecordEval},
		{"GET /evals/results", Viewer, h.ListEvals},
		{"GET /evals/results/{id}", Viewer, h.GetEval},

		{"GET /runs/{id}/verification", Viewer, h.GetVerification},
		{"GET /runs/{id}", Viewer, h.GetRun},
		{"GET /runs/{id}/events", Viewer, h.ListEvents},
		{"GET /runs/{id}/diff", Viewer, h.GetDiff},
		{"GET /runs/{id}/reviews", Viewer, h.ListRunReviews},
		{"POST /runs/{id}/resume", Member, h.ResumeRun},

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
	case SignedIn:
		return h.Authenticated(r.Handler)
	default:
		return h.RequireRole(accessRole[r.Access], r.Handler)
	}
}
