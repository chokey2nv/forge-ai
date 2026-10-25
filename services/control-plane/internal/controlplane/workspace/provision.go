package workspace

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Workspace struct {
	WorktreePath  string
	ContainerName string
	BaseCommit    string
}
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

func RunInContainer(ctx context.Context, containerName string, argv []string, timeout time.Duration) ExecResult {
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()

	cmd := exec.CommandContext(sctx, "docker", append([]string{"exec", containerName}, argv...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			exit = -1
		}
	}

	return ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exit,
		Duration: time.Since(start),
	}
}
func ProvisionWithSetup(ctx context.Context, repoPath, baseCommit, image, runID string, setupTimeout time.Duration) (*Workspace, error) {
	worktreeDir, err := mktempDir("forge-wt-")
	if err != nil {
		return nil, err
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "add", "--detach", worktreeDir, baseCommit).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("worktree: %s: %w", out, err)
	}

	// Setup container: has network, installs deps, then disappears.
	if err := installDeps(ctx, worktreeDir, image, setupTimeout); err != nil {
		_ = exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "remove", "--force", worktreeDir).Run()
		return nil, err
	}

	containerName := fmt.Sprintf("forge-worker-%s", runID)
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
		_ = exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "remove", "--force", worktreeDir).Run()
		return nil, fmt.Errorf("docker run: %s: %w", out, err)
	}

	return &Workspace{WorktreePath: worktreeDir, ContainerName: containerName, BaseCommit: baseCommit}, nil
}

func installDeps(ctx context.Context, worktreePath, image string, timeout time.Duration) error {
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := exec.CommandContext(sctx, "docker", "run", "--rm",
		"--workdir", "/workspace",
		"-v", worktreePath+":/workspace:rw",
		"--network=bridge",
		image,
		"sh", "-c", `
			if [ -f package.json ]; then npm install --no-audit --no-fund; fi
			if [ -f go.mod ]; then go mod download; fi
		`,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("dependency install: %s: %w", out, err)
	}
	return nil
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
