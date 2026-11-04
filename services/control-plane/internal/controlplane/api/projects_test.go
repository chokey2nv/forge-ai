package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
)

type memProjects struct{ m map[string]*domain.Project }

func (s *memProjects) Create(_ context.Context, p *domain.Project) error {
	for _, x := range s.m {
		if x.Name == p.Name {
			return errors.New("dup name")
		}
	}
	if p.ID == "" {
		p.ID = "prj-" + p.Name
	}
	s.m[p.ID] = p
	return nil
}
func (s *memProjects) Get(_ context.Context, id string) (*domain.Project, error) {
	if p, ok := s.m[id]; ok {
		cp := *p
		return &cp, nil
	}
	return nil, errors.New("no")
}
func (s *memProjects) List(context.Context) ([]*domain.Project, error) {
	var out []*domain.Project
	for _, p := range s.m {
		out = append(out, p)
	}
	return out, nil
}
func (s *memProjects) ListForUser(_ context.Context, uid string) ([]*domain.Project, error) {
	var out []*domain.Project
	for _, p := range s.m {
		if p.RoleOf(&domain.User{ID: uid}) != "" {
			out = append(out, p)
		}
	}
	return out, nil
}
func (s *memProjects) ByRepo(_ context.Context, path string) (*domain.Project, error) {
	for _, p := range s.m {
		if p.HasRepo(path) {
			return p, nil
		}
	}
	return nil, errors.New("no")
}
func (s *memProjects) AddRepo(_ context.Context, id string, r domain.ProjectRepo) error {
	s.m[id].Repos = append(s.m[id].Repos, r)
	return nil
}
func (s *memProjects) RemoveRepo(context.Context, string, string) error { return nil }
func (s *memProjects) SetMember(_ context.Context, id string, m domain.ProjectMember) error {
	p := s.m[id]
	for i := range p.Members {
		if p.Members[i].UserID == m.UserID {
			p.Members[i].Role = m.Role
			return nil
		}
	}
	p.Members = append(p.Members, m)
	return nil
}
func (s *memProjects) RemoveMember(context.Context, string, string) error { return nil }

// projTasks serves tasks by ID and supports the migration.
type projTasks struct {
	store.TaskRepo
	m map[string]*domain.Task
}

func (t *projTasks) Get(_ context.Context, id string) (*domain.Task, error) {
	if x, ok := t.m[id]; ok {
		return x, nil
	}
	return nil, errors.New("no")
}
func (t *projTasks) UnassignedRepoPaths(context.Context) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, x := range t.m {
		if x.ProjectID == "" && !seen[x.RepoPath] {
			seen[x.RepoPath] = true
			out = append(out, x.RepoPath)
		}
	}
	return out, nil
}
func (t *projTasks) AssignProject(_ context.Context, path, pid string) (int64, error) {
	var n int64
	for _, x := range t.m {
		if x.RepoPath == path && x.ProjectID == "" {
			x.ProjectID = pid
			n++
		}
	}
	return n, nil
}

func TestProjectScopedAccess(t *testing.T) {
	users := &memUsers{m: map[string]*domain.User{}}
	tokens := &memTokens{m: map[string]*domain.APIToken{}}
	projects := &memProjects{m: map[string]*domain.Project{}}
	tasks := &projTasks{m: map[string]*domain.Task{}}
	h := &Handlers{Users: users, Tokens: tokens, Projects: projects, Tasks: tasks}
	ctx := context.Background()
	tok := map[string]string{}
	for _, n := range []string{"ada", "pam", "vic", "mo", "rae", "out"} { // ada: first user, instance admin
		u, _ := h.AddUser(ctx, n, n+"-password-1", "", false, domain.RoleReviewer) // instance role must not matter
		_, tok[n], _ = h.IssueToken(ctx, u.ID, "t-"+n)
	}
	id := func(n string) string { u, _ := users.GetByName(ctx, n); return u.ID }
	projects.m["prj-a"] = &domain.Project{ID: "prj-a", Name: "a", Repos: []domain.ProjectRepo{{Path: "/repo/a"}},
		Members: []domain.ProjectMember{{UserID: id("pam"), Role: domain.RoleAdmin}, {UserID: id("vic"), Role: domain.RoleViewer},
			{UserID: id("mo"), Role: domain.RoleMember}, {UserID: id("rae"), Role: domain.RoleReviewer}}}
	tasks.m["t1"] = &domain.Task{ID: "t1", ProjectID: "prj-a", RepoPath: "/repo/a"}

	ok := func(w http.ResponseWriter, r *http.Request) {
		if projectFrom(r.Context()) == nil && scopeOf(r.Pattern) == "project" {
			t.Error("project not in context")
		}
		w.WriteHeader(200)
	}
	cases := []struct {
		access  Access
		pattern string
		path    string
		want    map[string]int
	}{
		{Viewer, "GET /tasks/{id}", "/tasks/t1", map[string]int{"ada": 200, "pam": 200, "vic": 200, "mo": 200, "rae": 200, "out": 404}},
		{Member, "POST /tasks/{id}/threads", "/tasks/t1/threads", map[string]int{"ada": 200, "vic": 403, "mo": 200, "rae": 200, "out": 404}},
		{Reviewer, "POST /tasks/{id}/approve", "/tasks/t1/approve", map[string]int{"ada": 200, "pam": 200, "vic": 403, "mo": 403, "rae": 200, "out": 404}},
		{Reviewer, "POST /tasks/{id}/approve", "/tasks/nope/approve", map[string]int{"ada": 404, "rae": 404}},
		{ProjectAdmin, "POST /projects/{pid}/members", "/projects/prj-a/members", map[string]int{"ada": 200, "pam": 200, "rae": 403, "out": 404}},
	}
	for _, c := range cases {
		mux := http.NewServeMux()
		mux.HandleFunc(c.pattern, h.guard(Route{Pattern: c.pattern, Access: c.access, Handler: ok}))
		for who, code := range c.want {
			method := "POST"
			if c.access == Viewer {
				method = "GET"
			}
			req := httptest.NewRequest(method, c.path, nil)
			req.Header.Set("Authorization", "Bearer "+tok[who])
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != code {
				t.Errorf("%s %s as %s: %d, want %d", c.pattern, c.path, who, w.Code, code)
			}
		}
	}

	// Listings show only the caller's projects; instance admins see all.
	if ids, _ := h.visibleProjectIDs(ctx, &domain.User{ID: id("out")}); ids == nil || len(ids) != 0 {
		t.Errorf("an outsider sees %v", ids)
	}
	if ids, _ := h.visibleProjectIDs(ctx, &domain.User{ID: id("mo")}); len(ids) != 1 || ids[0] != "prj-a" {
		t.Errorf("mo sees %v", ids)
	}
	if ids, _ := h.visibleProjectIDs(ctx, &domain.User{InstanceAdmin: true}); ids != nil {
		t.Error("an instance admin sees everything (nil)")
	}

	// The last admin stays.
	p := projects.m["prj-a"]
	if !lastAdmin(p, id("pam")) || lastAdmin(p, id("rae")) {
		t.Error("pam is the only admin")
	}
	setMember := func(who, name string, role domain.Role) int {
		b, _ := json.Marshal(map[string]any{"name": name, "role": role})
		req := httptest.NewRequest("POST", "/projects/prj-a/members", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+tok[who])
		w := httptest.NewRecorder()
		h.Mux().ServeHTTP(w, req)
		return w.Code
	}
	if code := setMember("pam", "pam", domain.RoleMember); code != http.StatusConflict {
		t.Errorf("the last admin demoting themself: %d", code)
	}
	if code := setMember("pam", "out", domain.RoleViewer); code != 200 || p.RoleOf(&domain.User{ID: id("out")}) != domain.RoleViewer {
		t.Errorf("adding a member: %d", code)
	}
	if code := setMember("pam", "nobody", domain.RoleViewer); code != 404 {
		t.Errorf("an unknown user: %d", code)
	}
	if code := setMember("pam", "mo", "owner"); code != 400 {
		t.Errorf("an unknown role: %d", code)
	}
}

func TestMigrateToProjects(t *testing.T) {
	users := &memUsers{m: map[string]*domain.User{}}
	projects := &memProjects{m: map[string]*domain.Project{}}
	tasks := &projTasks{m: map[string]*domain.Task{
		"a1": {ID: "a1", RepoPath: "/src/app"}, "a2": {ID: "a2", RepoPath: "/src/app"},
		"b1": {ID: "b1", RepoPath: "/work/app"}, // same base name: a second project
		"c1": {ID: "c1", RepoPath: "/src/lib"}, "done": {ID: "done", RepoPath: "/src/lib", ProjectID: "prj-x"},
	}}
	h := &Handlers{Users: users, Projects: projects, Tasks: tasks}
	ctx := context.Background()
	h.AddUser(ctx, "ada", "ada-password-1", "", false, "") // first user: instance admin, needs no membership
	h.AddUser(ctx, "rae", "rae-password-1", "", false, domain.RoleReviewer)
	projects.m["prj-lib"] = &domain.Project{ID: "prj-lib", Name: "lib", Repos: []domain.ProjectRepo{{Path: "/src/lib"}}}

	created, assigned, err := h.MigrateToProjects(ctx)
	if err != nil || created != 2 || assigned != 4 {
		t.Fatalf("created %d assigned %d err %v", created, assigned, err)
	}
	if tasks.m["c1"].ProjectID != "prj-lib" || tasks.m["done"].ProjectID != "prj-x" {
		t.Error("a repository already in a project uses it; assigned tasks are left alone")
	}
	app, app2 := projects.m[tasks.m["a1"].ProjectID], projects.m[tasks.m["b1"].ProjectID]
	if app == nil || app2 == nil || app == app2 || tasks.m["a2"].ProjectID != app.ID || !app.HasRepo("/src/app") {
		t.Fatalf("projects per repository: %+v %+v", app, app2)
	}
	names := map[string]bool{app.Name: true, app2.Name: true}
	if !names["app"] || !names["app-2"] {
		t.Errorf("names: %v", names)
	}
	if app.RoleOf(&domain.User{ID: "usr-rae"}) != domain.RoleReviewer || app.RoleOf(&domain.User{ID: "usr-ada"}) != "" {
		t.Errorf("members: %+v", app.Members)
	}
	if c, a, err := h.MigrateToProjects(ctx); c != 0 || a != 0 || err != nil {
		t.Errorf("second run: %d %d %v", c, a, err)
	}
}
