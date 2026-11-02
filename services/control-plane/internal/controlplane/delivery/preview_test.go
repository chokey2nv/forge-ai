package delivery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

func TestParseRemoteURL(t *testing.T) {
	cases := []struct{ url, host, repo string }{
		{"https://github.com/chokey2nv/forge-ai.git", "github.com", "chokey2nv/forge-ai"},
		{"https://github.com/chokey2nv/forge-ai", "github.com", "chokey2nv/forge-ai"},
		{"git@github.com:chokey2nv/forge-ai.git", "github.com", "chokey2nv/forge-ai"},
		{"ssh://git@github.example.com:2222/team/app.git", "github.example.com", "team/app"},
		{"https://user@gitlab.com/group/sub/app.git", "gitlab.com", "group/sub/app"},
		{"/tmp/origin.git", "", ""},
		{"file:///tmp/origin.git", "", ""},
		{"../origin", "", ""},
	}
	for _, c := range cases {
		if h, r := ParseRemoteURL(c.url); h != c.host || r != c.repo {
			t.Errorf("ParseRemoteURL(%q) = %q %q, want %q %q", c.url, h, r, c.host, c.repo)
		}
	}
}

// fakeGH writes a gh stand-in whose `auth status` succeeds or fails.
func fakeGH(t *testing.T, loggedIn bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gh")
	script := "#!/bin/sh\necho '  ✓ Logged in to github.com account octocat (keyring)'\nexit 0\n"
	if !loggedIn {
		script = "#!/bin/sh\necho 'You are not logged into any GitHub hosts.' >&2\nexit 1\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func taskFor(repo string) *domain.Task {
	return &domain.Task{ID: "task-1a2b3c4d", RepoPath: repo, TargetBranch: "main", IntegrationBranch: "forge/fix-1a2b3c4d"}
}

func TestPreview(t *testing.T) {
	ctx := context.Background()

	t.Run("no remote: local merge", func(t *testing.T) {
		repo := newRepo(t)
		p := NewInspector(fakeGH(t, true)).Preview(ctx, taskFor(repo))
		if p.Mode != ModeLocalMerge || p.Remote != nil || len(p.Warnings) != 0 {
			t.Errorf("got %+v", p)
		}
		if !strings.Contains(p.Action, "merge forge/fix-1a2b3c4d into main locally") {
			t.Errorf("action = %q", p.Action)
		}
	})

	t.Run("GitHub remote, gh logged in: pull request", func(t *testing.T) {
		repo := newRepo(t)
		run(t, repo, "remote", "add", "origin", "git@github.com:owner/app.git")
		p := NewInspector(fakeGH(t, true)).Preview(ctx, taskFor(repo))
		if p.Mode != ModePullRequest || !p.GHReady || p.GHAccount != "octocat" || len(p.Warnings) != 0 {
			t.Errorf("got %+v", p)
		}
		if !strings.Contains(p.Action, "open a pull request into main on github.com/owner/app as octocat") {
			t.Errorf("action = %q", p.Action)
		}
	})

	t.Run("origin preferred over other remotes", func(t *testing.T) {
		repo := newRepo(t)
		run(t, repo, "remote", "add", "aaa", "https://github.com/other/x.git")
		run(t, repo, "remote", "add", "origin", "https://github.com/owner/app.git")
		p := NewInspector(fakeGH(t, true)).Preview(ctx, taskFor(repo))
		if p.Remote == nil || p.Remote.Name != "origin" {
			t.Errorf("remote = %+v, want origin", p.Remote)
		}
	})

	t.Run("gh not logged in: warned", func(t *testing.T) {
		repo := newRepo(t)
		run(t, repo, "remote", "add", "origin", "https://github.com/owner/app.git")
		p := NewInspector(fakeGH(t, false)).Preview(ctx, taskFor(repo))
		if p.Mode != ModePullRequest || p.GHReady || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "not logged in to github.com") {
			t.Errorf("got %+v", p)
		}
	})

	t.Run("gh missing: warned", func(t *testing.T) {
		repo := newRepo(t)
		run(t, repo, "remote", "add", "origin", "https://github.com/owner/app.git")
		p := NewInspector(filepath.Join(t.TempDir(), "no-gh-here")).Preview(ctx, taskFor(repo))
		if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "gh is not installed") {
			t.Errorf("got %+v", p)
		}
	})

	t.Run("local-path remote: warned", func(t *testing.T) {
		repo := newRepo(t)
		run(t, repo, "remote", "add", "origin", t.TempDir())
		p := NewInspector(fakeGH(t, true)).Preview(ctx, taskFor(repo))
		if p.Mode != ModePullRequest || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "local path") {
			t.Errorf("got %+v", p)
		}
	})

	t.Run("task without a parent branch", func(t *testing.T) {
		p := NewInspector("gh").Preview(ctx, &domain.Task{ID: "old"})
		if p.Mode != ModeUnavailable {
			t.Errorf("got %+v", p)
		}
	})
}
