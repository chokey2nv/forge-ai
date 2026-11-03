package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var (
	// ErrTargetMoved: the target branch is not contained in the work branch
	// (it advanced, or advanced during delivery). Merge it in and retry.
	ErrTargetMoved = errors.New("target branch moved")
	// ErrTargetDirty: the target is checked out with uncommitted changes.
	ErrTargetDirty = errors.New("target branch is checked out with uncommitted changes")
)

// IsAncestor reports whether a is contained in b.
func IsAncestor(ctx context.Context, repoPath, a, b string) bool {
	_, err := git(ctx, repoPath, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// TargetCheckout finds a worktree that has branch checked out (the main
// repository's working tree included) and whether it has uncommitted
// changes to tracked files. Empty path: not checked out anywhere.
func TargetCheckout(ctx context.Context, repoPath, branch string) (path string, dirty bool, err error) {
	out, err := git(ctx, repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return "", false, err
	}
	for _, entry := range strings.Split(out, "\n\n") {
		wt := ""
		for _, l := range strings.Split(entry, "\n") {
			if p, ok := strings.CutPrefix(l, "worktree "); ok {
				wt = p
			}
			if l == "branch refs/heads/"+branch && wt != "" {
				status, err := git(ctx, wt, "status", "--porcelain", "--untracked-files=no")
				if err != nil {
					return wt, false, err
				}
				return wt, status != "", nil
			}
		}
	}
	return "", false, nil
}

// MergeLocally merges branch into target without touching any checkout the
// user did not leave clean. branch must already contain target (callers
// merge the target in first); the result is a merge commit with target and
// branch as parents and branch's tree. A clean checkout of target is
// fast-forwarded to it; otherwise the ref moves by compare-and-swap.
func MergeLocally(ctx context.Context, repoPath, target, branch, message string) (string, error) {
	t, err := BranchTip(ctx, repoPath, target)
	if err != nil {
		return "", err
	}
	b, err := BranchTip(ctx, repoPath, branch)
	if err != nil {
		return "", err
	}
	if !IsAncestor(ctx, repoPath, t, b) {
		return "", ErrTargetMoved
	}

	wt, err := cleanCheckout(ctx, repoPath, target)
	if err != nil {
		return "", err
	}
	tree, err := git(ctx, repoPath, "rev-parse", b+"^{tree}")
	if err != nil {
		return "", err
	}
	merge, err := commitTree(ctx, repoPath, tree, message, t, b)
	if err != nil {
		return "", fmt.Errorf("create merge commit: %w", err)
	}
	return merge, advance(ctx, repoPath, wt, target, t, merge, message)
}

// cleanCheckout returns the worktree that has target checked out ("" for
// none), refusing one with uncommitted changes.
func cleanCheckout(ctx context.Context, repoPath, target string) (string, error) {
	wt, dirty, err := TargetCheckout(ctx, repoPath, target)
	if err != nil {
		return "", err
	}
	if dirty {
		return "", fmt.Errorf("%w (%s)", ErrTargetDirty, wt)
	}
	return wt, nil
}

func commitTree(ctx context.Context, repoPath, tree, message string, parents ...string) (string, error) {
	args := []string{"-C", repoPath, "commit-tree", tree}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	cmd := exec.CommandContext(ctx, "git", append(args, "-m", message)...)
	cmd.Env = identityEnv(ctx, repoPath)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// advance moves target from old to commit (a descendant of old). A clean
// checkout wt is fast-forwarded, so the branch and the files the user sees
// move together; otherwise the ref moves by compare-and-swap. Either fails
// with ErrTargetMoved if the branch moved meanwhile.
func advance(ctx context.Context, repoPath, wt, target, old, commit, message string) error {
	if wt != "" {
		if _, err := git(ctx, wt, "merge", "--ff-only", "-q", commit); err != nil {
			return fmt.Errorf("%w: %v", ErrTargetMoved, err)
		}
		return nil
	}
	if _, err := git(ctx, repoPath, "update-ref", "-m", "forge: "+message, "refs/heads/"+target, commit, old); err != nil {
		return fmt.Errorf("%w: %v", ErrTargetMoved, err)
	}
	return nil
}

// Push publishes branch to remote. It never forces.
func Push(ctx context.Context, repoPath, remote, branch string) error {
	if _, err := git(ctx, repoPath, "push", "--quiet", remote, "refs/heads/"+branch+":refs/heads/"+branch); err != nil {
		return fmt.Errorf("push %s to %s: %w", branch, remote, err)
	}
	return nil
}

var prNumber = regexp.MustCompile(`/pull/(\d+)`)

// OpenPR opens a pull request from branch into target with gh, run in the
// repository so gh resolves the remote itself; --repo is passed when the
// remote names one.
func OpenPR(ctx context.Context, gh, repoPath string, remote *Remote, target, branch, title, body string) (url string, number int, err error) {
	args := []string{"pr", "create", "--base", target, "--head", branch, "--title", title, "--body", body}
	if remote != nil && remote.Host != "" && remote.Repo != "" {
		args = append(args, "--repo", remote.Host+"/"+remote.Repo)
	}
	out, err := runGH(ctx, gh, repoPath, args...)
	if err != nil {
		return "", 0, fmt.Errorf("gh pr create: %w", err)
	}
	for _, line := range strings.Split(out, "\n") {
		if m := prNumber.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			return strings.TrimSpace(line), n, nil
		}
	}
	return "", 0, fmt.Errorf("gh pr create: no pull request URL in output %q", out)
}

// PRState asks gh for a pull request's state: OPEN, MERGED or CLOSED, and
// the merge commit when merged.
func PRState(ctx context.Context, gh, repoPath, url string) (state, mergeCommit string, err error) {
	out, err := runGH(ctx, gh, repoPath, "pr", "view", url, "--json", "state,mergeCommit")
	if err != nil {
		return "", "", fmt.Errorf("gh pr view: %w", err)
	}
	var v struct {
		State       string `json:"state"`
		MergeCommit *struct {
			OID string `json:"oid"`
		} `json:"mergeCommit"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return "", "", fmt.Errorf("gh pr view: unreadable output %q: %w", out, err)
	}
	if v.MergeCommit != nil {
		mergeCommit = v.MergeCommit.OID
	}
	return v.State, mergeCommit, nil
}

func runGH(ctx context.Context, gh, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, gh, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// identityEnv supplies a committer identity when the repository has none.
func identityEnv(ctx context.Context, repoPath string) []string {
	env := os.Environ()
	if email, _ := git(ctx, repoPath, "config", "user.email"); email == "" {
		env = append(env,
			"GIT_AUTHOR_NAME=Forge", "GIT_AUTHOR_EMAIL=forge@localhost",
			"GIT_COMMITTER_NAME=Forge", "GIT_COMMITTER_EMAIL=forge@localhost")
	}
	return env
}
