package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

// Threads can be opened, answered, resolved and reopened only while a human
// is looking at the change: up for review, or escalated to them.
func feedbackOpen(task *domain.Task) bool {
	return task.Status == domain.StatusReviewPending || task.Status == domain.StatusEscalated
}

// anchorContext is how many lines of the hunk an anchor keeps on each side.
const anchorContext = 3

type anchorRequest struct {
	Path string `json:"path"`
	Side string `json:"side"` // "new" (default) | "old"
	Line int    `json:"line"`
}

// errNotInDiff answers 400: an anchor must name a line the revision's diff shows.
var errNotInDiff = errors.New("not in the diff")

// diffLine finds a line of a unified diff: a line of path on one side, as
// the diff shows it (added or context lines on the new side, deleted or
// context lines on the old). It returns the anchor with the line's text and
// its neighbours in the hunk.
func diffLine(diff string, req anchorRequest) (*domain.Anchor, error) {
	if req.Side == "" {
		req.Side = "new"
	}
	if req.Side != "new" && req.Side != "old" {
		return nil, errors.New(`side must be "new" or "old"`)
	}
	if req.Path == "" || req.Line < 1 {
		return nil, errors.New("anchor needs a path and a line")
	}

	type line struct {
		text string
		no   int
	}
	var (
		oldPath, newPath string
		inHunk           bool
		oldNo, newNo     int
		hunk             []line // the hunk's lines on the requested side
		found            = -1
		fileSeen         bool
	)
	match := func() bool {
		if req.Side == "new" {
			return newPath == req.Path
		}
		return oldPath == req.Path
	}
	finish := func() (*domain.Anchor, bool) {
		if found < 0 {
			hunk = hunk[:0]
			return nil, false
		}
		a := &domain.Anchor{Path: req.Path, Side: req.Side, Line: req.Line, LineText: hunk[found].text}
		for i := max(0, found-anchorContext); i < found; i++ {
			a.Before = append(a.Before, hunk[i].text)
		}
		for i := found + 1; i < len(hunk) && i <= found+anchorContext; i++ {
			a.After = append(a.After, hunk[i].text)
		}
		return a, true
	}

	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			if a, ok := finish(); ok {
				return a, nil
			}
			inHunk, oldPath, newPath = false, "", ""
			continue
		case !inHunk && strings.HasPrefix(l, "--- "):
			oldPath = diffPath(l[4:])
			continue
		case !inHunk && strings.HasPrefix(l, "+++ "):
			newPath = diffPath(l[4:])
			fileSeen = fileSeen || match()
			continue
		case strings.HasPrefix(l, "@@ "):
			if a, ok := finish(); ok {
				return a, nil
			}
			m := hunkHeader.FindStringSubmatch(l)
			if m == nil {
				inHunk = false
				continue
			}
			oldNo, _ = strconv.Atoi(m[1])
			newNo, _ = strconv.Atoi(m[2])
			inHunk = true
			continue
		}
		if !inHunk || !match() || l == "" {
			continue
		}
		kind, text := l[0], l[1:]
		var no int
		switch {
		case kind == ' ' && req.Side == "new", kind == '+':
			no = newNo
		case kind == ' ' && req.Side == "old", kind == '-':
			no = oldNo
		}
		if kind == ' ' || kind == '-' {
			oldNo++
		}
		if kind == ' ' || kind == '+' {
			newNo++
		}
		if no == 0 || (kind == '+' && req.Side == "old") || (kind == '-' && req.Side == "new") {
			continue
		}
		if no == req.Line {
			found = len(hunk)
		}
		hunk = append(hunk, line{text, no})
	}
	if a, ok := finish(); ok {
		return a, nil
	}
	if !fileSeen {
		return nil, errors.New(req.Path + " is " + errNotInDiff.Error())
	}
	return nil, errors.New(req.Path + ":" + strconv.Itoa(req.Line) + " (" + req.Side + ") is " + errNotInDiff.Error())
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// diffPath is a "--- a/x" or "+++ b/x" path without its prefix; /dev/null
// (a new or deleted file's missing side) is empty.
func diffPath(p string) string {
	p = strings.TrimSuffix(p, "\t")
	if strings.HasPrefix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			p = u
		}
	}
	if p == "/dev/null" {
		return ""
	}
	if len(p) > 2 && (p[:2] == "a/" || p[:2] == "b/") {
		return p[2:]
	}
	return p
}

// revisionDiff is the combined change a thread on revision n refers to: the
// commit it anchors to and its diff from the parent's base. Revision 0 is
// the work in progress, pinned to the branch's current tip.
func revisionDiff(ctx context.Context, task *domain.Task, n int) (commit, diff string, err error) {
	from := task.IntegrationBaseCommit
	if n == 0 {
		if commit, err = workspace.ResolveCommit(ctx, task.RepoPath, task.IntegrationBranch); err != nil {
			return "", "", err
		}
	} else {
		rev := task.Revisions[n-1]
		from, commit = rev.BaseCommit, rev.Commit
	}
	diff, err = workspace.DiffCommits(task.RepoPath, from, commit)
	return commit, diff, err
}

// threadTask loads the task a thread request is about and checks that it
// can take feedback now.
func (h *Handlers) threadTask(w http.ResponseWriter, r *http.Request) (*domain.Task, bool) {
	task, err := h.Tasks.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	if !isParentReview(task) {
		http.Error(w, "threads are on a parent task's combined change", http.StatusBadRequest)
		return nil, false
	}
	if !feedbackOpen(task) {
		http.Error(w, "threads can change only while the task is REVIEW_PENDING or ESCALATED (current: "+string(task.Status)+")", http.StatusConflict)
		return nil, false
	}
	return task, true
}

// humanAuthor answers 403 unless the request is a human's. Agents reply to
// threads through their work submissions, never through these endpoints,
// and only humans resolve. Until there is authentication (Phase 13) a
// human is anyone who does not claim to be an agent or use an agent's name.
func (h *Handlers) humanAuthor(w http.ResponseWriter, author, kind, doing string) bool {
	if strings.TrimSpace(author) == "" {
		http.Error(w, "author is required", http.StatusBadRequest)
		return false
	}
	isAgent := false
	if h.Registry != nil {
		_, isAgent = h.Registry.GetAgent(author)
	}
	if (kind != "" && kind != domain.CommentHuman) || isAgent {
		http.Error(w, "only a human can "+doing, http.StatusForbidden)
		return false
	}
	return true
}

func (h *Handlers) loadThread(w http.ResponseWriter, r *http.Request, task *domain.Task) (*domain.Thread, bool) {
	t, err := h.Threads.Get(r.Context(), r.PathValue("tid"))
	if err != nil || t.TaskID != task.ID {
		http.NotFound(w, r)
		return nil, false
	}
	return t, true
}

// threadList is what GET /tasks/{id}/threads serves.
type threadList struct {
	Threads []*domain.Thread `json:"threads"`
	Notes   []reviewerNote   `json:"notes"`
}

// ListThreads: GET /tasks/{id}/threads — the human threads, and the final
// reviewers' findings as read-only notes.
func (h *Handlers) ListThreads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	task, err := h.Tasks.Get(ctx, r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	threads, err := h.Threads.ListByTask(ctx, task.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, threadList{Threads: threads, Notes: h.reviewerNotes(ctx, task)})
}

// CreateThread: POST /tasks/{id}/threads {author, body, revision?, anchor?}.
// The anchor must be a line the revision's diff shows; without one the
// thread is a general comment on the change.
func (h *Handlers) CreateThread(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Author   string         `json:"author"`
		Kind     string         `json:"kind,omitempty"`
		Body     string         `json:"body"`
		Revision *int           `json:"revision,omitempty"`
		Anchor   *anchorRequest `json:"anchor,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	task, ok := h.threadTask(w, r)
	if !ok || !h.humanAuthor(w, body.Author, body.Kind, "open a thread") {
		return
	}
	if strings.TrimSpace(body.Body) == "" {
		http.Error(w, "body is required", http.StatusBadRequest)
		return
	}
	n := len(task.Revisions)
	if body.Revision != nil {
		n = *body.Revision
	}
	if n < 0 || n > len(task.Revisions) {
		http.Error(w, "no such revision", http.StatusBadRequest)
		return
	}
	commit, diff, err := revisionDiff(ctx, task, n)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()
	t := &domain.Thread{
		TaskID: task.ID, Revision: n, ShownOn: n, Commit: commit, Status: domain.ThreadOpen,
		Comments:  []domain.Comment{{Author: body.Author, Kind: domain.CommentHuman, Body: body.Body, Revision: n, At: now}},
		CreatedAt: now, UpdatedAt: now,
	}
	if body.Anchor != nil {
		if t.Anchor, err = diffLine(diff, *body.Anchor); err != nil {
			http.Error(w, "anchor: "+err.Error(), http.StatusBadRequest)
			return
		}
		origin := *t.Anchor
		t.Origin = &origin
	}
	if err := h.Threads.Create(ctx, t); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = h.Events.Append(ctx, &domain.Event{TaskID: task.ID, Type: "thread.opened", Reason: t.ID + " on " + t.Where() + " by " + body.Author, At: now})
	writeJSON(w, http.StatusCreated, t)
}

// AddThreadComment: POST /tasks/{id}/threads/{tid}/comments {author, body}.
func (h *Handlers) AddThreadComment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Author string `json:"author"`
		Kind   string `json:"kind,omitempty"`
		Body   string `json:"body"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	task, ok := h.threadTask(w, r)
	if !ok || !h.humanAuthor(w, body.Author, body.Kind, "reply here") {
		return
	}
	if strings.TrimSpace(body.Body) == "" {
		http.Error(w, "body is required", http.StatusBadRequest)
		return
	}
	t, ok := h.loadThread(w, r, task)
	if !ok {
		return
	}
	c := domain.Comment{Author: body.Author, Kind: domain.CommentHuman, Body: body.Body, Revision: len(task.Revisions), At: time.Now().UTC()}
	if err := h.Threads.AddComment(ctx, t.ID, c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	t.Comments, t.UpdatedAt = append(t.Comments, c), c.At
	writeJSON(w, http.StatusOK, t)
}

func (h *Handlers) ResolveThread(w http.ResponseWriter, r *http.Request) {
	h.setThreadStatus(w, r, domain.ThreadResolved)
}
func (h *Handlers) ReopenThread(w http.ResponseWriter, r *http.Request) {
	h.setThreadStatus(w, r, domain.ThreadOpen)
}

// setThreadStatus: POST /tasks/{id}/threads/{tid}/resolve|reopen {author}.
func (h *Handlers) setThreadStatus(w http.ResponseWriter, r *http.Request, to domain.ThreadStatus) {
	ctx := r.Context()
	var body struct {
		Author string `json:"author"`
		Kind   string `json:"kind,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	verb := map[domain.ThreadStatus]string{domain.ThreadResolved: "resolve", domain.ThreadOpen: "reopen"}[to]
	task, ok := h.threadTask(w, r)
	if !ok || !h.humanAuthor(w, body.Author, body.Kind, verb+" a thread") {
		return
	}
	t, ok := h.loadThread(w, r, task)
	if !ok {
		return
	}
	if (to == domain.ThreadResolved) == (t.Status == domain.ThreadResolved) {
		http.Error(w, "thread is already "+string(t.Status), http.StatusConflict)
		return
	}
	by := ""
	if to == domain.ThreadResolved {
		by = body.Author
	}
	now := time.Now().UTC()
	if err := h.Threads.SetStatus(ctx, t.ID, to, by, now); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	t.Status, t.ResolvedBy, t.UpdatedAt = to, by, now
	_ = h.Events.Append(ctx, &domain.Event{TaskID: task.ID, Type: "thread." + string(to), Reason: t.ID + " by " + body.Author, At: now})
	writeJSON(w, http.StatusOK, t)
}

// unresolvedThreads summarises the threads a decision leaves open.
func (h *Handlers) unresolvedThreads(ctx context.Context, taskID string) []domain.ThreadSummary {
	if h.Threads == nil {
		return nil
	}
	threads, err := h.Threads.ListByTask(ctx, taskID)
	if err != nil {
		return nil
	}
	var out []domain.ThreadSummary
	for _, t := range threads {
		if t.Unresolved() {
			out = append(out, t.Summary())
		}
	}
	return out
}

// reviewerNote is a final reviewer's finding, shown read-only on the diff of
// the revision it was found in. Path and Line are set when the defect's
// location names a file (and line).
type reviewerNote struct {
	Revision    int    `json:"revision"`
	Reviewer    string `json:"reviewer"`
	Verdict     string `json:"verdict"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
	Location    string `json:"location,omitempty"`
	Path        string `json:"path,omitempty"`
	Line        int    `json:"line,omitempty"`
}

// reviewerNotes collects the defects of each revision's final review (its
// last round, the one that judged that code). A final review not yet tied
// to a revision, such as an escalated one, belongs to the work in progress.
func (h *Handlers) reviewerNotes(ctx context.Context, task *domain.Task) []reviewerNote {
	notes := []reviewerNote{}
	if h.Reviews == nil || !isParentReview(task) {
		return notes
	}
	runs := map[string]int{}
	order := []string{}
	for _, rev := range task.Revisions {
		if _, dup := runs[rev.FinalReviewRunID]; !dup {
			order = append(order, rev.FinalReviewRunID)
		}
		runs[rev.FinalReviewRunID] = rev.N
	}
	if pending := finalReviewRunID(task); runs[pending] == 0 {
		runs[pending] = 0
		order = append(order, pending)
	}
	for _, runID := range order {
		reviews, err := h.Reviews.ListByRun(ctx, runID)
		if err != nil || len(reviews) == 0 {
			continue
		}
		last := reviews[len(reviews)-1].Round
		for _, rv := range reviews {
			if rv.Round != last {
				continue
			}
			for _, d := range rv.Defects {
				n := reviewerNote{
					Revision: runs[runID], Reviewer: rv.Reviewer, Verdict: rv.Verdict,
					Severity: d.Severity, Description: d.Description, Location: d.Location,
				}
				n.Path, n.Line = parseLocation(d.Location)
				notes = append(notes, n)
			}
		}
	}
	return notes
}

var (
	// path, path:12, path:12-20, path:12:5, path#L12
	locColon = regexp.MustCompile(`^(\S+?)(?:(?::|#L)(\d+)(?:[-:,]\S*)?)?$`)
	// "path line 12", "path, line 12", "path (line 12)", "path L12"
	locWords = regexp.MustCompile(`^(\S+?),?\s+\(?(?:lines?|L)\s*(\d+)`)
)

// parseLocation reads a reviewer's free-text defect location as a file path
// and, if it has one, a line. A location that does not look like a path (a
// function name, a sentence) gives no path.
func parseLocation(loc string) (path string, line int) {
	loc = strings.Trim(strings.TrimSpace(loc), "`'\"")
	m := locColon.FindStringSubmatch(loc)
	if m == nil {
		m = locWords.FindStringSubmatch(loc)
	}
	if m == nil {
		return "", 0
	}
	path = strings.TrimPrefix(m[1], "./")
	if !strings.ContainsAny(path, "./") || strings.HasSuffix(path, ".") || strings.Contains(path, "(") {
		return "", 0
	}
	if m[2] != "" {
		line, _ = strconv.Atoi(m[2])
	}
	return path, line
}
