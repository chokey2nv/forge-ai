package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// diffOf returns the diff an edit makes on top of ref, without leaving it.
func diffOf(t *testing.T, repo, ref string, edit func(dir string)) string {
	t.Helper()
	wt := filepath.Join(t.TempDir(), "edit")
	git(t, repo, "worktree", "add", "-q", "--detach", wt, ref)
	defer git(t, repo, "worktree", "remove", "--force", wt)
	edit(wt)
	git(t, wt, "add", "-A")
	return git(t, wt, "diff", "--cached", "HEAD") + "\n"
}

func TestSubtaskBranchesMergeIntoParent(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t) // a.txt: "one"
	base := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "branch", "parent", base)
	git(t, repo, "branch", "parent-a", base)
	git(t, repo, "branch", "parent-b", base)
	parentWT := filepath.Join(t.TempDir(), "int")
	git(t, repo, "worktree", "add", "-q", parentWT, "parent")

	dA := diffOf(t, repo, base, func(d string) { write(t, filepath.Join(d, "tax.js"), "tax\n") })
	dB := diffOf(t, repo, base, func(d string) { write(t, filepath.Join(d, "format.js"), "format\n") })

	cA, err := CommitDiffOnBranch(ctx, repo, "parent-a", dA, "Add tax")
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, repo, "rev-parse", "parent-a"); got != cA || git(t, repo, "rev-parse", "parent-a~1") != base {
		t.Fatalf("parent-a should be one commit on top of base")
	}
	if _, err := CommitDiffOnBranch(ctx, repo, "parent-b", dB, "Add format"); err != nil {
		t.Fatal(err)
	}

	// Siblings started from the same base merge one after the other.
	if err := MergeIntoWorktree(ctx, parentWT, "parent-a", "integrate(a): Add tax"); err != nil {
		t.Fatal(err)
	}
	if err := MergeIntoWorktree(ctx, parentWT, "parent-b", "integrate(b): Add format"); err != nil {
		t.Fatalf("second sibling must merge too: %v", err)
	}
	if n := git(t, repo, "rev-list", "--count", "--merges", base+"..parent"); n != "2" {
		t.Errorf("parent has %s merge commits, want 2", n)
	}
	for _, f := range []string{"tax.js", "format.js"} {
		if _, err := os.Stat(filepath.Join(parentWT, f)); err != nil {
			t.Errorf("%s missing on the parent branch", f)
		}
	}
	if git(t, repo, "log", "-1", "--format=%s", "parent") != "integrate(b): Add format" {
		t.Error("merge message not used")
	}
}

func TestConflictingSiblingIsAbortedCleanly(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	base := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "branch", "parent", base)
	git(t, repo, "branch", "parent-a", base)
	git(t, repo, "branch", "parent-b", base)
	parentWT := filepath.Join(t.TempDir(), "int")
	git(t, repo, "worktree", "add", "-q", parentWT, "parent")

	dA := diffOf(t, repo, base, func(d string) { write(t, filepath.Join(d, "a.txt"), "from a\n") })
	dB := diffOf(t, repo, base, func(d string) { write(t, filepath.Join(d, "a.txt"), "from b\n") })
	if _, err := CommitDiffOnBranch(ctx, repo, "parent-a", dA, "A"); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitDiffOnBranch(ctx, repo, "parent-b", dB, "B"); err != nil {
		t.Fatal(err)
	}
	if err := MergeIntoWorktree(ctx, parentWT, "parent-a", "integrate(a)"); err != nil {
		t.Fatal(err)
	}
	before := git(t, repo, "rev-parse", "parent")
	err := MergeIntoWorktree(ctx, parentWT, "parent-b", "integrate(b)")
	if err == nil || !strings.Contains(err.Error(), "merge conflict in a.txt") {
		t.Fatalf("want a conflict naming a.txt, got %v", err)
	}
	if st := git(t, parentWT, "status", "--porcelain"); st != "" {
		t.Errorf("aborted merge must leave the worktree clean:\n%s", st)
	}
	if git(t, repo, "rev-parse", "parent") != before {
		t.Error("a failed merge must not move the parent branch")
	}
}

func TestCommitDiffOnBranchRejectsEmptyAndStale(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	base := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "branch", "child", base)
	if _, err := CommitDiffOnBranch(ctx, repo, "child", "  \n", "x"); err == nil {
		t.Error("an empty diff must not produce a commit")
	}
	// A diff that does not apply to the branch is reported, and leaves no worktree behind.
	bad := "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-nope\n+x\n"
	if _, err := CommitDiffOnBranch(ctx, repo, "child", bad, "x"); err == nil {
		t.Error("a non-applying diff must fail")
	}
	if strings.Contains(git(t, repo, "worktree", "list"), "forge-commit-") {
		t.Error("temporary worktree left behind")
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiffCommitsDetectsRenames(t *testing.T) {
	repo := newRepo(t)
	from := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "mv", "a.txt", "b.txt")
	git(t, repo, "commit", "-q", "-m", "rename")
	diff, err := DiffCommits(repo, from, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "rename from a.txt") || !strings.Contains(diff, "rename to b.txt") {
		t.Errorf("rename not detected:\n%s", diff)
	}
	if _, err := DiffCommits(repo, from, "no-such-ref"); err == nil {
		t.Error("an unknown commit should fail")
	}
}
