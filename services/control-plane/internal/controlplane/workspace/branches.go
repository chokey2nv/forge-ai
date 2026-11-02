package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Subtask branches (Phase 11): each subtask's work is committed on its own
// branch, cut from the parent branch, and merged into the parent branch.

// CommitDiffOnBranch commits a subtask's recorded diff on its branch, which
// must point at the commit the diff was taken against. It works from the
// diff, not the subtask's workspace, so it also works after the workspace
// was reclaimed (e.g. a subtask accepted from ESCALATED). Returns the commit.
func CommitDiffOnBranch(ctx context.Context, repoPath, branch, diff, message string) (string, error) {
	if strings.TrimSpace(diff) == "" {
		return "", fmt.Errorf("no changes to commit on %s", branch)
	}
	wt, err := mktempDir("forge-commit-")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = exec.Command("git", "-C", repoPath, "worktree", "remove", "--force", wt).Run()
		_ = os.RemoveAll(wt)
	}()
	if out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "add", "-q", wt, branch).CombinedOutput(); err != nil {
		return "", fmt.Errorf("check out %s: %s: %w", branch, strings.TrimSpace(string(out)), err)
	}

	apply := exec.CommandContext(ctx, "git", "-C", wt, "apply", "--whitespace=nowarn")
	apply.Stdin = strings.NewReader(diff)
	if out, err := apply.CombinedOutput(); err != nil {
		return "", fmt.Errorf("apply the subtask's diff to %s: %s: %w", branch, strings.TrimSpace(string(out)), err)
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", wt, "add", "-A").CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add: %s: %w", out, err)
	}
	commit := exec.CommandContext(ctx, "git", "-C", wt, "commit", "-q", "-m", message)
	commit.Env = commitEnv(repoPath)
	if out, err := commit.CombinedOutput(); err != nil {
		return "", fmt.Errorf("commit on %s: %s: %w", branch, strings.TrimSpace(string(out)), err)
	}
	out, err := exec.CommandContext(ctx, "git", "-C", wt, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// MergeIntoWorktree merges branch into the branch checked out in worktree
// with a merge commit. On a conflict the merge is aborted, leaving the
// worktree as it was, and the conflicting paths are reported.
func MergeIntoWorktree(ctx context.Context, worktree, branch, message string) error {
	merge := exec.CommandContext(ctx, "git", "-C", worktree, "merge", "--no-ff", "--no-edit", "-m", message, branch)
	merge.Env = commitEnv(worktree)
	out, err := merge.CombinedOutput()
	if err == nil {
		return nil
	}
	conflicts, _ := exec.Command("git", "-C", worktree, "diff", "--name-only", "--diff-filter=U").Output()
	_ = exec.Command("git", "-C", worktree, "merge", "--abort").Run()
	if files := strings.Fields(string(conflicts)); len(files) > 0 {
		return fmt.Errorf("merge conflict in %s", strings.Join(files, ", "))
	}
	return fmt.Errorf("merge %s: %s: %w", branch, strings.TrimSpace(string(out)), err)
}

// commitEnv supplies a committer identity when the repository has none
// configured, so commits made on the user's behalf never fail for lack of one.
func commitEnv(repoPath string) []string {
	env := os.Environ()
	if out, _ := exec.Command("git", "-C", repoPath, "config", "user.email").Output(); strings.TrimSpace(string(out)) == "" {
		env = append(env,
			"GIT_AUTHOR_NAME=Forge", "GIT_AUTHOR_EMAIL=forge@localhost",
			"GIT_COMMITTER_NAME=Forge", "GIT_COMMITTER_EMAIL=forge@localhost")
	}
	return env
}
