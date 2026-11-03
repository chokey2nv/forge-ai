package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
)

func TestFinalReviewIDsPerRevision(t *testing.T) {
	p := &domain.Task{ID: "p", IntegrationBranch: "forge/p"}
	if got := finalReviewRunID(p); got != "run-p-final-review" {
		t.Errorf("first review: got %s, want the pre-revision ID", got)
	}
	if got := combinedVerificationID(p); got != "vr-p-combined" {
		t.Errorf("first verification: got %s", got)
	}
	if got := approvedReviewRunID(p); got != "run-p-final-review" {
		t.Errorf("no revision yet: got %s", got)
	}

	p.Revisions = []domain.Revision{{N: 1, FinalReviewRunID: "run-p-final-review"}}
	if got := finalReviewRunID(p); got != "run-p-final-review-r2" {
		t.Errorf("second review: got %s", got)
	}
	if got := combinedVerificationID(p); got != "vr-p-combined-r2" {
		t.Errorf("second verification: got %s", got)
	}
	if got := approvedReviewRunID(p); got != "run-p-final-review" {
		t.Errorf("approval of r1: got %s", got)
	}
}

// oneTask serves a single task; nothing else in the store is used.
type oneTask struct {
	store.TaskRepo
	task *domain.Task
}

func (o oneTask) Get(_ context.Context, id string) (*domain.Task, error) {
	if id != o.task.ID {
		return nil, errors.New("not found")
	}
	return o.task, nil
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, repo, name, body string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-q", "-m", name)
	return gitT(t, repo, "rev-parse", "HEAD")
}

func TestGetTaskDiffByRevision(t *testing.T) {
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	base := commitFile(t, repo, "a.txt", "one\n")
	gitT(t, repo, "checkout", "-q", "-b", "forge/p")
	r1 := commitFile(t, repo, "a.txt", "one\ntwo\n")
	r2 := commitFile(t, repo, "b.txt", "bee\n")
	commitFile(t, repo, "c.txt", "in progress\n")

	task := &domain.Task{
		ID: "p", RepoPath: repo, IntegrationBranch: "forge/p", IntegrationBaseCommit: base,
		Revisions: []domain.Revision{
			{N: 1, Commit: r1, BaseCommit: base, FinalReviewRunID: "run-p-final-review"},
			{N: 2, Commit: r2, BaseCommit: base, FinalReviewRunID: "run-p-final-review-r2"},
		},
	}
	h := &Handlers{Tasks: oneTask{task: task}}
	get := func(query string) (int, taskDiff) {
		req := httptest.NewRequest(http.MethodGet, "/tasks/p/diff"+query, nil)
		req.SetPathValue("id", "p")
		w := httptest.NewRecorder()
		h.GetTaskDiff(w, req)
		var d taskDiff
		_ = json.Unmarshal(w.Body.Bytes(), &d)
		return w.Code, d
	}
	files := func(d taskDiff) (out []string) {
		for _, l := range strings.Split(d.Diff, "\n") {
			if strings.HasPrefix(l, "diff --git ") {
				out = append(out, strings.Fields(l)[3])
			}
		}
		return strings.Fields(strings.Join(out, " "))
	}

	cases := []struct {
		query     string
		code      int
		revision  int
		from, to  string
		wantFiles string
	}{
		{"", 200, 2, base, r2, "b/a.txt b/b.txt"},
		{"?revision=1", 200, 1, base, r1, "b/a.txt"},
		{"?revision=2&since=1", 200, 2, r1, r2, "b/b.txt"},
		{"?revision=0", 200, 0, base, "forge/p", "b/a.txt b/b.txt b/c.txt"},
		{"?revision=3", 404, 0, "", "", ""},
		{"?revision=1&since=1", 400, 0, "", "", ""},
		{"?revision=0&since=1", 400, 0, "", "", ""},
	}
	for _, c := range cases {
		code, d := get(c.query)
		if code != c.code {
			t.Errorf("%s: status %d, want %d", c.query, code, c.code)
			continue
		}
		if code != 200 {
			continue
		}
		if d.Revision != c.revision || d.From != c.from || d.To != c.to || len(d.Revisions) != 2 {
			t.Errorf("%s: got revision %d %s..%s (%d revisions)", c.query, d.Revision, d.From, d.To, len(d.Revisions))
		}
		if got := strings.Join(files(d), " "); got != c.wantFiles {
			t.Errorf("%s: files %q, want %q", c.query, got, c.wantFiles)
		}
	}
}
