package delivery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Fix the off-by-one pagination bug and add regression tests": "fix-the-off-by-one-pagination-bug-and",
		"  Add taxFor(amount, ratePercent) in src/tax.js!":           "add-taxfor-amount-ratepercent-in-src-tax",
		"Ünïcode only ✓✓":                                            "n-code-only",
		"":                                                           "task",
		"!!!":                                                        "task",
		strings.Repeat("x", 60):                                      strings.Repeat("x", 40),
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
		if len(Slug(in)) > maxSlugLen {
			t.Errorf("Slug(%q) longer than %d", in, maxSlugLen)
		}
	}
	if got := ParentBranchName("task-1a2b3c4d", "Fix paginate"); got != "forge/fix-paginate-1a2b3c4d" {
		t.Errorf("ParentBranchName = %q", got)
	}
}

func TestResolveTarget(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	run(t, repo, "branch", "release")
	tip := run(t, repo, "rev-parse", "main")

	if b, sha, err := ResolveTarget(ctx, repo, "", "HEAD"); err != nil || b != "main" || sha != tip {
		t.Errorf("HEAD: got %q %q %v, want main %s", b, sha, err, tip)
	}
	if b, _, err := ResolveTarget(ctx, repo, "", "release"); err != nil || b != "release" {
		t.Errorf("baseCommit naming a branch: got %q %v", b, err)
	}
	if b, _, err := ResolveTarget(ctx, repo, "release", "HEAD"); err != nil || b != "release" {
		t.Errorf("explicit target: got %q %v", b, err)
	}
	if _, _, err := ResolveTarget(ctx, repo, "nope", ""); err == nil {
		t.Error("a missing target branch must fail")
	}
	if _, _, err := ResolveTarget(ctx, repo, "", tip); err == nil || !strings.Contains(err.Error(), "set targetBranch") {
		t.Errorf("a SHA base needs an explicit target: %v", err)
	}
	run(t, repo, "checkout", "-q", "--detach")
	if _, _, err := ResolveTarget(ctx, repo, "", "HEAD"); err == nil || !strings.Contains(err.Error(), "detached") {
		t.Errorf("detached HEAD needs an explicit target: %v", err)
	}
}

func TestCreateBranchNeverMovesAnExistingOne(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	tip := run(t, repo, "rev-parse", "main")
	if err := CreateBranch(ctx, repo, "forge/fix-1", tip); err != nil {
		t.Fatal(err)
	}
	if got := run(t, repo, "rev-parse", "forge/fix-1"); got != tip {
		t.Errorf("branch at %s, want %s", got, tip)
	}
	if err := CreateBranch(ctx, repo, "forge/fix-1", tip); err == nil {
		t.Error("creating an existing branch must fail, not move it")
	}
	if err := CreateBranch(ctx, repo, "forge/bad..name", tip); err == nil {
		t.Error("an invalid branch name must fail")
	}
}
