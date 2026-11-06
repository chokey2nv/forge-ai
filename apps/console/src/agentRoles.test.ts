import { test } from "node:test";
import assert from "node:assert/strict";
import { reviewersOf, reviews, roleOf } from "./agentRoles.ts";
import type { AgentDefinition, Task } from "./api.ts";

const agent = (name: string, more: Partial<AgentDefinition> = {}): AgentDefinition => ({
  name, description: "", version: 1, tools: [], systemPrompt: "", contentHash: "", loadedAt: "", ...more,
});
const task = (more: Partial<Task> = {}): Task => ({
  id: "t", requirement: "r", acceptance: [], status: "CREATED", createdAt: "", updatedAt: "", ...more,
});

const team = [
  agent("head-engineer", { submissionTool: "submit_task_graph", reviewers: ["reviewer", "security-reviewer"] }),
  agent("implementer", { reviewers: ["reviewer", "test-reviewer"] }),
  agent("qa-engineer"),
  agent("reviewer", { submissionTool: "submit_review" }),
];

test("an agent's role follows its submission tool, and none means it does the work", () => {
  assert.equal(roleOf(team[0]), "plans");
  assert.equal(roleOf(team[1]), "works");
  assert.equal(roleOf(team[3]), "reviews");
  assert.equal(roleOf(agent("planner", { submissionTool: "submit_plan" })), "steps");
});

test("a subtask is reviewed by its agent's list, a whole task by the head engineer's", () => {
  assert.deepEqual(reviewersOf(task({ parentId: "p", assignedAgent: "implementer" }), team), ["reviewer", "test-reviewer"]);
  assert.deepEqual(reviewersOf(task(), team), ["reviewer", "security-reviewer"]);
});

test("an agent with no reviewers, or one no longer registered, lists nobody", () => {
  assert.deepEqual(reviewersOf(task({ parentId: "p", assignedAgent: "qa-engineer" }), team), []);
  assert.deepEqual(reviewersOf(task({ parentId: "p", assignedAgent: "gone" }), team), []);
});

test("reviews lists whose work an agent checks", () => {
  assert.deepEqual(reviews("reviewer", team), ["head-engineer", "implementer"]);
  assert.deepEqual(reviews("implementer", team), []);
});
