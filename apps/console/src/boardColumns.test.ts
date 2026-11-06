import { test } from "node:test";
import assert from "node:assert/strict";
import { agentsOnBoard, ago, columnOf, matches, moveTo, onBoard, sortCards, COLUMNS } from "./boardColumns.ts";
import type { Task, TaskStatus } from "./api.ts";

const task = (status: TaskStatus, more: Partial<Task> = {}): Task => ({
  id: "t", requirement: "r", acceptance: [], status, createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-01T00:00:00Z", ...more,
});

test("every status lands in the column a person would look for it", () => {
  const want: Record<string, TaskStatus[]> = {
    backlog: ["CREATED"],
    todo: ["PLANNED"],
    progress: ["DECOMPOSING", "DISPATCHED", "RUNNING", "VERIFYING", "AGENT_REVIEW", "INTEGRATING", "BLOCKED", "RESUMABLE", "ROLLING_BACK"],
    review: ["ESCALATED", "REVIEW_PENDING", "APPROVED", "DELIVERING", "PR_OPEN", "REVERT_PR_OPEN"],
    done: ["MERGED", "INTEGRATED"],
    closed: ["REJECTED", "ROLLED_BACK"],
  };
  for (const [column, statuses] of Object.entries(want)) {
    for (const s of statuses) assert.equal(columnOf(task(s)), column, s);
  }
  assert.equal(COLUMNS.length, Object.keys(want).length);
});

test("the stored lane only counts while its status allows it", () => {
  assert.equal(columnOf(task("CREATED", { boardLane: "todo" })), "todo");
  assert.equal(columnOf(task("MERGED", { boardLane: "closed" })), "closed");
  assert.equal(columnOf(task("RUNNING", { boardLane: "todo" })), "progress", "started from Todo");
  assert.equal(columnOf(task("ROLLED_BACK", { boardLane: "closed" })), "closed");
  assert.equal(columnOf(task("ROLLING_BACK", { boardLane: "closed" })), "progress", "a closed task being rolled back is moving again");
});

test("a planned subtask that hasn't started is next up", () => {
  assert.equal(columnOf(task("CREATED", { parentId: "p" })), "todo");
});

test("only moves that mean something are offered", () => {
  const backlog = task("CREATED");
  assert.equal(moveTo(backlog, "todo"), "todo");
  assert.equal(moveTo(backlog, "progress"), "start");
  assert.equal(moveTo(backlog, "backlog"), null, "already there");
  assert.equal(moveTo(backlog, "done"), null);
  assert.equal(moveTo(task("CREATED", { boardLane: "todo" }), "backlog"), "backlog");
  assert.equal(moveTo(task("MERGED"), "closed"), "closed");
  assert.equal(moveTo(task("MERGED", { boardLane: "closed" }), "done"), "done");
  assert.equal(moveTo(task("REVIEW_PENDING"), "done"), null, "approving happens where the change can be seen");
  assert.equal(moveTo(task("RUNNING"), "backlog"), null);
  assert.equal(moveTo(task("REJECTED"), "done"), null);
  assert.equal(moveTo(task("CREATED", { parentId: "p" }), "progress"), null, "a subtask follows its parent");
});

test("subtasks appear when asked for, or when they need a person", () => {
  assert.equal(onBoard(task("RUNNING", { parentId: "p" }), false), false);
  assert.equal(onBoard(task("RUNNING", { parentId: "p" }), true), true);
  assert.equal(onBoard(task("BLOCKED", { parentId: "p" }), false), true);
  assert.equal(onBoard(task("RUNNING"), false), true);
});

test("cards that need a person come first, then the latest", () => {
  const ids = sortCards([
    task("RUNNING", { id: "old", updatedAt: "2026-11-01T00:00:00Z" }),
    task("RUNNING", { id: "new", updatedAt: "2026-11-03T00:00:00Z" }),
    task("BLOCKED", { id: "stuck", updatedAt: "2026-10-01T00:00:00Z" }),
  ]).map((t) => t.id);
  assert.deepEqual(ids, ["stuck", "new", "old"]);
});

test("search looks at the title, requirement, id and agent", () => {
  const t = task("RUNNING", { id: "task-abc", title: "Cart totals", requirement: "fix rounding", assignedAgent: "qa-engineer" });
  for (const q of ["cart", "ROUNDING", "task-abc", "qa-", "  "]) assert.equal(matches(t, q), true, q);
  assert.equal(matches(t, "login"), false);
});

test("ages read in the largest whole unit", () => {
  const now = Date.parse("2026-11-05T12:00:00Z");
  assert.equal(ago("2026-11-05T11:59:40Z", now), "just now");
  assert.equal(ago("2026-11-05T11:15:00Z", now), "45m ago");
  assert.equal(ago("2026-11-05T09:00:00Z", now), "3h ago");
  assert.equal(ago("2026-11-02T12:00:00Z", now), "3d ago");
});

test("the agent filter lists each agent with work once, in order", () => {
  const tasks = [task("RUNNING", { assignedAgent: "qa-engineer" }), task("CREATED"), task("MERGED", { assignedAgent: "implementer" }), task("VERIFYING", { assignedAgent: "qa-engineer" })];
  assert.deepEqual(agentsOnBoard(tasks), ["implementer", "qa-engineer"]);
});
