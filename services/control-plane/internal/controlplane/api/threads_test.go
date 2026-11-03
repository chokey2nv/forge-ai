package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/agent"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
)

// git prints a blank context line as a single space.
var anchorDiff = strings.ReplaceAll(`diff --git a/src/price.js b/src/price.js
index 1111111..2222222 100644
--- a/src/price.js
+++ b/src/price.js
@@ -1,6 +1,6 @@
 // prices
 export function applyDiscount(p, pct) {
-  return p - pct;
+  return Math.round(p * (100 - pct)) / 100;
 }

 export const ZERO = 0;
diff --git a/tests/new.test.js b/tests/new.test.js
new file mode 100644
--- /dev/null
+++ b/tests/new.test.js
@@ -0,0 +1,2 @@
+import test from "node:test";
+test("x", () => {});
diff --git a/old.txt b/old.txt
deleted file mode 100644
--- a/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-gone
`, "\n\n", "\n \n")

func TestDiffLineAnchors(t *testing.T) {
	cases := []struct {
		req      anchorRequest
		lineText string
		before   []string
		after    []string
		err      string
	}{
		{req: anchorRequest{Path: "src/price.js", Line: 3},
			lineText: "  return Math.round(p * (100 - pct)) / 100;",
			before:   []string{"// prices", "export function applyDiscount(p, pct) {"},
			after:    []string{"}", "", "export const ZERO = 0;"}},
		{req: anchorRequest{Path: "src/price.js", Side: "old", Line: 3},
			lineText: "  return p - pct;",
			before:   []string{"// prices", "export function applyDiscount(p, pct) {"},
			after:    []string{"}", "", "export const ZERO = 0;"}},
		// A context line is on both sides.
		{req: anchorRequest{Path: "src/price.js", Side: "old", Line: 6}, lineText: "export const ZERO = 0;",
			before: []string{"  return p - pct;", "}", ""}},
		{req: anchorRequest{Path: "tests/new.test.js", Line: 2}, lineText: `test("x", () => {});`,
			before: []string{`import test from "node:test";`}},
		{req: anchorRequest{Path: "old.txt", Side: "old", Line: 1}, lineText: "gone"},

		{req: anchorRequest{Path: "src/price.js", Line: 7}, err: "src/price.js:7 (new) is not in the diff"},
		{req: anchorRequest{Path: "old.txt", Line: 1}, err: "old.txt is not in the diff"}, // deleted: no new side
		{req: anchorRequest{Path: "tests/new.test.js", Side: "old", Line: 1}, err: "not in the diff"},
		{req: anchorRequest{Path: "README.md", Line: 1}, err: "README.md is not in the diff"},
		{req: anchorRequest{Path: "src/price.js", Side: "left", Line: 1}, err: `side must be "new" or "old"`},
		{req: anchorRequest{Path: "src/price.js"}, err: "anchor needs a path and a line"},
	}
	for _, c := range cases {
		a, err := diffLine(anchorDiff, c.req)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%+v: err %v, want %q", c.req, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%+v: %v", c.req, err)
			continue
		}
		if a.LineText != c.lineText || strings.Join(a.Before, "|") != strings.Join(c.before, "|") || strings.Join(a.After, "|") != strings.Join(c.after, "|") {
			t.Errorf("%+v: got %q before %q after %q", c.req, a.LineText, a.Before, a.After)
		}
		if a.Side == "" || a.Path != c.req.Path || a.Line != c.req.Line {
			t.Errorf("%+v: anchor %+v", c.req, a)
		}
	}
}

func TestParseLocation(t *testing.T) {
	cases := []struct {
		loc  string
		path string
		line int
	}{
		{"src/price.js:12", "src/price.js", 12},
		{"src/price.js:12-20", "src/price.js", 12},
		{"src/price.js:12:5", "src/price.js", 12},
		{"`./src/price.js#L7`", "src/price.js", 7},
		{"src/price.js line 4", "src/price.js", 4},
		{"src/price.js (line 9)", "src/price.js", 9},
		{"tests/p.test.js", "tests/p.test.js", 0},
		{"Makefile", "", 0}, // no dot or slash: not taken as a path
		{"applyDiscount()", "", 0},
		{"the rounding logic", "", 0},
		{"", "", 0},
	}
	for _, c := range cases {
		if p, l := parseLocation(c.loc); p != c.path || l != c.line {
			t.Errorf("%q: got %q:%d, want %q:%d", c.loc, p, l, c.path, c.line)
		}
	}
}

// memThreads is an in-memory ThreadRepo.
type memThreads struct{ m map[string]*domain.Thread }

func (s *memThreads) Create(_ context.Context, t *domain.Thread) error {
	t.ID = "thr-" + string(rune('a'+len(s.m)))
	cp := *t
	s.m[t.ID] = &cp
	return nil
}
func (s *memThreads) Get(_ context.Context, id string) (*domain.Thread, error) {
	t, ok := s.m[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *t
	return &cp, nil
}
func (s *memThreads) ListByTask(_ context.Context, taskID string) ([]*domain.Thread, error) {
	var out []*domain.Thread
	for _, id := range []string{"thr-a", "thr-b", "thr-c", "thr-d"} {
		if t, ok := s.m[id]; ok && t.TaskID == taskID {
			out = append(out, t)
		}
	}
	return out, nil
}
func (s *memThreads) AddComment(_ context.Context, id string, c domain.Comment) error {
	s.m[id].Comments = append(s.m[id].Comments, c)
	return nil
}
func (s *memThreads) SetStatus(_ context.Context, id string, st domain.ThreadStatus, by string, _ time.Time) error {
	s.m[id].Status, s.m[id].ResolvedBy = st, by
	return nil
}

type noEvents struct{ store.EventRepo }

func (noEvents) Append(context.Context, *domain.Event) error { return nil }

func TestThreadRules(t *testing.T) {
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	base := commitFile(t, repo, "a.txt", "one\n")
	gitT(t, repo, "checkout", "-q", "-b", "forge/p")
	r1 := commitFile(t, repo, "a.txt", "one\ntwo\n")

	task := &domain.Task{
		ID: "p", RepoPath: repo, IntegrationBranch: "forge/p", IntegrationBaseCommit: base,
		Status:    domain.StatusReviewPending,
		Revisions: []domain.Revision{{N: 1, Commit: r1, BaseCommit: base}},
	}
	reg := agent.NewRegistry("../../../../../agents", "../../../../../skills", nil)
	if err := reg.Load(context.Background()); err != nil {
		t.Fatalf("load agents: %v", err)
	}
	threads := &memThreads{m: map[string]*domain.Thread{}}
	h := &Handlers{Tasks: oneTask{task: task}, Threads: threads, Events: noEvents{}, Registry: reg}

	call := func(handler http.HandlerFunc, path, tid string, body any) (int, string) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/tasks/p/threads"+path, bytes.NewReader(b))
		req.SetPathValue("id", "p")
		req.SetPathValue("tid", tid)
		w := httptest.NewRecorder()
		handler(w, req)
		return w.Code, w.Body.String()
	}
	open := func(body any) (int, string) { return call(h.CreateThread, "", "", body) }

	code, out := open(map[string]any{"author": "dana", "body": "why two?", "anchor": map[string]any{"path": "a.txt", "line": 2}})
	if code != http.StatusCreated {
		t.Fatalf("open anchored thread: %d %s", code, out)
	}
	th := threads.m["thr-a"]
	if th.Revision != 1 || th.Commit != r1 || th.Anchor.LineText != "two" || th.Status != domain.ThreadOpen || th.Comments[0].Kind != domain.CommentHuman {
		t.Errorf("thread %+v anchor %+v", th, th.Anchor)
	}
	if code, out := open(map[string]any{"author": "dana", "body": "general remark"}); code != http.StatusCreated {
		t.Errorf("open general thread: %d %s", code, out)
	}

	for name, c := range map[string]struct {
		body map[string]any
		code int
	}{
		"line not in the diff": {map[string]any{"author": "dana", "body": "x", "anchor": map[string]any{"path": "a.txt", "line": 9}}, 400},
		"file not in the diff": {map[string]any{"author": "dana", "body": "x", "anchor": map[string]any{"path": "b.txt", "line": 1}}, 400},
		"unknown revision":     {map[string]any{"author": "dana", "body": "x", "revision": 2}, 400},
		"no body":              {map[string]any{"author": "dana", "body": " "}, 400},
		"no author":            {map[string]any{"body": "x"}, 400},
		"an agent's name":      {map[string]any{"author": "implementer", "body": "x"}, 403},
		"an agent's kind":      {map[string]any{"author": "bot", "kind": "agent", "body": "x"}, 403},
	} {
		if code, out := open(c.body); code != c.code {
			t.Errorf("%s: %d %s, want %d", name, code, out, c.code)
		}
	}

	if code, out := call(h.AddThreadComment, "", "thr-a", map[string]any{"author": "dana", "body": "also this"}); code != 200 || len(threads.m["thr-a"].Comments) != 2 {
		t.Errorf("reply: %d %s", code, out)
	}
	if code, _ := call(h.AddThreadComment, "", "thr-z", map[string]any{"author": "dana", "body": "x"}); code != 404 {
		t.Errorf("reply to an unknown thread: %d", code)
	}

	// Only humans resolve.
	for _, who := range []map[string]any{{"author": "reviewer"}, {"author": "head-engineer"}, {"author": "x", "kind": "agent"}} {
		if code, _ := call(h.ResolveThread, "", "thr-a", who); code != http.StatusForbidden {
			t.Errorf("resolve by %v: %d, want 403", who, code)
		}
	}
	if code, out := call(h.ResolveThread, "", "thr-a", map[string]any{"author": "dana"}); code != 200 || threads.m["thr-a"].Status != domain.ThreadResolved || threads.m["thr-a"].ResolvedBy != "dana" {
		t.Errorf("resolve: %d %s", code, out)
	}
	if code, _ := call(h.ResolveThread, "", "thr-a", map[string]any{"author": "dana"}); code != http.StatusConflict {
		t.Errorf("resolve twice: %d, want 409", code)
	}
	if got := h.unresolvedThreads(context.Background(), "p"); len(got) != 1 || got[0].ID != "thr-b" || got[0].Where != "general" {
		t.Errorf("unresolved: %+v", got)
	}
	if code, _ := call(h.ReopenThread, "", "thr-a", map[string]any{"author": "dana"}); code != 200 || threads.m["thr-a"].Status != domain.ThreadOpen || threads.m["thr-a"].ResolvedBy != "" {
		t.Errorf("reopen: %d", code)
	}

	// Nothing changes once the human has decided.
	for _, s := range []domain.TaskStatus{domain.StatusApproved, domain.StatusMerged, domain.StatusRejected, domain.StatusDecomposing} {
		task.Status = s
		if code, _ := open(map[string]any{"author": "dana", "body": "late"}); code != http.StatusConflict {
			t.Errorf("open while %s: %d, want 409", s, code)
		}
		if code, _ := call(h.ResolveThread, "", "thr-b", map[string]any{"author": "dana"}); code != http.StatusConflict {
			t.Errorf("resolve while %s: %d, want 409", s, code)
		}
	}
	task.Status = domain.StatusEscalated
	if code, out := open(map[string]any{"author": "dana", "body": "while escalated"}); code != http.StatusCreated {
		t.Errorf("open while ESCALATED: %d %s", code, out)
	}

	// A subtask takes no threads.
	child := &domain.Task{ID: "p", ParentID: "q", Status: domain.StatusReviewPending}
	h.Tasks = oneTask{task: child}
	if code, _ := open(map[string]any{"author": "dana", "body": "x"}); code != http.StatusBadRequest {
		t.Errorf("thread on a subtask: %d, want 400", code)
	}
}

type runReviews struct {
	store.ReviewRepo
	m map[string][]*domain.Review
}

func (r runReviews) ListByRun(_ context.Context, runID string) ([]*domain.Review, error) {
	return r.m[runID], nil
}

func TestReviewerNotesPerRevision(t *testing.T) {
	task := &domain.Task{ID: "p", IntegrationBranch: "forge/p", Status: domain.StatusEscalated,
		Revisions: []domain.Revision{{N: 1, FinalReviewRunID: "run-p-final-review"}}}
	h := &Handlers{Reviews: runReviews{m: map[string][]*domain.Review{
		"run-p-final-review": {
			{Round: 1, Reviewer: "reviewer", Verdict: "fail", Defects: []domain.Defect{{Severity: "major", Description: "superseded", Location: "a.js:1"}}},
			{Round: 2, Reviewer: "reviewer", Verdict: "pass", Defects: []domain.Defect{{Severity: "minor", Description: "name it", Location: "src/price.js:3"}}},
			{Round: 2, Reviewer: "security-reviewer", Verdict: "pass", Defects: []domain.Defect{{Severity: "minor", Description: "general"}}},
		},
		// r2's review, escalated: not a revision yet, so the work in progress.
		"run-p-final-review-r2": {{Round: 1, Reviewer: "reviewer", Verdict: "fail", Defects: []domain.Defect{{Severity: "critical", Description: "broken", Location: "src/price.js"}}}},
	}}}
	got := h.reviewerNotes(context.Background(), task)
	want := []string{"1 reviewer src/price.js:3 name it", "1 security-reviewer :0 general", "0 reviewer src/price.js:0 broken"}
	if len(got) != len(want) {
		t.Fatalf("notes: %+v", got)
	}
	for i, n := range got {
		if s := strings.Join([]string{string(rune('0' + n.Revision)), n.Reviewer, n.Path + ":" + string(rune('0'+n.Line)), n.Description}, " "); s != want[i] {
			t.Errorf("note %d: %q, want %q", i, s, want[i])
		}
	}
}
