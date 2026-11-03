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

	// Drop stale worktree metadata before creating a new one.
	if out, err := exec.Command("git", "-C", repoPath, "worktree", "prune").CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree prune: %s: %w", out, err)
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

// AddWorktreeForBranch checks out an existing branch in a new integration
// worktree, without moving the branch.
func AddWorktreeForBranch(repoPath, branch string) (string, error) {
	worktree, err := mktempDir("forge-int-")
	if err != nil {
		return "", err
	}
	if out, err := exec.Command("git", "-C", repoPath, "worktree", "prune").CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree prune: %s: %w", out, err)
	}
	if out, err := exec.Command("git", "-C", repoPath, "worktree", "add", worktree, branch).CombinedOutput(); err != nil {
		return "", fmt.Errorf("worktree add %s: %s: %w", branch, out, err)
	}
	return worktree, nil
}

// ProvisionChild creates a workspace for a child task from the parent's
// integration branch HEAD, installs dependencies, and starts a
// network-isolated container. childBranch is unused: a child's work reaches
// the parent as a diff, committed once on the integration branch.
func ProvisionChild(ctx context.Context, parentRepoPath, integrationBranch, childBranch, image, runID string, setupTimeoutSeconds int) (*Workspace, error) {
	worktree, err := mktempDir("forge-child-")
	if err != nil {
		return nil, err
	}

	// Starts from the *current* integration branch HEAD.
	if err := cloneAt(ctx, parentRepoPath, worktree, integrationBranch); err != nil {
		removeWorkspaceDir(ctx, parentRepoPath, worktree)
		return nil, fmt.Errorf("child workspace: %w", err)
	}

	if err := installDeps(ctx, worktree, image, setupDuration(setupTimeoutSeconds)); err != nil {
		removeWorkspaceDir(ctx, parentRepoPath, worktree)
		return nil, err
	}

	containerName := fmt.Sprintf("forge-worker-%s", runID)
	args := []string{
		"run", "-d", "--rm",
		"--name", containerName,
		"--label", LabelRun + "=" + runID, "--label", instanceLabel(),
		"--network=none",
		"--workdir", "/workspace",
		"-v", worktree + ":/workspace:rw",
		"--memory", "2g", "--cpus", "2", "--pids-limit", "256",
		image, "sleep", "infinity",
	}
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		removeWorkspaceDir(ctx, parentRepoPath, worktree)
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
		"--label", LabelRun + "=" + runID, "--label", instanceLabel(),
		"--network=none",
		"--workdir", "/workspace",
		"-v", integrationWorktree + ":/workspace:rw",
		"--memory", "2g", "--cpus", "2", "--pids-limit", "256",
		image, "sleep", "infinity",
	}
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", containerName).Run()
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

// DiffCommits is the change from one commit to another in repo, with renames
// detected.
func DiffCommits(repo, from, to string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "diff", "--no-color", "--no-ext-diff", "-M", from, to).Output()
	if err != nil {
		return "", fmt.Errorf("git diff %s %s: %w", from, to, err)
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

// RemoveIntegrationWorktree drops a parent's integration worktree after its
// branch is delivered. The branch stays.
func RemoveIntegrationWorktree(repoPath, worktree string) {
	_ = exec.Command("git", "-C", repoPath, "worktree", "remove", "--force", worktree).Run()
	_ = exec.Command("git", "-C", repoPath, "worktree", "prune").Run()
}
