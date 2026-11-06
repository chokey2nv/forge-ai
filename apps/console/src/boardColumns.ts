import type { Task, TaskStatus } from "./api";

export type Column = "backlog" | "todo" | "progress" | "review" | "done" | "closed";

export const COLUMNS: { id: Column; title: string; hint: string }[] = [
  { id: "backlog", title: "Backlog", hint: "Not started, and not planned yet." },
  { id: "todo", title: "Todo", hint: "Next up. Nothing runs until you start it." },
  { id: "progress", title: "In progress", hint: "Agents are working, or something stopped them." },
  { id: "review", title: "In review", hint: "Waiting for a person's decision, or being delivered." },
  { id: "done", title: "Done", hint: "Merged." },
  { id: "closed", title: "Closed", hint: "Rejected, rolled back, or closed after merging." },
];

const COLUMN_OF: Record<TaskStatus, Column> = {
  CREATED: "backlog", PLANNED: "todo",
  DECOMPOSING: "progress", DISPATCHED: "progress", RUNNING: "progress", VERIFYING: "progress",
  AGENT_REVIEW: "progress", INTEGRATING: "progress", BLOCKED: "progress", RESUMABLE: "progress", ROLLING_BACK: "progress",
  ESCALATED: "review", REVIEW_PENDING: "review", APPROVED: "review", DELIVERING: "review", PR_OPEN: "review", REVERT_PR_OPEN: "review",
  MERGED: "done", INTEGRATED: "done",
  REJECTED: "closed", ROLLED_BACK: "closed",
};

export function columnOf(t: Task): Column {
  // The head engineer has planned a subtask that hasn't started, so it's next up.
  if (t.status === "CREATED" && (t.parentId || t.boardLane === "todo")) return "todo";
  if (t.status === "MERGED" && t.boardLane === "closed") return "closed";
  return COLUMN_OF[t.status] ?? "progress";
}

export const STATUS_LABEL: Record<TaskStatus, string> = {
  CREATED: "Not started", PLANNED: "Planned", DECOMPOSING: "Planning", DISPATCHED: "In progress",
  RUNNING: "Agent working", VERIFYING: "Running checks", AGENT_REVIEW: "Agent review", ESCALATED: "Needs a decision",
  REVIEW_PENDING: "Waiting for approval", INTEGRATING: "Combining subtasks", INTEGRATED: "Integrated", APPROVED: "Approved",
  DELIVERING: "Delivering", PR_OPEN: "PR open", MERGED: "Merged", ROLLING_BACK: "Rolling back",
  REVERT_PR_OPEN: "Revert PR open", ROLLED_BACK: "Rolled back", BLOCKED: "Blocked", RESUMABLE: "Interrupted", REJECTED: "Rejected",
};

const NEEDS_YOU = new Set<TaskStatus>(["ESCALATED", "REVIEW_PENDING", "BLOCKED", "RESUMABLE"]);
export const needsYou = (t: Task) => NEEDS_YOU.has(t.status);

/** Subtasks are reached from their parent unless they need a person. */
export const onBoard = (t: Task, showSubtasks: boolean) => showSubtasks || !t.parentId || needsYou(t);

/** The agents with work on the board, for its filter. */
export const agentsOnBoard = (tasks: Task[]) =>
  [...new Set(tasks.flatMap((t) => t.assignedAgent ?? []))].sort();

export type Move = "backlog" | "todo" | "start" | "done" | "closed";

/** What dropping a card on a column does, or null if that move means nothing. */
export function moveTo(t: Task, to: Column): Move | null {
  if (t.parentId || to === columnOf(t)) return null;
  if (t.status === "CREATED") {
    if (to === "backlog" || to === "todo") return to;
    if (to === "progress") return "start";
  }
  if (t.status === "MERGED" && (to === "done" || to === "closed")) return to;
  return null;
}

/** What needs a person comes first, then whatever changed last. */
export function sortCards(cards: Task[]): Task[] {
  return [...cards].sort((a, b) =>
    Number(needsYou(b)) - Number(needsYou(a)) || Date.parse(b.updatedAt) - Date.parse(a.updatedAt));
}

export function matches(t: Task, query: string): boolean {
  const q = query.trim().toLowerCase();
  return !q || `${t.title ?? ""} ${t.requirement} ${t.id} ${t.assignedAgent ?? ""}`.toLowerCase().includes(q);
}

export function ago(iso: string, now = Date.now()): string {
  const min = Math.floor((now - Date.parse(iso)) / 60_000);
  if (min < 1) return "just now";
  if (min < 60) return `${min}m ago`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}
