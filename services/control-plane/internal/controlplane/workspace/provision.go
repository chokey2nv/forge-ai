package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Workspace struct {
	WorktreePath  string
	ContainerName string
	BaseCommit    string
}

func mktempDir(prefix string) (string, error) {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", err
	}
	return dir, nil
}
func Provision(ctx context.Context, repoPath, baseCommit, image, runID string) (*Workspace, error) {
	worktreeDir, err := mktempDir("forge-wt-")
	if err != nil {
		return nil, err
	}

	// git worktree add
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "add", "--detach", worktreeDir, baseCommit)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("worktree: %s: %w", out, err)
	}

	containerName := fmt.Sprintf("forge-worker-%s", runID)
	// Labels help cleanup after crashes; network=none; memory/cpus caps
	args := []string{
		"run", "-d", "--rm",
		"--name", containerName,
		"--label", "forge.run=" + runID,
		"--network=none",
		"--workdir", "/workspace",
		"-v", worktreeDir + ":/workspace:rw",
		"--memory", "2g", "--cpus", "2", "--pids-limit", "256",
		image, "sleep", "infinity",
	}
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker run: %s: %w", out, err)
	}

	return &Workspace{WorktreePath: worktreeDir, ContainerName: containerName, BaseCommit: baseCommit}, nil
}

func ApplyDiff(ctx context.Context, worktreePath, diff string) error {
	if diff == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "apply", "--whitespace=nowarn")
	cmd.Stdin = strings.NewReader(diff)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("apply diff: %s: %w", out, err)
	}
	return nil
}

func Cleanup(ctx context.Context, ws *Workspace, repoPath string) {
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", ws.ContainerName).Run()
	_ = exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "remove", "--force", ws.WorktreePath).Run()
}
