package delivery

import (
	"context"
	"strings"
	"testing"
)

func TestInspectRepo(t *testing.T) {
	ctx := context.Background()
	repo := workRepo(t) // main checked out, forge/w beside it
	run(t, repo, "branch", "release")

	r, err := InspectRepo(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if r.CurrentBranch != "main" || strings.Join(r.Branches, ",") != "main,release" {
		t.Errorf("got current %q branches %v; want main and [main release] (forge/ branches hidden)", r.CurrentBranch, r.Branches)
	}

	if _, err := InspectRepo(ctx, t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("plain directory: %v", err)
	}
	if _, err := InspectRepo(ctx, "/does/not/exist"); err == nil {
		t.Error("missing path accepted")
	}
	empty := t.TempDir()
	run(t, empty, "init", "-q")
	if _, err := InspectRepo(ctx, empty); err == nil || !strings.Contains(err.Error(), "no commits") {
		t.Errorf("empty repo: %v", err)
	}
}
