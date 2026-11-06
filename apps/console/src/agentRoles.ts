import type { AgentDefinition, Task } from "./api";

export type Role = "plans" | "works" | "reviews" | "steps";

/** What each kind of agent does and who decides it runs, in pipeline order. */
export const ROLES: { id: Role; title: string; how: string }[] = [
  { id: "plans", title: "Plans the work", how: "Runs first on every task. It splits the task into subtasks and chooses an agent for each." },
  { id: "works", title: "Does the work", how: "The head engineer chooses one of these for each subtask, from their descriptions." },
  { id: "reviews", title: "Reviews the work", how: "Chosen by the reviewers list of the agent whose work it is. All of them must pass." },
  { id: "steps", title: "Plans the steps", how: "Runs only when PLANNER_ENABLED is on, and hasn't been verified with a real model." },
];

// The same meaning the control plane gives submission_tool (api/completion.go).
const ROLE_BY_SUBMISSION: Record<string, Role> = {
  submit_task_graph: "plans",
  submit_plan: "steps",
  submit_review: "reviews",
};

export const roleOf = (a: AgentDefinition): Role => ROLE_BY_SUBMISSION[a.submissionTool ?? ""] ?? "works";

/** Who reviews a task's work: its agent's list, or for a whole task, the head engineer's. */
export function reviewersOf(t: Task, agents: AgentDefinition[]): string[] {
  const who = t.parentId ? t.assignedAgent : "head-engineer";
  return agents.find((a) => a.name === who)?.reviewers ?? [];
}

/** The agents whose work this one reviews. */
export const reviews = (name: string, agents: AgentDefinition[]) =>
  agents.filter((a) => a.reviewers?.includes(name)).map((a) => a.name);
