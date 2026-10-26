import type { ModelTool } from "./types";

export type AgentRole = "planner" | "implementer" | "reviewer";

// Physical enforcement — a role cannot call a tool not in this list,
// because the tool isn't in the request to the model.
export const ROLE_TOOL_NAMES: Record<AgentRole, string[]> = {
  planner:     ["read_file", "list_dir", "search", "git_status", "git_diff"],
  implementer: ["read_file", "list_dir", "search", "write_file", "apply_patch", "run_command", "git_status", "git_diff"],
  reviewer:    ["read_file", "list_dir", "search", "git_status", "git_diff"],
};

export const SUBMIT_PLAN: ModelTool = {
  name: "submit_plan",
  description: "Submit the implementation plan. Call this when you have enough information about the codebase to describe the fix.",
  inputSchema: {
    type: "object",
    properties: {
      steps: {
        type: "array",
        items: {
          type: "object",
          properties: {
            description: { type: "string" },
            files: { type: "array", items: { type: "string" } },
          },
          required: ["description"],
        },
      },
      risks: { type: "array", items: { type: "string" } },
    },
    required: ["steps"],
  },
};

export const SUBMIT_REVIEW: ModelTool = {
  name: "submit_review",
  description: "Submit the review verdict. Call this when you have completed the review.",
  inputSchema: {
    type: "object",
    properties: {
      verdict: { type: "string", enum: ["pass", "fail"] },
      defects: {
        type: "array",
        items: {
          type: "object",
          properties: {
            severity: { type: "string", enum: ["critical", "major", "minor"] },
            description: { type: "string" },
            location: { type: "string" },
          },
          required: ["severity", "description"],
        },
      },
      summary: { type: "string" },
    },
    required: ["verdict", "summary"],
  },
};

export const ROLE_SUBMISSION_TOOL: Record<AgentRole, string> = {
  planner: "submit_plan",
  implementer: "",     // implementer terminates by producing no tool calls
  reviewer: "submit_review",
};

export const ROLE_SYSTEM_PROMPTS: Record<AgentRole, string> = {
  planner: `You are a planning agent working inside a sandboxed workspace.

You CANNOT modify files. Available tools are read-only: read_file, list_dir, search, git_status, git_diff.

Your job:
1. Read the relevant code and tests.
2. Identify the files that need to change.
3. Call submit_plan with an ordered list of steps and the files each step touches.

Be specific. Do not speculate about files you have not read. Do not call tools you do not have.`,

  implementer: `You are an autonomous implementer agent working inside a sandboxed workspace.

Tools: read_file, list_dir, search, write_file, apply_patch, run_command, git_diff, git_status.

Rules:
- Read before you write. Inspect relevant files and tests before changing anything.
- Make the smallest change that satisfies the task AND the acceptance criteria.
- Prefer apply_patch over write_file for modifications.
- Run the project's tests with run_command before declaring done.
- Consider edge cases explicitly mentioned in the acceptance criteria.
- Do not touch .git directly.
- When finished, reply with a short summary.
- If blocked, reply with a clear explanation.`,

  reviewer: `You are a code reviewer. You CANNOT modify files. Available tools are read-only: read_file, list_dir, search, git_status, git_diff.

Your job:
1. Read the diff provided in the task.
2. Check the diff against EACH acceptance criterion, not just the tests.
3. Use the read tools to inspect related files if needed.
4. Call submit_review with a verdict.

Set verdict="fail" if ANY acceptance criterion is unmet, even if tests pass. A passing test suite does not prove the criteria are met.

Be specific about defects: which criterion, which file, what is missing.`,
};