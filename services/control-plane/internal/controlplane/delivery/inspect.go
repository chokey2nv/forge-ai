package delivery

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Repo describes a repository before a task is created in it, for the
// console's New task form: its branches and the one checked out.
type Repo struct {
	Path          string   `json:"path"`
	Branches      []string `json:"branches"`
	CurrentBranch string   `json:"currentBranch,omitempty"` // empty: detached HEAD
}

// InspectRepo reads a repository's local branches. It fails when path is
// not a git repository with at least one commit.
func InspectRepo(ctx context.Context, path string) (*Repo, error) {
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", path)
	}
	top, err := git(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%s is not a git repository", path)
	}
	if _, err := git(ctx, path, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		return nil, fmt.Errorf("%s has no commits yet", path)
	}
	r := &Repo{Path: top, Branches: []string{}}
	out, err := git(ctx, path, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	for _, b := range strings.Split(out, "\n") {
		// Forge's own task branches are not delivery targets.
		if b != "" && !strings.HasPrefix(b, "forge/") {
			r.Branches = append(r.Branches, b)
		}
	}
	r.CurrentBranch, _ = git(ctx, path, "symbolic-ref", "--quiet", "--short", "HEAD")
	return r, nil
}
