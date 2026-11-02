package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// childDiff makes a change on a scratch branch from base, returns its diff
// (as the adapter would capture it), and leaves HEAD back on the start branch.
func childDiff(t *testing.T, repo, base string, edit func()) string {
	t.Helper()
	start := git(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
	git(t, repo, "checkout", "-q", "-b", "child-"+strings.ReplaceAll(t.Name(), "/", "-"), base)
	edit()
	git(t, repo, "add", "-A")
	diff := git(t, repo, "diff", "--cached", "HEAD")
	git(t, repo, "reset", "-q", "--hard")
	git(t, repo, "checkout", "-q", start)
	return diff + "\n"
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Integration repo with a sibling already integrated on top of base.
func integrationRepo(t *testing.T) (repo, base string) {
	t.Helper()
	repo = t.TempDir()
	git(t, repo, "init", "-q")
	write(t, filepath.Join(repo, "price.js"), "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\n")
	write(t, filepath.Join(repo, "other.js"), "x\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "base")
	base = git(t, repo, "rev-parse", "HEAD")

	// The sibling integrated first: it changed the top of price.js.
	write(t, filepath.Join(repo, "price.js"), "LINE1 sibling\nline2\nline3\nline4\nline5\nline6\nline7\nline8\n")
	git(t, repo, "commit", "-q", "-am", "integrate sibling")
	return repo, base
}

func TestApplyDiff3Way(t *testing.T) {
	t.Run("other file", func(t *testing.T) {
		repo, base := integrationRepo(t)
		d := childDiff(t, repo, base, func() { write(t, filepath.Join(repo, "other.js"), "y\n") })
		if err := ApplyDiff3Way(repo, d); err != nil {
			t.Fatalf("disjoint change must apply: %v", err)
		}
	})

	t.Run("same file, other region merges 3-way", func(t *testing.T) {
		repo, base := integrationRepo(t)
		d := childDiff(t, repo, base, func() {
			write(t, filepath.Join(repo, "price.js"), "line1\nline2\nline3\nline4\nline5\nline6\nline7\nLINE8 child\n")
		})
		if err := ApplyDiff3Way(repo, d); err != nil {
			t.Fatalf("non-overlapping hunk after HEAD moved must merge: %v", err)
		}
		got, _ := os.ReadFile(filepath.Join(repo, "price.js"))
		if !strings.Contains(string(got), "LINE1 sibling") || !strings.Contains(string(got), "LINE8 child") {
			t.Errorf("merge lost a side:\n%s", got)
		}
	})

	t.Run("conflict leaves the worktree clean", func(t *testing.T) {
		repo, base := integrationRepo(t)
		d := childDiff(t, repo, base, func() {
			write(t, filepath.Join(repo, "price.js"), "LINE1 child\nline2\nline3\nline4\nline5\nline6\nline7\nline8\n")
			write(t, filepath.Join(repo, "new.js"), "added by child\n")
		})
		if err := ApplyDiff3Way(repo, d); err == nil {
			t.Fatal("overlapping edit must conflict")
		}
		if st := git(t, repo, "status", "--porcelain"); st != "" {
			t.Errorf("conflict must not leave changes behind:\n%s", st)
		}
	})

	t.Run("new file", func(t *testing.T) {
		repo, base := integrationRepo(t)
		d := childDiff(t, repo, base, func() { write(t, filepath.Join(repo, "cart.js"), "module.exports = {};\n") })
		if err := ApplyDiff3Way(repo, d); err != nil {
			t.Fatalf("new file must apply: %v", err)
		}
	})
}
