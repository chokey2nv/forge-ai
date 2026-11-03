package delivery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// deliveredRepo: forge/w merged into main by MergeLocally.
func deliveredRepo(t *testing.T) (repo, merge string) {
	t.Helper()
	repo = workRepo(t)
	merge, err := MergeLocally(context.Background(), repo, "main", "forge/w", "deliver")
	if err != nil {
		t.Fatal(err)
	}
	return repo, merge
}

func commitFile(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-q", "-m", "edit "+name)
}

func TestRevertLocally(t *testing.T) {
	ctx := context.Background()
	repo, merge := deliveredRepo(t)
	commitFile(t, repo, "later.txt", "later\n") // unrelated work lands after the delivery

	revert, err := RevertLocally(ctx, repo, "main", merge, "Revert forge/w")
	if err != nil {
		t.Fatal(err)
	}
	if run(t, repo, "rev-parse", "main") != revert || run(t, repo, "rev-parse", revert+"^") != run(t, repo, "rev-parse", "main~1") {
		t.Error("main is not one revert commit on top of its old tip")
	}
	if _, err := os.Stat(filepath.Join(repo, "w.txt")); !os.IsNotExist(err) {
		t.Error("the delivered file is still in the checkout")
	}
	if _, err := os.Stat(filepath.Join(repo, "later.txt")); err != nil {
		t.Error("later work was lost")
	}
	if !IsAncestor(ctx, repo, merge, "main") {
		t.Error("history was rewritten: the merge is no longer on main")
	}
}

func TestRevertLocallyRefusesDirtyCheckout(t *testing.T) {
	repo, merge := deliveredRepo(t)
	before := run(t, repo, "rev-parse", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RevertLocally(context.Background(), repo, "main", merge, "r"); !errors.Is(err, ErrTargetDirty) {
		t.Fatalf("want ErrTargetDirty, got %v", err)
	}
	if run(t, repo, "rev-parse", "main") != before {
		t.Error("main moved")
	}
}

func TestRevertConflict(t *testing.T) {
	repo, merge := deliveredRepo(t)
	commitFile(t, repo, "w.txt", "changed after delivery\n")
	before := run(t, repo, "rev-parse", "main")
	if _, err := RevertLocally(context.Background(), repo, "main", merge, "r"); !errors.Is(err, ErrRevertConflict) {
		t.Fatalf("want ErrRevertConflict, got %v", err)
	}
	if run(t, repo, "rev-parse", "main") != before {
		t.Error("main moved")
	}
}

func TestRevertBranchFromRemote(t *testing.T) {
	ctx := context.Background()
	repo, merge := deliveredRepo(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	run(t, t.TempDir(), "init", "-q", "--bare", origin)
	run(t, repo, "remote", "add", "origin", origin)
	run(t, repo, "push", "-q", "origin", "main")
	run(t, repo, "reset", "-q", "--hard", "main~1") // local main is behind origin: the revert must build on origin's

	revert, err := RevertBranch(ctx, repo, "origin", "main", merge, "forge/w-revert", "Revert")
	if err != nil {
		t.Fatal(err)
	}
	if run(t, repo, "rev-parse", "forge/w-revert") != revert || run(t, repo, "rev-parse", revert+"^") != merge {
		t.Error("revert branch is not one commit on top of origin/main")
	}
	if run(t, repo, "rev-parse", "main") == revert {
		t.Error("local main must not move")
	}
}

func TestRevertTwiceIsRefused(t *testing.T) {
	ctx := context.Background()
	repo, merge := deliveredRepo(t)
	if _, err := RevertLocally(ctx, repo, "main", merge, "r"); err != nil {
		t.Fatal(err)
	}
	before := run(t, repo, "rev-parse", "main")
	if _, err := RevertLocally(ctx, repo, "main", merge, "r"); !errors.Is(err, ErrAlreadyReverted) {
		t.Fatalf("want ErrAlreadyReverted, got %v", err)
	}
	if run(t, repo, "rev-parse", "main") != before {
		t.Error("main moved: an empty revert commit was added")
	}
}
