// Package delivery owns a task's life in git outside agent workspaces: the
// parent branch created with the task, and how an approved task reaches its
// target branch (a pull request when the repository has a remote, otherwise
// a local merge).
package delivery

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"unicode"
)

// maxSlugLen bounds the description part of a branch name.
const maxSlugLen = 40

// Slug turns a requirement into a short branch-name fragment: lower-case
// words joined by hyphens, cut at a word boundary.
func Slug(text string) string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(text) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()

	slug := ""
	for _, w := range words {
		next := w
		if slug != "" {
			next = slug + "-" + w
		}
		if len(next) > maxSlugLen {
			if slug == "" { // one very long word
				slug = w[:maxSlugLen]
			}
			break
		}
		slug = next
	}
	if slug == "" {
		return "task"
	}
	return slug
}

// ParentBranchName names the branch a task's work lives on, e.g.
// "forge/fix-the-off-by-one-pagination-bug-1a2b3c4d". The task ID's suffix
// keeps names unique.
func ParentBranchName(taskID, requirement string) string {
	return "forge/" + Slug(requirement) + "-" + strings.TrimPrefix(taskID, "task-")
}

// ResolveTarget decides which branch a task's work is for and its current
// tip. explicit wins; otherwise baseCommit when it names a branch; otherwise
// the branch checked out in the repository ("HEAD" or empty).
func ResolveTarget(ctx context.Context, repoPath, explicit, baseCommit string) (branch, tip string, err error) {
	switch {
	case explicit != "":
		branch = explicit
	case baseCommit != "" && baseCommit != "HEAD" && isBranch(ctx, repoPath, baseCommit):
		branch = baseCommit
	case baseCommit == "" || baseCommit == "HEAD":
		out, err := git(ctx, repoPath, "symbolic-ref", "--quiet", "--short", "HEAD")
		if err != nil {
			return "", "", fmt.Errorf("the repository has no branch checked out (detached HEAD); set targetBranch")
		}
		branch = out
	default:
		return "", "", fmt.Errorf("baseCommit %q is not a branch; set targetBranch to the branch the work is for", baseCommit)
	}
	if !isBranch(ctx, repoPath, branch) {
		return "", "", fmt.Errorf("target branch %q does not exist in %s", branch, repoPath)
	}
	tip, err = git(ctx, repoPath, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("resolve %s: %w", branch, err)
	}
	return branch, tip, nil
}

// CreateBranch creates name at sha. It never moves an existing branch.
func CreateBranch(ctx context.Context, repoPath, name, sha string) error {
	if _, err := git(ctx, repoPath, "check-ref-format", "--branch", name); err != nil {
		return fmt.Errorf("invalid branch name %q: %w", name, err)
	}
	if _, err := git(ctx, repoPath, "branch", "--no-track", name, sha); err != nil {
		return fmt.Errorf("create branch %s: %w", name, err)
	}
	return nil
}

// EnsureBranchAt creates name at sha, or accepts it if it already exists
// there. A branch at another commit is an error: it is never moved.
func EnsureBranchAt(ctx context.Context, repoPath, name, sha string) error {
	if isBranch(ctx, repoPath, name) {
		tip, err := git(ctx, repoPath, "rev-parse", "refs/heads/"+name)
		if err != nil {
			return err
		}
		if tip != sha {
			return fmt.Errorf("branch %s already exists at %s, not %s", name, tip, sha)
		}
		return nil
	}
	return CreateBranch(ctx, repoPath, name, sha)
}

// EnsureBranchOrAhead accepts a branch at sha or ahead of it (holding
// commits on top of sha), and creates it at sha when missing.
func EnsureBranchOrAhead(ctx context.Context, repoPath, name, sha string) error {
	if !isBranch(ctx, repoPath, name) {
		return CreateBranch(ctx, repoPath, name, sha)
	}
	if _, err := git(ctx, repoPath, "merge-base", "--is-ancestor", sha, "refs/heads/"+name); err != nil {
		return fmt.Errorf("branch %s does not contain its starting commit %s", name, sha)
	}
	return nil
}

// BranchTip is the commit a branch points at.
func BranchTip(ctx context.Context, repoPath, name string) (string, error) {
	return git(ctx, repoPath, "rev-parse", "--verify", "refs/heads/"+name)
}

// SubtaskBranchName is <parent branch>-<slug of the subtask's title>. git
// cannot hold both forge/x and forge/x/y, hence the hyphen.
func SubtaskBranchName(parentBranch, title string) string {
	return parentBranch + "-" + Slug(title)
}

// DeleteBranch force-deletes a branch this package just created that holds
// no work (its task could not be saved). Never use it on a branch with work.
func DeleteBranch(ctx context.Context, repoPath, name string) error {
	_, err := git(ctx, repoPath, "branch", "-D", name)
	return err
}

func isBranch(ctx context.Context, repoPath, name string) bool {
	_, err := git(ctx, repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

// git runs a git command in repoPath and returns its trimmed stdout.
func git(ctx context.Context, repoPath string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoPath}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
