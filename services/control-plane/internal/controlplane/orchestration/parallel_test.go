package orchestration

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/adapter"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/agent"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/statemachine"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

// Concurrency-safe fakes: provisioning runs in goroutines here.

type syncTasks struct {
	store.TaskRepo
	mu   sync.Mutex
	byID map[string]*domain.Task
}

func (m *syncTasks) status(id string) domain.TaskStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byID[id].Status
}

func (m *syncTasks) Get(_ context.Context, id string) (*domain.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.byID[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *t
	return &cp, nil
}

func (m *syncTasks) list(keep func(*domain.Task) bool) []*domain.Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*domain.Task
	for _, t := range m.byID {
		if keep(t) {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out
}

func (m *syncTasks) ListByStatus(_ context.Context, s domain.TaskStatus, _ int) ([]*domain.Task, error) {
	return m.list(func(t *domain.Task) bool { return t.Status == s }), nil
}

func (m *syncTasks) ListChildren(_ context.Context, parentID string) ([]*domain.Task, error) {
	return m.list(func(t *domain.Task) bool { return t.ParentID == parentID }), nil
}

func (m *syncTasks) CompareAndSetStatus(_ context.Context, id string, from, to domain.TaskStatus, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.byID[id]
	if t == nil || t.Status != from {
		return false, nil
	}
	t.Status = to
	return true, nil
}

func (m *syncTasks) UpdateLatestRun(_ context.Context, id, runID string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byID[id].LatestRunID = runID
	return nil
}

type syncRuns struct {
	store.RunRepo
	mu   sync.Mutex
	byID map[string]*domain.WorkflowRun
}

func (m *syncRuns) Get(_ context.Context, id string) (*domain.WorkflowRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.byID[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *r
	return &cp, nil
}

func (m *syncRuns) Create(_ context.Context, r *domain.WorkflowRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *r
	m.byID[r.ID] = &cp
	return nil
}

func (m *syncRuns) UpdateStatus(_ context.Context, id string, st domain.RunStatus, _ string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byID[id].Status = st
	return nil
}

func (m *syncRuns) UpdateWorkspace(_ context.Context, id, wt, container string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byID[id].WorktreePath, m.byID[id].ContainerName = wt, container
	return nil
}

type syncEvents struct{ store.EventRepo }

func (syncEvents) Append(context.Context, *domain.Event) error { return nil }

type parallelHarness struct {
	s        *Scheduler
	tasks    *syncTasks
	runs     *syncRuns
	release  chan struct{} // closing it lets every pending provision finish
	mu       sync.Mutex
	started  []string // run IDs the adapter was asked to start
	provBase []string // base commits provisioning was given
}

func newParallelHarness(t *testing.T, maxRuns int, children ...*domain.Task) *parallelHarness {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..")
	reg := agent.NewRegistry(filepath.Join(root, "agents"), filepath.Join(root, "skills"), nil)
	if err := reg.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	h := &parallelHarness{release: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.started = append(h.started, r.URL.Path)
		h.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)

	h.tasks = &syncTasks{byID: map[string]*domain.Task{
		"p": {ID: "p", Status: domain.StatusDispatched, RepoPath: "/repo", IntegrationBranch: "forge/p"},
	}}
	for _, c := range children {
		h.tasks.byID[c.ID] = c
	}
	h.runs = &syncRuns{byID: map[string]*domain.WorkflowRun{}}
	sm := &statemachine.Service{Tasks: h.tasks, Events: syncEvents{}}
	h.s = &Scheduler{
		Tasks: h.tasks, Runs: h.runs, Events: syncEvents{}, SM: sm,
		Registry: reg, Adapter: &adapter.Client{BaseURL: srv.URL},
		MaxConcurrentRuns: maxRuns,
		ResolveCommit: func(context.Context, string, string) (string, error) {
			return "0123abcd", nil
		},
		Provision: func(_ context.Context, _, base, _, _, runID string, _ int) (*workspace.Workspace, error) {
			h.mu.Lock()
			h.provBase = append(h.provBase, base)
			h.mu.Unlock()
			<-h.release // a slow clone + npm install
			return &workspace.Workspace{WorktreePath: "/tmp/wt-" + runID, ContainerName: "c-" + runID}, nil
		},
	}
	return h
}

func child(id string, deps ...string) *domain.Task {
	return &domain.Task{ID: id, ParentID: "p", Status: domain.StatusCreated, AssignedAgent: "implementer", DependsOn: deps}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func TestTickDoesNotWaitForProvisioning(t *testing.T) {
	h := newParallelHarness(t, 0, child("a"), child("b"))

	done := make(chan struct{})
	go func() { h.s.tick(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("tick blocked on provisioning")
	}

	// Both independent children are claimed and provisioning at once.
	for _, id := range []string{"a", "b"} {
		if got := h.tasks.status(id); got != domain.StatusDispatched {
			t.Errorf("child %s = %s, want DISPATCHED while provisioning", id, got)
		}
		run, err := h.runs.Get(context.Background(), "run-"+id)
		if err != nil || run.Status != domain.RunProvisioning {
			t.Errorf("run of %s: %v %v, want PROVISIONING (ignored by the stale-run watcher)", id, run, err)
		}
	}

	close(h.release)
	for _, id := range []string{"a", "b"} {
		eventually(t, id+" running", func() bool { return h.tasks.status(id) == domain.StatusRunning })
		run, _ := h.runs.Get(context.Background(), "run-"+id)
		if run.Status != domain.RunRunning || run.ContainerName != "c-"+id {
			t.Errorf("run of %s after provisioning: status %s container %q", id, run.Status, run.ContainerName)
		}
	}
	eventually(t, "both agents started", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.started) == 2
	})
}

func TestBaseCommitIsPinnedAtDispatch(t *testing.T) {
	h := newParallelHarness(t, 0, child("a"))
	close(h.release)
	h.s.tick(context.Background())

	run, err := h.runs.Get(context.Background(), "run-a")
	if err != nil {
		t.Fatal(err)
	}
	if run.BaseCommit != "0123abcd" {
		t.Errorf("run base = %q, want the integration HEAD SHA", run.BaseCommit)
	}
	eventually(t, "provisioned", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.provBase) == 1 && h.provBase[0] == "0123abcd"
	})
}

func TestCapacityLimitsLiveWorkspaces(t *testing.T) {
	h := newParallelHarness(t, 2, child("a"), child("b"), child("c"))
	// Another root task's agent is already holding one workspace.
	h.tasks.byID["other"] = &domain.Task{ID: "other", Status: domain.StatusRunning}

	h.s.tick(context.Background())

	claimed := 0
	for _, id := range []string{"a", "b", "c"} {
		if h.tasks.status(id) == domain.StatusDispatched {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("claimed %d children with cap 2 and 1 workspace in use, want 1", claimed)
	}

	// A second tick finds the cap reached: the claimed child is provisioning.
	h.s.tick(context.Background())
	claimed = 0
	for _, id := range []string{"a", "b", "c"} {
		if h.tasks.status(id) != domain.StatusCreated {
			claimed++
		}
	}
	if claimed != 1 {
		t.Errorf("a full cap must not admit more children, got %d claimed", claimed)
	}
	close(h.release)
}

func TestDependentWaitsForItsDependencies(t *testing.T) {
	h := newParallelHarness(t, 0, child("a"), child("b"), child("c", "a", "b"))
	close(h.release)
	h.s.tick(context.Background())

	if got := h.tasks.status("c"); got != domain.StatusCreated {
		t.Errorf("c depends on a and b, which are not integrated; c = %s, want CREATED", got)
	}
	for _, id := range []string{"a", "b"} {
		if got := h.tasks.status(id); got == domain.StatusCreated {
			t.Errorf("independent child %s was not dispatched", id)
		}
	}
}
