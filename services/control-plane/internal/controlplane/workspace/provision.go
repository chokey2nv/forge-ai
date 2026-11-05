package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	mounts, err := installDeps(ctx, worktreeDir, image, setupTimeout)
	if err != nil {
		removeWorkspaceDir(ctx, repoPath, worktreeDir)
		return nil, err
	}

	containerName := fmt.Sprintf("forge-worker-%s", runID)
	args := workerArgs(containerName, runID, worktreeDir, image, mounts...)
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", containerName).Run()
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		removeWorkspaceDir(ctx, repoPath, worktreeDir)
		return nil, fmt.Errorf("docker run: %s: %w", out, err)
	}

	return &Workspace{WorktreePath: worktreeDir, ContainerName: containerName, BaseCommit: baseCommit}, nil
}

func installDeps(ctx context.Context, worktreePath, image string, timeout time.Duration) ([]string, error) {
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	mounts, perWorkspace, ownScripts := prepareSharedDeps(sctx, worktreePath, image)

	base := filepath.Base(worktreePath)
	step := func(name, network, script string, extra ...string) error {
		// Killing the docker CLI on timeout or shutdown does not stop the
		// container, so name it and remove it explicitly on any failure, and
		// before starting: one left running by a control plane that was
		// killed would hold the name.
		RemoveContainer(ctx, name)
		args := []string{"run", "--rm",
			"--name", name,
			"--label", LabelRun + "=setup-" + base, "--label", instanceLabel(),
			"--workdir", "/workspace",
			"-v", worktreePath + ":/workspace:rw",
			"--network=" + network,
		}
		args = append(args, extra...)
		args = append(args, sandboxArgs()...)
		args = append(args, image, "sh", "-c", script)
		out, err := exec.CommandContext(sctx, "docker", args...).CombinedOutput()
		if err != nil {
			RemoveContainer(context.WithoutCancel(ctx), name)
			if errors.Is(sctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("dependency install took longer than %s; raise VERIFICATION_SETUP_TIMEOUT_SECONDS", timeout)
			}
			return fmt.Errorf("dependency install (%s network): %s: %w", network, out, err)
		}
		return nil
	}

	// Phase 13.5: after its first subtask a repository holds agent-written
	// code, which may include install hooks. Packages are fetched with the
	// network but none of their scripts run; the scripts (the dependencies'
	// and the project's own) then run with no network.
	if len(perWorkspace) > 0 {
		var store []string
		if slices.ContainsFunc(perWorkspace, func(p Project) bool { return p.PM == PNPM }) {
			var err error
			if store, err = pnpmStoreArgs(sctx, image); err != nil {
				return nil, err
			}
		}
		if err := step("forge-setup-"+base, "bridge", ForEachProject(perWorkspace, fetchCommand), store...); err != nil {
			return nil, err
		}
		if err := step("forge-setup-scripts-"+base, "none", ForEachProject(perWorkspace, scriptsCommand)); err != nil {
			return nil, err
		}
	}
	// A project on a shared volume still runs its own scripts here, offline:
	// they're agent-writable, so they never run against a writable volume.
	if len(ownScripts) > 0 {
		if err := step("forge-setup-own-"+base, "none", ForEachProject(ownScripts, func(Project) string { return runLifecycle }), mounts...); err != nil {
			return nil, err
		}
	}
	return mounts, nil
}

// prepareSharedDeps sorts a checkout's projects: those with packages on a
// shared volume (the mounts), those that install in this workspace, and those
// of the first kind with install scripts of their own. An install that fails on
// a volume falls back to this workspace, which reports it if it fails there too.
func prepareSharedDeps(ctx context.Context, worktreePath, image string) (mounts []string, perWorkspace, ownScripts []Project) {
	var id string
	for _, p := range Projects(worktreePath) {
		dir := filepath.Join(worktreePath, p.Dir)
		m, ok := readManifest(dir)
		if p.Go || !ok {
			perWorkspace = append(perWorkspace, p)
			continue
		}
		if !shareable(dir, p, m) {
			perWorkspace = append(perWorkspace, p)
			continue
		}
		if lifecycleScripts(m) {
			ownScripts = append(ownScripts, p)
		}
		if !m.hasDependencies() {
			continue
		}
		if id == "" {
			id = imageID(ctx, image)
		}
		name, err := depsVolumeName(dir, p, id)
		if err == nil {
			err = ensureDepsVolume(ctx, name, dir, p, image)
		}
		if err != nil {
			log.Printf("workspace: packages for %s not shared: %v", p.Dir, err)
			perWorkspace = append(perWorkspace, p)
			ownScripts = slices.DeleteFunc(ownScripts, func(o Project) bool { return o.Dir == p.Dir })
			continue
		}
		mounts = append(mounts, depsMountArgs(name, p)...)
	}
	return mounts, perWorkspace, ownScripts
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
