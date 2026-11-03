package api

import (
	"context"
	"strconv"
	"testing"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

func TestRelocate(t *testing.T) {
	// old: a b c d e f g h i j (lines 1-10); new: inserts X after b, changes e,
	// deletes h.
	const diff = `diff --git a/f.txt b/f.txt
index 1..2 100644
--- a/f.txt
+++ b/f.txt
@@ -1,9 +1,9 @@
 a
 b
+X
 c
 d
-e
+E
 f
 g
-h
 i
diff --git a/old/name.js b/new/name.js
similarity index 100%
rename from old/name.js
rename to new/name.js
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
--- a/gone.txt
+++ /dev/null
@@ -1 +0,0 @@
-bye
diff --git a/far.txt b/far.txt
--- a/far.txt
+++ b/far.txt
@@ -20,0 +21,2 @@
+new1
+new2
@@ -40 +42 @@
-old40
+new40
`
	cases := []struct {
		path string
		line int
		want string
		ok   bool
	}{
		{"f.txt", 1, "f.txt:1", true},
		{"f.txt", 3, "f.txt:4", true}, // c moves down past the insertion
		{"f.txt", 5, "", false},       // e changed
		{"f.txt", 7, "f.txt:8", true},
		{"f.txt", 8, "", false},         // h deleted
		{"f.txt", 9, "f.txt:9", true},   // i: +1 inserted, -1 deleted
		{"f.txt", 12, "f.txt:12", true}, // past the hunk: net offset 0
		{"old/name.js", 4, "new/name.js:4", true},
		{"gone.txt", 1, "", false},
		{"far.txt", 20, "far.txt:20", true}, // the line the insertion follows
		{"far.txt", 21, "far.txt:23", true},
		{"far.txt", 40, "", false},
		{"far.txt", 41, "far.txt:43", true},
		{"untouched.txt", 7, "untouched.txt:7", true},
	}
	for _, c := range cases {
		p, l, ok := relocate(diff, c.path, c.line)
		got := ""
		if ok {
			got = p + ":" + strconv.Itoa(l)
		}
		if ok != c.ok || got != c.want {
			t.Errorf("%s:%d -> %q ok=%v, want %q ok=%v", c.path, c.line, got, ok, c.want, c.ok)
		}
	}
}

func TestThreadReplies(t *testing.T) {
	msgs := []domain.Message{
		{Role: "assistant", ToolCalls: []domain.ToolCall{
			{Name: "write_file", Arguments: map[string]any{"path": "x"}},
			{Name: "reply_to_thread", Arguments: map[string]any{"thread": "T1", "body": "first try"}},
		}},
		{Role: "assistant", ToolCalls: []domain.ToolCall{
			{Name: "reply_to_thread", Arguments: map[string]any{"thread": "T1", "body": "Named it CENTS."}},
			{Name: "reply_to_thread", Arguments: map[string]any{"thread": "thr-b", "body": "Kept as is."}},
			{Name: "reply_to_thread", Arguments: map[string]any{"thread": "T3", "body": "not mine"}},
			{Name: "reply_to_thread", Arguments: map[string]any{"thread": "thr-a", "body": " "}},
		}},
	}
	replies, order, unknown := threadReplies([]string{"thr-a", "thr-b"}, msgs)
	if replies["thr-a"] != "Named it CENTS." || replies["thr-b"] != "Kept as is." || len(order) != 2 || order[0] != "thr-a" {
		t.Errorf("replies %v order %v", replies, order)
	}
	if len(unknown) != 2 || unknown[0] != "T3" {
		t.Errorf("unknown %v", unknown)
	}
}

func TestReanchorThreadsOnANewRevision(t *testing.T) {
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	base := commitFile(t, repo, "p.js", "// p\nfunction f(x) {\n  return x;\n}\nmodule.exports = { f };\n")
	gitT(t, repo, "checkout", "-q", "-b", "forge/p")
	r1 := commitFile(t, repo, "p.js", "// p\nfunction f(x) {\n  return x * 100 / 100;\n}\nmodule.exports = { f };\n")
	r2 := commitFile(t, repo, "p.js", "// p\nconst CENTS = 100;\n\nfunction f(x) {\n  return x * CENTS / CENTS;\n}\nmodule.exports = { f };\n")

	task := &domain.Task{ID: "p", RepoPath: repo, IntegrationBranch: "forge/p", IntegrationBaseCommit: base,
		Revisions: []domain.Revision{{N: 1, Commit: r1, BaseCommit: base}}}
	threads := &memThreads{m: map[string]*domain.Thread{}}
	h := &Handlers{Tasks: oneTask{task: task}, Threads: threads, Events: noEvents{}}
	diff1 := mustDiff(t, repo, base, r1)
	open := func(side string, line int, status domain.ThreadStatus) {
		a, err := diffLine(diff1, anchorRequest{Path: "p.js", Side: side, Line: line})
		if err != nil {
			t.Fatal(err)
		}
		_ = threads.Create(context.Background(), &domain.Thread{TaskID: "p", Revision: 1, ShownOn: 1, Commit: r1, Anchor: a, Status: status})
	}
	open("new", 3, domain.ThreadOpen)                                                                                                     // thr-a: the return line, rewritten in r2
	open("new", 5, domain.ThreadOpen)                                                                                                     // thr-b: module.exports, moves down 2
	open("old", 3, domain.ThreadOpen)                                                                                                     // thr-c: the deleted "return x;", still deleted
	open("new", 2, domain.ThreadResolved)                                                                                                 // thr-d: resolved, left alone
	_ = threads.Create(context.Background(), &domain.Thread{TaskID: "p", Revision: 1, ShownOn: 1, Commit: r1, Status: domain.ThreadOpen}) // general

	rev := domain.Revision{N: 2, Commit: r2, BaseCommit: base}
	h.reanchorThreads(context.Background(), task, rev)

	if a := threads.m["thr-a"]; !a.Outdated || a.OutdatedOn != 2 || a.ShownOn != 1 || a.Anchor.Line != 3 {
		t.Errorf("rewritten line: %+v", a)
	}
	if b := threads.m["thr-b"]; b.Outdated || b.ShownOn != 2 || b.Commit != r2 || b.Anchor.Line != 7 || b.Anchor.LineText != "module.exports = { f };" {
		t.Errorf("moved line: %+v %+v", b, b.Anchor)
	}
	if c := threads.m["thr-c"]; c.Outdated || c.ShownOn != 2 || c.Anchor.Line != 3 {
		t.Errorf("deleted line still deleted: %+v", c)
	}
	if d := threads.m["thr-d"]; d.ShownOn != 1 || d.Outdated {
		t.Errorf("resolved thread moved: %+v", d)
	}
	if e := threads.m["thr-e"]; e.ShownOn != 1 || e.Outdated {
		t.Errorf("general thread changed: %+v", e)
	}
}

func mustDiff(t *testing.T, repo, from, to string) string {
	t.Helper()
	return gitT(t, repo, "diff", "--no-color", "-M", from, to) + "\n"
}
