package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/delivery"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

// Phase 13.4: every task belongs to a project. A project names the
// repositories its tasks may work in and who may do what with them.

type projectKey struct{}

// projectFrom is the project a project-scoped route resolved.
func projectFrom(ctx context.Context) *domain.Project {
	p, _ := ctx.Value(projectKey{}).(*domain.Project)
	return p
}

// errNoProject answers 404: the resource does not exist, or the caller is
// not a member of its project (the two look the same from outside).
var errNoProject = errors.New("not found")

// scopeOf is how a route's path names its project: through a task, a run,
// or the project itself. Others are instance-wide.
func scopeOf(pattern string) string {
	_, path, _ := strings.Cut(pattern, " ")
	switch {
	case strings.HasPrefix(path, "/tasks/{id}"):
		return "task"
	case strings.HasPrefix(path, "/runs/{id}"):
		return "run"
	case strings.HasPrefix(path, "/projects/{pid}"):
		return "project"
	}
	return ""
}

// projectOfRequest loads the project a scoped request is about.
func (h *Handlers) projectOfRequest(r *http.Request, scope string) (*domain.Project, error) {
	ctx := r.Context()
	var projectID string
	switch scope {
	case "project":
		projectID = r.PathValue("pid")
	case "task", "run":
		taskID := r.PathValue("id")
		if scope == "run" {
			run, err := h.Runs.Get(ctx, r.PathValue("id"))
			if err != nil {
				return nil, errNoProject
			}
			taskID = run.TaskID
		}
		task, err := h.Tasks.Get(ctx, taskID)
		if err != nil {
			return nil, errNoProject
		}
		if task.ProjectID == "" {
			return &domain.Project{}, nil // from before projects: instance admins only
		}
		projectID = task.ProjectID
	}
	if h.Projects == nil {
		return nil, errNoProject
	}
	p, err := h.Projects.Get(ctx, projectID)
	if err != nil {
		return nil, errNoProject
	}
	return p, nil
}

// RequireProjectRole lets through signed-in users with at least role min in
// the project the route names; others get 404 (not a member) or 403.
func (h *Handlers) RequireProjectRole(scope string, min domain.Role, next http.HandlerFunc) http.HandlerFunc {
	return h.Authenticated(func(w http.ResponseWriter, r *http.Request) {
		p, err := h.projectOfRequest(r, scope)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		role := p.RoleOf(PrincipalFrom(r.Context()).User)
		if role == "" {
			http.NotFound(w, r)
			return
		}
		if !role.Allows(min) {
			http.Error(w, "this needs the "+string(min)+" role in project "+p.Name, http.StatusForbidden)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), projectKey{}, p)))
	})
}

// memberProject loads a project named in a request body or query and checks
// the caller's role in it (for routes whose path does not name it).
func (h *Handlers) memberProject(ctx context.Context, w http.ResponseWriter, r *http.Request, projectID string, min domain.Role) (*domain.Project, bool) {
	if projectID == "" {
		http.Error(w, "projectId is required", http.StatusBadRequest)
		return nil, false
	}
	p, err := h.Projects.Get(ctx, projectID)
	role := domain.Role("")
	if err == nil {
		role = p.RoleOf(actorOf(r))
	}
	if role == "" {
		http.Error(w, "no such project", http.StatusNotFound)
		return nil, false
	}
	if !role.Allows(min) {
		http.Error(w, "this needs the "+string(min)+" role in project "+p.Name, http.StatusForbidden)
		return nil, false
	}
	return p, true
}

// visibleProjectIDs is what a listing may show: nil means everything (an
// instance admin), otherwise the caller's projects.
func (h *Handlers) visibleProjectIDs(ctx context.Context, u *domain.User) ([]string, error) {
	if u.InstanceAdmin {
		return nil, nil
	}
	ps, err := h.Projects.ListForUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(ps))
	for _, p := range ps {
		ids = append(ids, p.ID)
	}
	return ids, nil
}

// projectView is a project with the caller's role in it.
type projectView struct {
	*domain.Project
	MyRole domain.Role `json:"myRole"`
}

// ListProjects: GET /projects — the caller's projects (all, for instance admins).
func (h *Handlers) ListProjects(w http.ResponseWriter, r *http.Request) {
	u := actorOf(r)
	var ps []*domain.Project
	var err error
	if u.InstanceAdmin {
		ps, err = h.Projects.List(r.Context())
	} else {
		ps, err = h.Projects.ListForUser(r.Context(), u.ID)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]projectView, 0, len(ps))
	for _, p := range ps {
		out = append(out, projectView{p, p.RoleOf(u)})
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateProject: POST /projects {name} — instance admins; they become its admin.
func (h *Handlers) CreateProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := strings.TrimSpace(body.Name)
	if name == "" {
		http.Error(w, "a project needs a name", http.StatusBadRequest)
		return
	}
	u := actorOf(r)
	p := &domain.Project{Name: name, CreatedAt: time.Now().UTC(),
		Members: []domain.ProjectMember{{UserID: u.ID, Name: u.Name, Role: domain.RoleAdmin}}}
	if err := h.Projects.Create(r.Context(), p); err != nil {
		http.Error(w, "a project with that name exists", http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusCreated, projectView{p, p.RoleOf(u)})
}

// GetProject: GET /projects/{pid}.
func (h *Handlers) GetProject(w http.ResponseWriter, r *http.Request) {
	p := projectFrom(r.Context())
	writeJSON(w, http.StatusOK, projectView{p, p.RoleOf(actorOf(r))})
}

// AddProjectRepo: POST /projects/{pid}/repos {path, defaultTarget?} — project
// admins. The path must be a git repository on this machine, in no other project.
func (h *Handlers) AddProjectRepo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := projectFrom(ctx)
	var body struct {
		Path          string `json:"path"`
		DefaultTarget string `json:"defaultTarget,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	path := filepath.Clean(strings.TrimSpace(body.Path))
	if !filepath.IsAbs(path) {
		http.Error(w, "give the repository's absolute path on the control plane's machine", http.StatusBadRequest)
		return
	}
	repo, err := delivery.InspectRepo(ctx, path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Tasks name the path as given here, so keep it; it must be the
	// repository's top folder, not one inside it (symlinks aside: git
	// reports /private/var for /var on macOS).
	if a, b := realPath(path), realPath(repo.Path); a != b {
		http.Error(w, "give the repository's top folder: "+repo.Path, http.StatusBadRequest)
		return
	}
	if other, err := h.Projects.ByRepo(ctx, path); err == nil {
		if other.ID == p.ID {
			http.Error(w, path+" is already in this project", http.StatusConflict)
		} else {
			http.Error(w, path+" belongs to another project", http.StatusConflict)
		}
		return
	}
	if body.DefaultTarget != "" {
		if _, _, err := delivery.ResolveTarget(ctx, path, body.DefaultTarget, "HEAD"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	pr := domain.ProjectRepo{Path: path, DefaultTarget: body.DefaultTarget, AddedAt: time.Now().UTC()}
	if err := h.Projects.AddRepo(ctx, p.ID, pr); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	p.Repos = append(p.Repos, pr)
	writeJSON(w, http.StatusCreated, projectView{p, p.RoleOf(actorOf(r))})
}

// RemoveProjectRepo: DELETE /projects/{pid}/repos?path= — project admins.
// Existing tasks keep it; new ones cannot use it.
func (h *Handlers) RemoveProjectRepo(w http.ResponseWriter, r *http.Request) {
	p := projectFrom(r.Context())
	path := r.URL.Query().Get("path")
	if !p.HasRepo(path) {
		http.NotFound(w, r)
		return
	}
	if err := h.Projects.RemoveRepo(r.Context(), p.ID, path); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetProjectMember: POST /projects/{pid}/members {name, role} — project admins
// add a user or change their role.
func (h *Handlers) SetProjectMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := projectFrom(ctx)
	var body struct {
		Name string      `json:"name"`
		Role domain.Role `json:"role"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !domain.ValidProjectRole(body.Role) {
		http.Error(w, "role must be viewer, member, reviewer or admin", http.StatusBadRequest)
		return
	}
	u, err := h.Users.GetByName(ctx, strings.TrimSpace(body.Name))
	if err != nil {
		http.Error(w, "no user named "+body.Name, http.StatusNotFound)
		return
	}
	if body.Role != domain.RoleAdmin && lastAdmin(p, u.ID) {
		http.Error(w, "a project keeps at least one admin", http.StatusConflict)
		return
	}
	m := domain.ProjectMember{UserID: u.ID, Name: u.Name, Role: body.Role}
	if err := h.Projects.SetMember(ctx, p.ID, m); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// RemoveProjectMember: DELETE /projects/{pid}/members/{uid} — project admins.
func (h *Handlers) RemoveProjectMember(w http.ResponseWriter, r *http.Request) {
	p := projectFrom(r.Context())
	uid := r.PathValue("uid")
	if p.RoleOf(&domain.User{ID: uid}) == "" {
		http.NotFound(w, r)
		return
	}
	if lastAdmin(p, uid) {
		http.Error(w, "a project keeps at least one admin", http.StatusConflict)
		return
	}
	if err := h.Projects.RemoveMember(r.Context(), p.ID, uid); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// lastAdmin reports whether userID is the project's only admin.
func lastAdmin(p *domain.Project, userID string) bool {
	admins, isAdmin := 0, false
	for _, m := range p.Members {
		if m.Role == domain.RoleAdmin {
			admins++
			isAdmin = isAdmin || m.UserID == userID
		}
	}
	return isAdmin && admins == 1
}

// MigrateToProjects puts tasks from before projects into one, per
// repository: an existing project that has the repository, otherwise a new
// project named after it, whose members are every existing user at their
// instance role. Running it again changes nothing.
func (h *Handlers) MigrateToProjects(ctx context.Context) (created, assigned int, err error) {
	paths, err := h.Tasks.UnassignedRepoPaths(ctx)
	if err != nil || len(paths) == 0 {
		return 0, 0, err
	}
	users, err := h.Users.List(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, path := range paths {
		p, err := h.Projects.ByRepo(ctx, path)
		if err != nil {
			p = &domain.Project{Repos: []domain.ProjectRepo{{Path: path, AddedAt: time.Now().UTC()}}, CreatedAt: time.Now().UTC()}
			for _, u := range users {
				if u.DisabledAt == nil && !u.InstanceAdmin {
					p.Members = append(p.Members, domain.ProjectMember{UserID: u.ID, Name: u.Name, Role: u.EffectiveRole()})
				}
			}
			base := filepath.Base(path)
			for n := 1; ; n++ {
				p.ID, p.Name = "", base
				if n > 1 {
					p.Name = fmt.Sprintf("%s-%d", base, n)
				}
				if err = h.Projects.Create(ctx, p); err == nil {
					break
				}
				if n > 50 {
					return created, assigned, fmt.Errorf("create a project for %s: %w", path, err)
				}
			}
			created++
		}
		n, err := h.Tasks.AssignProject(ctx, path, p.ID)
		if err != nil {
			return created, assigned, err
		}
		assigned += int(n)
	}
	if created > 0 || assigned > 0 {
		log.Printf("projects: put %d task(s) from before projects into projects (%d created)", assigned, created)
	}
	return created, assigned, nil
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
