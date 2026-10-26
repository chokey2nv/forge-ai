package workspace

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// CreateIntegrationWorktree creates a git worktree for a parent task's
// integration branch, seeded from baseCommit. The branch is created if absent.
func CreateIntegrationWorktree(repoPath, baseCommit, branch string) (string, error) {
	worktree, err := mktempDir("forge-int-")
	if err != nil {
		return "", err
	}

	// Create or checkout the branch in a new worktree.
	out, err := exec.Command(
		"git", "-C", repoPath,
		"worktree", "add", "-B", branch, worktree, baseCommit,
	).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("worktree add -B %s: %s: %w", branch, out, err)
	}
	return worktree, nil
}

// ProvisionChild creates a worktree for a child task, starting from the
// parent's integration branch HEAD, installs dependencies, and starts a
// network-isolated container.
func ProvisionChild(ctx context.Context, parentRepoPath, integrationBranch, childBranch, image, runID string, setupTimeoutSeconds int) (*Workspace, error) {
	worktree, err := mktempDir("forge-child-")
	if err != nil {
		return nil, err
	}

	// Child worktree branches off the *current* integration branch HEAD.
	out, err := exec.Command(
		"git", "-C", parentRepoPath,
		"worktree", "add", "-B", childBranch, worktree, integrationBranch,
	).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("child worktree add: %s: %w", out, err)
	}

	if err := installDeps(ctx, worktree, image, setupDuration(setupTimeoutSeconds)); err != nil {
		_ = exec.Command("git", "-C", parentRepoPath, "worktree", "remove", "--force", worktree).Run()
		return nil, err
	}

	containerName := fmt.Sprintf("forge-worker-%s", runID)
	args := []string{
		"run", "-d", "--rm",
		"--name", containerName,
		"--label", "forge.run=" + runID,
		"--network=none",
		"--workdir", "/workspace",
		"-v", worktree + ":/workspace:rw",
		"--memory", "2g", "--cpus", "2", "--pids-limit", "256",
		image, "sleep", "infinity",
	}
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		_ = exec.Command("git", "-C", parentRepoPath, "worktree", "remove", "--force", worktree).Run()
		return nil, fmt.Errorf("docker run: %s: %w", out, err)
	}

	return &Workspace{WorktreePath: worktree, ContainerName: containerName, BaseCommit: integrationBranch}, nil
}

// ProvisionParentContainer starts a network-isolated container for a parent's
// integration worktree (used after all children integrate, for combined verification).
func ProvisionParentContainer(ctx context.Context, repoPath, integrationWorktree, image, runID string, setupTimeoutSeconds int) (*Workspace, error) {
	if err := installDeps(ctx, integrationWorktree, image, setupDuration(setupTimeoutSeconds)); err != nil {
		return nil, err
	}

	containerName := fmt.Sprintf("forge-worker-%s", runID)
	args := []string{
		"run", "-d", "--rm",
		"--name", containerName,
		"--label", "forge.run=" + runID,
		"--network=none",
		"--workdir", "/workspace",
		"-v", integrationWorktree + ":/workspace:rw",
		"--memory", "2g", "--cpus", "2", "--pids-limit", "256",
		image, "sleep", "infinity",
	}
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker run: %s: %w", out, err)
	}
	return &Workspace{WorktreePath: integrationWorktree, ContainerName: containerName, BaseCommit: ""}, nil
}

func setupDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return 120 * time.Second
	}
	return time.Duration(seconds) * time.Second
}

// DiffFromBase returns `git diff <base>..HEAD` from the given worktree.
func DiffFromBase(worktree, base string) (string, error) {
	out, err := exec.Command("git", "-C", worktree, "diff", base+"..HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git diff %s..HEAD: %w", base, err)
	}
	return string(out), nil
}

// CommitAll stages everything and commits, used by the Integrator.
func CommitAll(worktree, message string) error {
	if out, err := exec.Command("git", "-C", worktree, "add", "-A").CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %s: %w", out, err)
	}
	if out, err := exec.Command("git", "-C", worktree, "commit", "-m", message, "--allow-empty").CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %s: %w", out, err)
	}
	return nil
}
