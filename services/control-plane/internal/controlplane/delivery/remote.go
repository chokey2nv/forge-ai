package delivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// A repository with a remote delivers there, so the remote's target branch is
// what work must start from and merge with. A human merges pull requests on
// the forge, which leaves the local branch behind; Forge never moves it.

// ErrDiverged: the local target and the remote's each hold commits the other lacks.
var ErrDiverged = errors.New("the local branch and the remote's have diverged")

const fetchTimeout = time.Minute

// FetchTarget fetches the remote's target and returns its tip. It's kept under
// its own ref rather than FETCH_HEAD, which another fetch could overwrite. The
// user's branches don't move; like any fetch, git also updates origin/<target>.
func FetchTarget(ctx context.Context, repoPath, remote, target string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	ref := "refs/forge/remotes/" + remote + "/" + target
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "fetch", "--quiet", "--no-tags", remote, "+refs/heads/"+target+":"+ref)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0") // fail rather than wait for a password
	if out, err := cmd.CombinedOutput(); err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return "", fmt.Errorf("fetch %s from %s: %s", target, remote, msg)
		}
		return "", fmt.Errorf("fetch %s from %s: %w", target, remote, err)
	}
	return git(ctx, repoPath, "rev-parse", "--verify", ref+"^{commit}")
}

// StartingPoint decides the commit a new task's branch starts from, given its
// target's local tip. Behind the remote: the remote's tip. Ahead: the local
// tip, so the work builds on the unpushed commits and they go in its pull
// request. Without a remote, or when the fetch fails (offline, or the branch
// was never pushed): the local tip. note says why, when it isn't the same
// commit on both sides.
func StartingPoint(ctx context.Context, repoPath, target, localTip string) (tip, note string, err error) {
	remote, err := FindRemote(ctx, repoPath)
	if err != nil || remote == nil {
		return localTip, "", nil // delivery reports a broken remote
	}
	fetched, err := FetchTarget(ctx, repoPath, remote.Name, target)
	if err != nil {
		return localTip, fmt.Sprintf("started from the local branch: %v", err), nil
	}
	switch {
	case fetched == localTip:
		return localTip, "", nil
	case IsAncestor(ctx, repoPath, localTip, fetched):
		return fetched, fmt.Sprintf("started from %s/%s; the local branch is behind it", remote.Name, target), nil
	case IsAncestor(ctx, repoPath, fetched, localTip):
		return localTip, fmt.Sprintf("started from the local branch; it has commits %s/%s lacks, which will be in the pull request", remote.Name, target), nil
	}
	return "", "", fmt.Errorf("%w: %s and %s/%s; pull or push so that one contains the other, then try again", ErrDiverged, target, remote.Name, target)
}
