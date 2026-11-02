package delivery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// workRepo: main with a.txt, and forge/w one commit ahead (w.txt).
func workRepo(t *testing.T) string {
	t.Helper()
	repo := newRepo(t)
	run(t, repo, "checkout", "-q", "-b", "forge/w")
	if err := os.WriteFile(filepath.Join(repo, "w.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-q", "-m", "work")
	run(t, repo, "checkout", "-q", "main")
	return repo
}

func TestMergeLocallyIntoCheckedOutTarget(t *testing.T) {
	ctx := context.Background()
	repo := workRepo(t) // main is checked out in the repo itself, clean
	before := run(t, repo, "rev-parse", "main")

	merge, err := MergeLocally(ctx, repo, "main", "forge/w", "Forge: work (task 1)")
	if err != nil {
		t.Fatal(err)
	}
	if run(t, repo, "rev-parse", "main") != merge {
		t.Error("main not moved to the merge commit")
	}
	parents := strings.Fields(run(t, repo, "rev-list", "--parents", "-n", "1", merge))
	if len(parents) != 3 || parents[1] != before || parents[2] != run(t, repo, "rev-parse", "forge/w") {
		t.Errorf("merge parents = %v, want main's old tip and the work branch", parents[1:])
	}
	if _, err := os.Stat(filepath.Join(repo, "w.txt")); err != nil {
		t.Error("the checked-out working tree was not updated")
	}
	if st := run(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("checkout left dirty: %s", st)
	}
}

func TestMergeLocallyIntoTargetNotCheckedOut(t *testing.T) {
	ctx := context.Background()
	repo := workRepo(t)
	run(t, repo, "checkout", "-q", "--detach") // main checked out nowhere
	merge, err := MergeLocally(ctx, repo, "main", "forge/w", "m")
	if err != nil {
		t.Fatal(err)
	}
	if run(t, repo, "rev-parse", "main") != merge {
		t.Error("main not moved")
	}
}

func TestMergeLocallyRefusesDirtyCheckout(t *testing.T) {
	ctx := context.Background()
	repo := workRepo(t)
	before := run(t, repo, "rev-parse", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("my edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := MergeLocally(ctx, repo, "main", "forge/w", "m")
	if !errors.Is(err, ErrTargetDirty) {
		t.Fatalf("want ErrTargetDirty, got %v", err)
	}
	if run(t, repo, "rev-parse", "main") != before {
		t.Error("main must not move")
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(b) != "my edit\n" {
		t.Error("the user's uncommitted edit was touched")
	}
}

func TestMergeLocallyNeedsTargetMergedIn(t *testing.T) {
	ctx := context.Background()
	repo := workRepo(t)
	// main advances after the work branched off.
	if err := os.WriteFile(filepath.Join(repo, "m.txt"), []byte("main moved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-q", "-m", "moved")
	if _, err := MergeLocally(ctx, repo, "main", "forge/w", "m"); !errors.Is(err, ErrTargetMoved) {
		t.Fatalf("want ErrTargetMoved, got %v", err)
	}
	if !IsAncestor(ctx, repo, "forge/w~1", "main") || IsAncestor(ctx, repo, "main", "forge/w") {
		t.Error("IsAncestor disagrees with the history")
	}
}

func TestPushAndPullRequest(t *testing.T) {
	ctx := context.Background()
	repo := workRepo(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	run(t, t.TempDir(), "init", "-q", "--bare", origin)
	run(t, repo, "remote", "add", "origin", origin)

	if err := Push(ctx, repo, "origin", "forge/w"); err != nil {
		t.Fatal(err)
	}
	if run(t, origin, "rev-parse", "forge/w") != run(t, repo, "rev-parse", "forge/w") {
		t.Error("branch not on origin")
	}

	// A gh stand-in: records its arguments, answers create and view.
	dir := t.TempDir()
	log := filepath.Join(dir, "gh.log")
	gh := filepath.Join(dir, "gh")
	script := `#!/bin/sh
echo "$*" >>` + log + `
case "$1 $2" in
  "pr create") echo "Creating pull request..."; echo "https://github.com/owner/app/pull/42" ;;
  "pr view") echo '{"state":"MERGED","mergeCommit":{"oid":"abc123"}}' ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(gh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	remote := &Remote{Name: "origin", Host: "github.com", Repo: "owner/app"}
	url, n, err := OpenPR(ctx, gh, repo, remote, "main", "forge/w", "Fix it", "body text")
	if err != nil || url != "https://github.com/owner/app/pull/42" || n != 42 {
		t.Fatalf("OpenPR = %q %d %v", url, n, err)
	}
	args, _ := os.ReadFile(log)
	for _, want := range []string{"pr create --base main --head forge/w --title Fix it --body body text --repo github.com/owner/app"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("gh called with %q, want %q", args, want)
		}
	}

	state, mc, err := PRState(ctx, gh, repo, url)
	if err != nil || state != "MERGED" || mc != "abc123" {
		t.Errorf("PRState = %q %q %v", state, mc, err)
	}
}

func TestPushNeverForces(t *testing.T) {
	ctx := context.Background()
	repo := workRepo(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	run(t, t.TempDir(), "init", "-q", "--bare", origin)
	run(t, repo, "remote", "add", "origin", origin)
	if err := Push(ctx, repo, "origin", "forge/w"); err != nil {
		t.Fatal(err)
	}
	// Someone else's commit lands on origin's forge/w; our branch is behind.
	other := filepath.Join(t.TempDir(), "other")
	run(t, t.TempDir(), "clone", "-q", "-b", "forge/w", origin, other)
	if err := os.WriteFile(filepath.Join(other, "x.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, other, "add", "-A")
	run(t, other, "commit", "-q", "-m", "theirs")
	run(t, other, "push", "-q", "origin", "forge/w")
	run(t, repo, "commit", "-q", "--allow-empty", "-m", "ours")
	run(t, repo, "branch", "-f", "forge/w", "HEAD")
	if err := Push(ctx, repo, "origin", "forge/w"); err == nil {
		t.Error("a non-fast-forward push must fail, not overwrite")
	}
}
