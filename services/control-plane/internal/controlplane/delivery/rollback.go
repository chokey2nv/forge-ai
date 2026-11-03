package delivery

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrRevertConflict: later commits on the target changed the same lines, so
// the delivery cannot be reverted automatically.
var ErrRevertConflict = errors.New("the revert conflicts with later changes on the target")

// ErrAlreadyReverted: the target no longer holds the delivery's changes (it
// was reverted by hand, or by an earlier rollback), so a revert would be empty.
var ErrAlreadyReverted = errors.New("the delivery is already reverted on the target")

// RevertCommit creates (but does not land) a commit on top of onto that
// undoes merge: a three-way merge of onto and merge's first parent, with
// merge as the base. Works for a merge commit (undoing what its second
// parent brought) and for a squash merge alike.
func RevertCommit(ctx context.Context, repoPath, onto, merge, message string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "merge-tree", "--write-tree", "--no-messages",
		"--merge-base", merge, onto, merge+"^1")
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 { // merge-tree: conflicts
		return "", ErrRevertConflict
	}
	if err != nil {
		return "", fmt.Errorf("revert %s: %w", shortRef(merge), err)
	}
	tree := firstLineOf(string(out))
	if ontoTree, err := git(ctx, repoPath, "rev-parse", onto+"^{tree}"); err == nil && ontoTree == tree {
		return "", ErrAlreadyReverted
	}
	return commitTree(ctx, repoPath, tree, message, onto)
}

// RevertLocally lands a revert of merge on target, with the same guarantees
// as MergeLocally: a dirty checkout is refused, a clean one fast-forwarded,
// otherwise a compare-and-swap ref update.
func RevertLocally(ctx context.Context, repoPath, target, merge, message string) (string, error) {
	t, err := BranchTip(ctx, repoPath, target)
	if err != nil {
		return "", err
	}
	if !IsAncestor(ctx, repoPath, merge, t) {
		return "", fmt.Errorf("%s is not on %s; nothing to revert there", shortRef(merge), target)
	}
	wt, err := cleanCheckout(ctx, repoPath, target)
	if err != nil {
		return "", err
	}
	revert, err := RevertCommit(ctx, repoPath, t, merge, message)
	if err != nil {
		return "", err
	}
	return revert, advance(ctx, repoPath, wt, target, t, revert, message)
}

// RevertBranch fetches the remote's target, commits a revert of merge on top
// of it and creates branch there, ready to push for a revert pull request.
func RevertBranch(ctx context.Context, repoPath, remote, target, merge, branch, message string) (string, error) {
	if _, err := git(ctx, repoPath, "fetch", "--quiet", remote, "refs/heads/"+target); err != nil {
		return "", fmt.Errorf("fetch %s from %s: %w", target, remote, err)
	}
	onto, err := git(ctx, repoPath, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return "", err
	}
	if !IsAncestor(ctx, repoPath, merge, onto) {
		return "", fmt.Errorf("%s is not on %s/%s; nothing to revert there", shortRef(merge), remote, target)
	}
	revert, err := RevertCommit(ctx, repoPath, onto, merge, message)
	if err != nil {
		return "", err
	}
	if err := CreateBranch(ctx, repoPath, branch, revert); err != nil {
		return "", err
	}
	return revert, nil
}

func firstLineOf(s string) string { return strings.TrimSpace(strings.SplitN(s, "\n", 2)[0]) }

func shortRef(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
