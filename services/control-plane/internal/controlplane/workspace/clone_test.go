package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "first")
	return repo
}

func TestCloneAtIsSelfContained(t *testing.T) {
	repo := newRepo(t)
	// A local branch, like a parent's forge/<task> integration branch.
	git(t, repo, "checkout", "-q", "-b", "forge/task-x")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "commit", "-q", "-am", "second")
	want := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "checkout", "-q", "-")

	dir, err := mktempDir("forge-wt-")
	if err != nil {
		t.Fatal(err)
	}
	defer removeWorkspaceDir(context.Background(), repo, dir)

	if err := cloneAt(context.Background(), repo, dir, "forge/task-x"); err != nil {
		t.Fatal(err)
	}

	// .git must be a real directory, not a pointer into the host repo.
	if fi, err := os.Stat(filepath.Join(dir, ".git")); err != nil || !fi.IsDir() {
		t.Fatalf(".git is not a directory: %v", err)
	}
	if got := git(t, dir, "rev-parse", "HEAD"); got != want {
		t.Errorf("HEAD = %s, want %s", got, want)
	}
	if remotes := git(t, dir, "remote"); remotes != "" {
		t.Errorf("clone must have no remotes, got %q", remotes)
	}

	// Dependency installs must not end up in the captured diff.
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The diff the adapter captures must see agent edits.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	diff := git(t, dir, "diff", "--cached", "HEAD")
	if !strings.Contains(diff, "+three") {
		t.Errorf("diff missing edit:\n%s", diff)
	}
	if strings.Contains(diff, "node_modules") || strings.Contains(diff, "package-lock.json") {
		t.Errorf("diff includes dependency-install output:\n%s", diff)
	}
}

func TestRemoveWorkspaceDirOnlyRemovesOwnTempDirs(t *testing.T) {
	repo := newRepo(t)

	own, err := mktempDir("forge-child-")
	if err != nil {
		t.Fatal(err)
	}
	removeWorkspaceDir(context.Background(), repo, own)
	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Errorf("own workspace %s was not removed", own)
	}

	integration, err := mktempDir("forge-int-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(integration)
	removeWorkspaceDir(context.Background(), repo, integration)
	if _, err := os.Stat(integration); err != nil {
		t.Errorf("integration worktree must survive cleanup: %v", err)
	}

	for _, p := range []string{repo, "/", os.TempDir(), filepath.Join(os.TempDir(), "forge-wt-")} {
		if isRemovableWorkspace(p) {
			t.Errorf("isRemovableWorkspace(%q) = true", p)
		}
	}
}

// The parent's final review diffs base..HEAD on the integration branch; a
// symbolic base would move with the branch and produce an empty diff.
func TestResolvedBaseSurvivesIntegrationCommits(t *testing.T) {
	repo := newRepo(t)
	base, err := ResolveCommit(context.Background(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	wt, err := CreateIntegrationWorktree(repo, base, "forge/task-y")
	if err != nil {
		t.Fatal(err)
	}
	defer git(t, repo, "worktree", "remove", "--force", wt)

	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("integrated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "commit", "-q", "-am", "integrate child")

	diff, err := DiffFromBase(wt, base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+integrated") {
		t.Errorf("diff from pinned base is missing the integrated change:\n%s", diff)
	}
}
