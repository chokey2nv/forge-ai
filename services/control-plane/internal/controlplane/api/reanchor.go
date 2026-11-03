package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

// relocate follows line of path at an older commit to a newer one through
// diff (git diff -M older newer). It gives the line's path and number at
// the newer commit, or ok=false when the line was changed or removed (or
// its file deleted).
func relocate(diff, path string, line int) (newPath string, newLine int, ok bool) {
	var (
		inFile, inHunk   bool
		oldPath, curPath string
		deleted          bool
		offset           int
		oldNo, newNo     int
		done             bool
		result           int
	)
	newPath = path
	for _, l := range strings.Split(diff, "\n") {
		if strings.HasPrefix(l, "diff --git ") {
			if inFile {
				break // past the file's section
			}
			inHunk, oldPath, curPath, deleted = false, "", "", false
			continue
		}
		if !inHunk {
			switch {
			case strings.HasPrefix(l, "--- "):
				oldPath = diffPath(l[4:])
			case strings.HasPrefix(l, "rename from "):
				oldPath = l[len("rename from "):]
			case strings.HasPrefix(l, "+++ "):
				curPath = diffPath(l[4:])
			case strings.HasPrefix(l, "rename to "):
				curPath = l[len("rename to "):]
			case strings.HasPrefix(l, "deleted file mode"):
				deleted = true
			}
			if oldPath == path && !inFile {
				inFile = true
			}
			if inFile && curPath != "" {
				newPath = curPath
			}
		}
		if !inFile {
			continue
		}
		if deleted {
			return "", 0, false
		}
		if m := hunkHeader.FindStringSubmatch(l); m != nil {
			oldStart, _ := strconv.Atoi(m[1])
			newStart, _ := strconv.Atoi(m[2])
			oldCount, newCount := hunkCounts(l)
			first := oldStart // the first old line the hunk covers
			if oldCount == 0 {
				first = oldStart + 1 // a pure insertion after oldStart
			}
			if line < first {
				result, done = line+offset, true
				break
			}
			oldNo, newNo, inHunk = oldStart, newStart, true
			// A side with no lines names the line before the hunk.
			oldEnd, newEnd := oldStart+oldCount, newStart+newCount
			if oldCount == 0 {
				oldEnd++
			}
			if newCount == 0 {
				newEnd++
			}
			offset = newEnd - oldEnd
			continue
		}
		if !inHunk || l == "" {
			continue
		}
		switch l[0] {
		case ' ':
			if oldNo == line {
				return newPath, newNo, true
			}
			oldNo++
			newNo++
		case '-':
			if oldNo == line {
				return newPath, 0, false
			}
			oldNo++
		case '+':
			newNo++
		}
	}
	if done {
		return newPath, result, true
	}
	return newPath, line + offset, true
}

// hunkCounts reads a hunk header's line counts; an omitted count is 1.
func hunkCounts(header string) (oldCount, newCount int) {
	f := strings.Fields(header) // "@@" "-a,b" "+c,d" "@@" …
	count := func(s string) int {
		if i := strings.IndexByte(s, ','); i >= 0 {
			n, _ := strconv.Atoi(s[i+1:])
			return n
		}
		return 1
	}
	if len(f) >= 3 {
		return count(f[1]), count(f[2])
	}
	return 1, 1
}

// reanchorThreads places each open line thread on a newly recorded
// revision: moved to its line's new number when the line is unchanged
// since the revision it was shown on, otherwise marked outdated (with its
// last place and context kept). Resolved threads stay where they were.
func (h *Handlers) reanchorThreads(ctx context.Context, task *domain.Task, rev domain.Revision) {
	if h.Threads == nil {
		return
	}
	threads, err := h.Threads.ListByTask(ctx, task.ID)
	if err != nil {
		return
	}
	revDiff, err := workspace.DiffCommits(task.RepoPath, rev.BaseCommit, rev.Commit)
	if err != nil {
		return
	}
	between := map[string]string{} // older commit -> diff to rev.Commit
	now := time.Now().UTC()
	for _, t := range threads {
		if t.Anchor == nil || t.Outdated || !t.Unresolved() || t.Shown() >= rev.N {
			continue
		}
		moved, why := h.place(task, t, rev, revDiff, between)
		if moved != nil {
			_ = h.Threads.SavePlacement(ctx, t.ID, moved, rev.N, rev.Commit, 0, now)
			if moved.Line != t.Anchor.Line || moved.Path != t.Anchor.Path {
				_ = h.Events.Append(ctx, &domain.Event{TaskID: task.ID, Type: "thread.moved",
					Reason: fmt.Sprintf("%s: %s:%d -> %s:%d on r%d", t.ID, t.Anchor.Path, t.Anchor.Line, moved.Path, moved.Line, rev.N), At: now})
			}
			continue
		}
		_ = h.Threads.SavePlacement(ctx, t.ID, nil, 0, "", rev.N, now)
		_ = h.Events.Append(ctx, &domain.Event{TaskID: task.ID, Type: "thread.outdated",
			Reason: fmt.Sprintf("%s on %s: %s in r%d", t.ID, t.Where(), why, rev.N), At: now})
	}
}

// place finds a thread's line on rev, or says why it cannot.
func (h *Handlers) place(task *domain.Task, t *domain.Thread, rev domain.Revision, revDiff string, between map[string]string) (*domain.Anchor, string) {
	a := t.Anchor
	if a.Side == "old" {
		// The old side is the base, which revisions share: the line stays
		// while the new revision still deletes it.
		if moved, err := diffLine(revDiff, anchorRequest{Path: a.Path, Side: "old", Line: a.Line}); err == nil && moved.LineText == a.LineText {
			return moved, ""
		}
		return nil, "the deleted line is no longer part of the change"
	}
	diff, ok := between[t.Commit]
	if !ok {
		var err error
		if diff, err = workspace.DiffCommits(task.RepoPath, t.Commit, rev.Commit); err != nil {
			return nil, "cannot compare revisions: " + err.Error()
		}
		between[t.Commit] = diff
	}
	path, line, ok := relocate(diff, a.Path, a.Line)
	if !ok {
		if path == "" {
			return nil, "the file was deleted"
		}
		return nil, "the line was changed"
	}
	moved, err := diffLine(revDiff, anchorRequest{Path: path, Side: "new", Line: line})
	if err != nil || moved.LineText != a.LineText {
		return nil, "the line is no longer part of the change"
	}
	return moved, ""
}

// threadReplies are the reply_to_thread calls in a follow-up subtask's
// conversation, the last per thread, resolved against the threads the
// subtask addresses (T1 is its first). Unknown references are returned apart.
func threadReplies(addresses []string, messages []domain.Message) (replies map[string]string, order []string, unknown []string) {
	refs := map[string]string{}
	for i, id := range addresses {
		refs[fmt.Sprintf("T%d", i+1)] = id
		refs[id] = id
	}
	replies = map[string]string{}
	for _, m := range messages {
		for _, c := range m.ToolCalls {
			if c.Name != "reply_to_thread" {
				continue
			}
			ref, _ := c.Arguments["thread"].(string)
			body, _ := c.Arguments["body"].(string)
			id, ok := refs[strings.TrimSpace(ref)]
			if !ok || strings.TrimSpace(body) == "" {
				unknown = append(unknown, ref)
				continue
			}
			if _, seen := replies[id]; !seen {
				order = append(order, id)
			}
			replies[id] = strings.TrimSpace(body)
		}
	}
	return replies, order, unknown
}

// postThreadReplies posts a follow-up subtask's replies once its work is
// integrated, as agent comments on the revision being prepared, and marks
// those threads addressed (a resolved thread stays resolved).
func (h *Handlers) postThreadReplies(ctx context.Context, child *domain.Task, run *domain.WorkflowRun) {
	if h.Threads == nil || len(child.Addresses) == 0 || run.LastCheckpoint == nil {
		return
	}
	replies, order, unknown := threadReplies(child.Addresses, run.LastCheckpoint.Messages)
	revision := 1
	if parent, err := h.Tasks.Get(ctx, child.ParentID); err == nil {
		revision = len(parent.Revisions) + 1
	}
	now := time.Now().UTC()
	author := child.AssignedAgent
	if author == "" {
		author = "implementer"
	}
	for _, id := range order {
		t, err := h.Threads.Get(ctx, id)
		if err != nil {
			continue
		}
		_ = h.Threads.AddComment(ctx, id, domain.Comment{Author: author, Kind: domain.CommentAgent, Body: replies[id], Revision: revision, At: now})
		if t.Status != domain.ThreadResolved {
			_ = h.Threads.SetStatus(ctx, id, domain.ThreadAddressed, "", now)
		}
		_ = h.Events.Append(ctx, &domain.Event{TaskID: child.ParentID, RunID: run.ID, Type: "thread.replied", Reason: id + " by " + author + " (" + child.ID + ")", At: now})
	}
	for _, ref := range unknown {
		_ = h.Events.Append(ctx, &domain.Event{TaskID: child.ID, RunID: run.ID, Type: "thread.reply_ignored",
			Reason: fmt.Sprintf("%q is not a thread this task addresses, or the reply is empty", ref), At: now})
	}
}
