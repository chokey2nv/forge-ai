package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

// recordRevision records the parent branch's tip as the next revision, once
// the combined change has been put up for human review. reviewRunID is the
// final review that passed it (or the escalated one a human accepted).
func (h *Handlers) recordRevision(ctx context.Context, task *domain.Task, reviewRunID string) {
	commit, err := workspace.ResolveCommit(ctx, task.RepoPath, task.IntegrationBranch)
	if err != nil {
		_ = h.Events.Append(ctx, &domain.Event{TaskID: task.ID, Type: "revision.failed", Reason: err.Error(), At: time.Now().UTC()})
		return
	}
	rev := domain.Revision{
		N:                len(task.Revisions) + 1,
		Commit:           commit,
		BaseCommit:       task.IntegrationBaseCommit,
		FinalReviewRunID: reviewRunID,
		At:               time.Now().UTC(),
	}
	if err := h.Tasks.AddRevision(ctx, task.ID, rev, rev.At); err != nil {
		_ = h.Events.Append(ctx, &domain.Event{TaskID: task.ID, Type: "revision.failed", Reason: err.Error(), At: rev.At})
		return
	}
	task.Revisions = append(task.Revisions, rev)
	_ = h.Events.Append(ctx, &domain.Event{
		TaskID: task.ID, Type: "revision.recorded",
		Reason: "r" + strconv.Itoa(rev.N) + " at " + shortSHA(commit), At: rev.At,
	})
}

// taskDiff is what GET /tasks/{id}/diff serves. Revision 0 is the work in
// progress: the parent branch's current tip, before (or between) reviews.
type taskDiff struct {
	TaskID    string            `json:"taskId"`
	Revisions []domain.Revision `json:"revisions"`
	Revision  int               `json:"revision"`
	Since     int               `json:"since,omitempty"`
	From      string            `json:"from"`
	To        string            `json:"to"`
	Diff      string            `json:"diff"`
}

// GetTaskDiff serves a task's change: for a parent, the combined diff of a
// revision (?revision=n, default the latest; work in progress if none) or
// the changes between two revisions (?since=m); for any other task, its
// latest run's diff.
func (h *Handlers) GetTaskDiff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	task, err := h.Tasks.Get(ctx, r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	out := taskDiff{TaskID: task.ID, Revisions: task.Revisions}
	if out.Revisions == nil {
		out.Revisions = []domain.Revision{}
	}

	if !isParentReview(task) {
		if task.LatestRunID != "" {
			if run, err := h.Runs.Get(ctx, task.LatestRunID); err == nil && run.LastCheckpoint != nil {
				out.Diff = run.LastCheckpoint.WorkspaceDiff
			}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	revision := func(name string, def int) (int, bool) {
		v := r.URL.Query().Get(name)
		if v == "" {
			return def, true
		}
		n, err := strconv.Atoi(v)
		return n, err == nil && n >= 0 && n <= len(task.Revisions)
	}
	n, ok := revision("revision", len(task.Revisions))
	if !ok {
		http.Error(w, "no such revision", http.StatusNotFound)
		return
	}
	since, ok := revision("since", 0)
	if !ok || (since > 0 && (n == 0 || since >= n)) {
		http.Error(w, "since must name an earlier revision", http.StatusBadRequest)
		return
	}

	out.Revision, out.Since = n, since
	if n == 0 {
		out.From, out.To = task.IntegrationBaseCommit, task.IntegrationBranch
	} else {
		out.From, out.To = task.Revisions[n-1].BaseCommit, task.Revisions[n-1].Commit
	}
	if since > 0 {
		out.From = task.Revisions[since-1].Commit
	}
	if out.Diff, err = workspace.DiffCommits(task.RepoPath, out.From, out.To); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
