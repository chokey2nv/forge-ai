package delivery

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// behindRepo: repo's main was pushed to a bare origin, and a second clone then
// landed a commit on origin's main that repo hasn't fetched.
func behindRepo(t *testing.T) (repo, localTip, originTip string) {
	t.Helper()
	repo = workRepo(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	run(t, t.TempDir(), "init", "-q", "--bare", origin)
	run(t, repo, "remote", "add", "origin", origin)
	run(t, repo, "push", "-q", "origin", "main")
	other := filepath.Join(t.TempDir(), "other")
	run(t, t.TempDir(), "clone", "-q", "-b", "main", origin, other)
	run(t, other, "config", "user.name", "someone")
	run(t, other, "config", "user.email", "someone@localhost")
	commitFile(t, other, "merged.txt", "merged on the forge\n")
	run(t, other, "push", "-q", "origin", "main")
	return repo, run(t, repo, "rev-parse", "main"), run(t, other, "rev-parse", "HEAD")
}

func TestStartingPoint(t *testing.T) {
	ctx := context.Background()

	t.Run("no remote: the local tip", func(t *testing.T) {
		repo := workRepo(t)
		local := run(t, repo, "rev-parse", "main")
		if tip, note, err := StartingPoint(ctx, repo, "main", local); err != nil || tip != local || note != "" {
			t.Errorf("got %s %q %v", tip, note, err)
		}
	})
	t.Run("the same on both sides: no note", func(t *testing.T) {
		repo := workRepo(t)
		origin := filepath.Join(t.TempDir(), "origin.git")
		run(t, t.TempDir(), "init", "-q", "--bare", origin)
		run(t, repo, "remote", "add", "origin", origin)
		run(t, repo, "push", "-q", "origin", "main")
		local := run(t, repo, "rev-parse", "main")
		if tip, note, err := StartingPoint(ctx, repo, "main", local); err != nil || tip != local || note != "" {
			t.Errorf("got %s %q %v", tip, note, err)
		}
	})
	t.Run("behind: the remote's tip, and local branches stay put", func(t *testing.T) {
		repo, local, remote := behindRepo(t)
		tip, note, err := StartingPoint(ctx, repo, "main", local)
		if err != nil || tip != remote || !strings.Contains(note, "behind") {
			t.Errorf("got %s %q %v", tip, note, err)
		}
		if run(t, repo, "rev-parse", "main") != local {
			t.Error("local main moved")
		}
	})
	t.Run("ahead: the local tip", func(t *testing.T) {
		repo := workRepo(t)
		origin := filepath.Join(t.TempDir(), "origin.git")
		run(t, t.TempDir(), "init", "-q", "--bare", origin)
		run(t, repo, "remote", "add", "origin", origin)
		run(t, repo, "push", "-q", "origin", "main")
		run(t, repo, "checkout", "-q", "main")
		commitFile(t, repo, "unpushed.txt", "unpushed\n")
		local := run(t, repo, "rev-parse", "main")
		tip, note, err := StartingPoint(ctx, repo, "main", local)
		if err != nil || tip != local || !strings.Contains(note, "lacks") {
			t.Errorf("got %s %q %v", tip, note, err)
		}
	})
	t.Run("diverged: refused", func(t *testing.T) {
		repo, _, _ := behindRepo(t)
		run(t, repo, "checkout", "-q", "main")
		commitFile(t, repo, "unpushed.txt", "unpushed\n")
		local := run(t, repo, "rev-parse", "main")
		if _, _, err := StartingPoint(ctx, repo, "main", local); !errors.Is(err, ErrDiverged) {
			t.Errorf("want ErrDiverged, got %v", err)
		}
	})
	t.Run("the fetch fails: the local tip, and says so", func(t *testing.T) {
		repo := workRepo(t)
		run(t, repo, "remote", "add", "origin", filepath.Join(t.TempDir(), "missing.git"))
		local := run(t, repo, "rev-parse", "main")
		tip, note, err := StartingPoint(ctx, repo, "main", local)
		if err != nil || tip != local || !strings.Contains(note, "started from the local branch") {
			t.Errorf("got %s %q %v", tip, note, err)
		}
	})
}

func TestFetchTargetNotOnTheRemote(t *testing.T) {
	repo, _, _ := behindRepo(t)
	if _, err := FetchTarget(context.Background(), repo, "origin", "never-pushed"); err == nil || !strings.Contains(err.Error(), "never-pushed") {
		t.Errorf("want an error naming the branch, got %v", err)
	}
}
