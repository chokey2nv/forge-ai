package orchestration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/statemachine"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

// subtaskDiff is the diff of writing file=content on top of base.
func subtaskDiff(t *testing.T, repo, base, file, content string) string {
	t.Helper()
	wt := filepath.Join(t.TempDir(), "edit")
	gitIn(t, repo, "worktree", "add", "-q", "--detach", wt, base)
	defer gitIn(t, repo, "worktree", "remove", "--force", wt)
	if err := os.WriteFile(filepath.Join(wt, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", "-A")
	return gitIn(t, wt, "diff", "--cached", "HEAD") + "\n"
}

func TestIntegrateMergesSubtaskBranches(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "price.js"), []byte("price\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "init")
	base := gitIn(t, repo, "rev-parse", "HEAD")
	gitIn(t, repo, "branch", "forge/p", base)
	wt := filepath.Join(t.TempDir(), "int")
	gitIn(t, repo, "worktree", "add", "-q", wt, "forge/p")

	tasks := &syncTasks{byID: map[string]*domain.Task{
		"p": {ID: "p", Status: domain.StatusDispatched, RepoPath: repo, IntegrationBranch: "forge/p", IntegrationWorktree: wt},
	}}
	mk := func(id, title string) *domain.Task {
		c := &domain.Task{ID: id, ParentID: "p", Title: title, Status: domain.StatusAgentReview, Branch: "forge/p-" + id}
		tasks.byID[id] = c
		return c
	}
	ig := &Integrator{Tasks: tasks, Events: syncEvents{}, SM: &statemachine.Service{Tasks: tasks, Events: syncEvents{}}}

	// Two parallel siblings from the same base; neither branch exists yet
	// (subtasks dispatched before branches existed get one here).
	a, b := mk("a", "Add tax"), mk("b", "Add format")
	if err := ig.Integrate(ctx, a, base, subtaskDiff(t, repo, base, "tax.js", "tax\n")); err != nil {
		t.Fatal(err)
	}
	if err := ig.Integrate(ctx, b, base, subtaskDiff(t, repo, base, "format.js", "format\n")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if s := tasks.status(id); s != domain.StatusIntegrated {
			t.Errorf("%s = %s, want INTEGRATED", id, s)
		}
		if n := gitIn(t, repo, "rev-list", "--count", base+"..forge/p-"+id); n != "1" {
			t.Errorf("forge/p-%s has %s commits on its base, want 1", id, n)
		}
	}
	if n := gitIn(t, repo, "rev-list", "--count", "--merges", base+"..forge/p"); n != "2" {
		t.Errorf("parent branch has %s merges, want 2", n)
	}
	if !strings.Contains(gitIn(t, repo, "log", "--format=%s", "forge/p"), "integrate(a): Add tax") {
		t.Error("merge message must name the subtask")
	}

	// A sibling that edits what a merged one edited conflicts and blocks.
	c := mk("c", "Change price")
	cDiff := subtaskDiff(t, repo, base, "tax.js", "different tax\n")
	if err := ig.Integrate(ctx, c, base, cDiff); err == nil {
		t.Fatal("conflicting sibling must fail to integrate")
	}
	if tasks.status("c") != domain.StatusBlocked || tasks.status("p") != domain.StatusBlocked {
		t.Errorf("conflict must block child and parent: c=%s p=%s", tasks.status("c"), tasks.status("p"))
	}
	if st := gitIn(t, wt, "status", "--porcelain"); st != "" {
		t.Errorf("integration worktree left dirty:\n%s", st)
	}
}
