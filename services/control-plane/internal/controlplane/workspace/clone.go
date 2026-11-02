package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Prefixes of the per-run directories this package creates and may delete.
// The parent's integration worktree ("forge-int-") is deliberately absent.
var removablePrefixes = []string{"forge-wt-", "forge-child-"}

// localExcludes keeps dependency-install output out of captured diffs. It goes
// in .git/info/exclude, which affects only untracked files, so a lockfile the
// repository already tracks still shows up when the agent changes it.
const localExcludes = "node_modules/\npackage-lock.json\npnpm-lock.yaml\nyarn.lock\n"

// cloneAt fills dir with a standalone clone of repoPath checked out (detached)
// at commitish.
//
// Agent containers mount only dir. A `git worktree` would leave dir/.git as a
// pointer into repoPath/.git on the host, which the container cannot see, so
// every git command in it fails. A clone carries its own .git, and the
// container gets no write path into the host repository (refs, hooks).
func cloneAt(ctx context.Context, repoPath, dir, commitish string) error {
	// Resolve on the host: local branches (e.g. forge/<task>) become
	// remote-tracking refs in the clone and would not resolve by name.
	sha, err := ResolveCommit(ctx, repoPath, commitish)
	if err != nil {
		return err
	}

	steps := [][]string{
		// --no-hardlinks: hardlinked objects would let the container modify
		// the host repository's object files.
		{"git", "clone", "--quiet", "--no-hardlinks", "--no-checkout", repoPath, dir},
		{"git", "-C", dir, "checkout", "--quiet", "--detach", sha},
		// The origin path is a host path; nothing in the container should use it.
		{"git", "-C", dir, "remote", "remove", "origin"},
	}
	for _, argv := range steps {
		if out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %s: %w", strings.Join(argv[:3], " "), out, err)
		}
	}

	// Without this, `git add -A` stages node_modules and the diff outgrows
	// Mongo's 16MB document limit, so checkpoints silently fail to save.
	exclude := filepath.Join(dir, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(localExcludes)
	return err
}

// ResolveCommit turns a commit-ish such as "HEAD" or a branch name into a SHA.
func ResolveCommit(ctx context.Context, repoPath, commitish string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "--verify", commitish+"^{commit}").Output()
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", commitish, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// removeWorkspaceDir deletes a per-run workspace directory. It refuses any
// path that is not one of this package's temp directories, since the path
// comes from the database.
func removeWorkspaceDir(ctx context.Context, repoPath, dir string) {
	if dir == "" {
		return
	}
	// Workspaces created before the switch to clones are git worktrees;
	// drop their registration in the host repository too.
	_ = exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "remove", "--force", dir).Run()

	if !isRemovableWorkspace(dir) {
		return
	}
	_ = os.RemoveAll(dir)
}

func isRemovableWorkspace(dir string) bool {
	clean := filepath.Clean(dir)
	if filepath.Dir(clean) != filepath.Clean(os.TempDir()) {
		return false
	}
	base := filepath.Base(clean)
	for _, p := range removablePrefixes {
		if strings.HasPrefix(base, p) && len(base) > len(p) {
			return true
		}
	}
	return false
}
