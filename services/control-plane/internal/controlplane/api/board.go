package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

// boardLaneFor is the lane a person's move stores, or why the move can't be
// made. The pipeline's status decides every other column, so only a task not
// started yet and a merged one can be moved by hand; approving and starting
// have their own calls.
func boardLaneFor(task *domain.Task, column string) (domain.BoardLane, string) {
	if task.ParentID != "" {
		return "", "a subtask follows its parent"
	}
	switch column {
	case "backlog", "todo":
		if task.Status != domain.StatusCreated {
			return "", "it has already started"
		}
		if column == "todo" {
			return domain.LaneTodo, ""
		}
		return "", ""
	case "done", "closed":
		if task.Status != domain.StatusMerged {
			return "", "only a merged task can move between done and closed"
		}
		if column == "closed" {
			return domain.LaneClosed, ""
		}
		return "", ""
	}
	return "", "no such column: " + column
}

// MoveOnBoard moves a card between Backlog and Todo, or Done and Closed
// (POST /tasks/{id}/board). It starts nothing and calls no model.
func (h *Handlers) MoveOnBoard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	task, err := h.Tasks.Get(ctx, r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Column string `json:"column"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	lane, why := boardLaneFor(task, body.Column)
	if why != "" {
		http.Error(w, "can't move it: "+why, http.StatusConflict)
		return
	}
	now := time.Now().UTC()
	if err := h.Tasks.SetBoardLane(ctx, task.ID, lane, now); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a := actorOf(r)
	_ = h.Events.Append(ctx, &domain.Event{TaskID: task.ID, Type: "board.moved", Payload: body,
		Actor: a.Name, ActorID: a.ID, At: now})
	task.BoardLane, task.UpdatedAt = lane, now
	writeJSON(w, http.StatusOK, task)
}
