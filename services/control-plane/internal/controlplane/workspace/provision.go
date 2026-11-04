package workspace

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	if err := cloneAt(ctx, repoPath, worktreeDir, baseCommit); err != nil {
		removeWorkspaceDir(ctx, repoPath, worktreeDir)
		return nil, fmt.Errorf("workspace: %w", err)
	}

	// Setup container: has network, installs deps, then disappears.
	if err := installDeps(ctx, worktreeDir, image, setupTimeout); err != nil {
		removeWorkspaceDir(ctx, repoPath, worktreeDir)
		return nil, err
	}

	containerName := fmt.Sprintf("forge-worker-%s", runID)
	args := workerArgs(containerName, runID, worktreeDir, image)
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", containerName).Run()
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		removeWorkspaceDir(ctx, repoPath, worktreeDir)
		return nil, fmt.Errorf("docker run: %s: %w", out, err)
	}

	return &Workspace{WorktreePath: worktreeDir, ContainerName: containerName, BaseCommit: baseCommit}, nil
}

func installDeps(ctx context.Context, worktreePath, image string, timeout time.Duration) error {
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	base := filepath.Base(worktreePath)
	step := func(name, network, script string) error {
		// Killing the docker CLI on timeout or shutdown does not stop the
		// container, so name it and remove it explicitly on any failure.
		args := []string{"run", "--rm",
			"--name", name,
			"--label", LabelRun + "=setup-" + base, "--label", instanceLabel(),
			"--workdir", "/workspace",
			"-v", worktreePath + ":/workspace:rw",
			"--network=" + network,
		}
		args = append(args, sandboxArgs()...)
		args = append(args, image, "sh", "-c", script)
		out, err := exec.CommandContext(sctx, "docker", args...).CombinedOutput()
		if err != nil {
			RemoveContainer(context.WithoutCancel(ctx), name)
			return fmt.Errorf("dependency install (%s network): %s: %w", network, out, err)
		}
		return nil
	}

	// Phase 13.5: after its first subtask a repository holds agent-written
	// code, which may include install hooks. Packages are fetched with the
	// network but none of their scripts run; the scripts (the dependencies'
	// and the project's own) then run with no network.
	if err := step("forge-setup-"+base, "bridge", `
		if [ -f package.json ]; then npm install --no-audit --no-fund --ignore-scripts; fi
		if [ -f go.mod ]; then go mod download; fi
	`); err != nil {
		return err
	}
	return step("forge-setup-scripts-"+base, "none", `
		if [ -f package.json ]; then
			npm rebuild --no-audit --no-fund || exit 1
			for s in preinstall install postinstall prepare; do npm run --if-present "$s" || exit 1; done
		fi
	`)
}

func mktempDir(prefix string) (string, error) {
	dir, err := os.MkdirTemp("", prefix+instanceTag())
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

	if err := cloneAt(ctx, repoPath, worktreeDir, baseCommit); err != nil {
		removeWorkspaceDir(ctx, repoPath, worktreeDir)
		return nil, fmt.Errorf("workspace: %w", err)
	}

	containerName := fmt.Sprintf("forge-worker-%s", runID)
	// Labels help cleanup after crashes; network=none; memory/cpus caps
	args := workerArgs(containerName, runID, worktreeDir, image)
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", containerName).Run()
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
	RemoveContainer(ctx, ws.ContainerName)
	removeWorkspaceDir(ctx, repoPath, ws.WorktreePath)
}
