package workspace

import (
	"fmt"
	"os"
)

// Phase 13.5: every container Forge starts (agent workspaces, verification,
// a parent's combined verification, dependency setup) runs unprivileged:
//
//   - as the control plane's own user, not root (as root, a non-root
//     fallback), so files it writes in /workspace stay the user's;
//   - with no Linux capabilities and no way to gain privileges (setuid);
//   - on a read-only root filesystem: only /workspace (the bind mount) and
//     a size-limited /tmp, which holds HOME and tool caches, are writable.
//
// Network is decided per container: none for agents and verification, and
// for setup only the step that fetches packages without running their
// scripts (installDeps).
func sandboxArgs() []string {
	uid, gid := containerUser()
	return []string{
		"--user", fmt.Sprintf("%d:%d", uid, gid),
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--read-only",
		"--tmpfs", "/tmp:rw,exec,nosuid,size=1g",
		"-e", "HOME=/tmp",
		"-e", "npm_config_cache=/tmp/.npm",
		"-e", "GOCACHE=/tmp/.cache/go-build",
		"-e", "GOPATH=/tmp/go",
	}
}

// fallbackUID is who containers run as when the control plane runs as root.
const fallbackUID = 65534 // nobody

func containerUser() (uid, gid int) {
	uid, gid = os.Getuid(), os.Getgid()
	if uid <= 0 {
		return fallbackUID, fallbackUID
	}
	return uid, gid
}

// workerArgs are the docker run arguments for a long-lived, network-isolated
// container with worktree mounted at /workspace.
func workerArgs(name, runID, worktree, image string) []string {
	args := []string{
		"run", "-d", "--rm",
		"--name", name,
		"--label", LabelRun + "=" + runID, "--label", instanceLabel(),
		"--network=none",
		"--workdir", "/workspace",
		"-v", worktree + ":/workspace:rw",
		"--memory", "2g", "--cpus", "2", "--pids-limit", "256",
	}
	args = append(args, sandboxArgs()...)
	return append(args, image, "sleep", "infinity")
}
