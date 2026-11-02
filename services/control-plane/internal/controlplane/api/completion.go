package api

import (
	"context"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/agent"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

// completionKind is what an agent's finished run means to the pipeline. It
// comes from the agent definition's submission_tool, so a new agent routes
// correctly from its markdown alone (a "security-reviewer" with
// submission_tool: submit_review is handled as a review).
type completionKind string

const (
	kindWork      completionKind = "work"      // no submission tool: edits code
	kindDecompose completionKind = "decompose" // submit_task_graph
	kindPlan      completionKind = "plan"      // submit_plan
	kindReview    completionKind = "review"    // submit_review
)

var kindBySubmissionTool = map[string]completionKind{
	"submit_task_graph": kindDecompose,
	"submit_plan":       kindPlan,
	"submit_review":     kindReview,
}

type completionHandler func(h *Handlers, ctx context.Context, run *domain.WorkflowRun, task *domain.Task, body AgentCompleteBody)

var completionHandlers = map[completionKind]completionHandler{
	kindWork:      (*Handlers).handleImplementerDone,
	kindDecompose: (*Handlers).handleDecompositionDone,
	kindPlan: func(h *Handlers, _ context.Context, run *domain.WorkflowRun, task *domain.Task, body AgentCompleteBody) {
		h.handlePlannerDone(run, task, body)
	},
	kindReview: func(h *Handlers, _ context.Context, run *domain.WorkflowRun, task *domain.Task, body AgentCompleteBody) {
		h.handleReviewerDone(run, task, body)
	},
}

// completionKindOf resolves the reporting agent. Callbacks are keyed by the
// agent that ran (the adapter echoes StartRun's Role), not by the run's
// provenance: a reviewer reports against the implementer's run ID.
// Unregistered agents are treated as work agents.
func (h *Handlers) completionKindOf(agentName string) completionKind {
	if h.Registry != nil {
		if def, ok := h.Registry.GetAgent(agentName); ok {
			if k, ok := kindBySubmissionTool[def.SubmissionTool]; ok {
				return k
			}
		}
	}
	return kindWork
}

// assignableAgents are the agents a decomposition may hand subtasks to: work
// agents. Reviewers and planners run as part of each subtask's pipeline and
// have no code of their own to produce; the head engineer does not recurse.
func (h *Handlers) assignableAgents() []*agent.AgentDefinition {
	var out []*agent.AgentDefinition
	for _, a := range h.Registry.ListAgents() {
		if h.completionKindOf(a.Name) == kindWork {
			out = append(out, a)
		}
	}
	return out
}

func (h *Handlers) dispatchCompletion(ctx context.Context, run *domain.WorkflowRun, task *domain.Task, body AgentCompleteBody) {
	completionHandlers[h.completionKindOf(body.Role)](h, ctx, run, task, body)
}

// handleDecompositionDone handles a submit_task_graph agent (the head engineer).
func (h *Handlers) handleDecompositionDone(ctx context.Context, run *domain.WorkflowRun, task *domain.Task, body AgentCompleteBody) {
	// The decomposition workspace is read-only scratch; children get their own.
	defer workspace.Cleanup(context.Background(), &workspace.Workspace{
		WorktreePath: run.WorktreePath, ContainerName: run.ContainerName,
	}, task.RepoPath)
	if body.Status != "complete" || body.Submission == nil {
		_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunBlocked, body.FinalMessage, time.Now().UTC())
		_ = h.SM.Transition(context.Background(), task, domain.StatusBlocked, "decomposition failed: "+body.FinalMessage)
		return
	}
	_ = h.Runs.UpdateStatus(ctx, run.ID, domain.RunComplete, "", time.Now().UTC())
	h.handleHeadEngineerDone(run, task, body.Submission)
}
