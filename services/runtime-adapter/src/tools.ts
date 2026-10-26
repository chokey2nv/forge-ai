import type { ModelTool } from "./types";
import { ROLE_TOOL_NAMES, ROLE_SUBMISSION_TOOL, SUBMIT_PLAN, SUBMIT_REVIEW, type AgentRole } from "./roles";

export function toolsForRole(role: AgentRole): ModelTool[] {
  const allowed = new Set(ROLE_TOOL_NAMES[role]);
  const base = TOOLS.filter((t) => allowed.has(t.name));

  const sub = ROLE_SUBMISSION_TOOL[role];
  if (sub === "submit_plan")   return [...base, SUBMIT_PLAN];
  if (sub === "submit_review") return [...base, SUBMIT_REVIEW];
  return base;
}

export const TOOLS: ModelTool[] = [
  {
    name: "read_file",
    description: "Read a file from the workspace. Optional startLine/endLine for a range.",
    inputSchema: {
      type: "object",
      properties: {
        path: { type: "string", description: "Path relative to workspace root" },
        startLine: { type: "number" },
        endLine: { type: "number" },
      },
      required: ["path"],
    },
  },
  {
    name: "list_dir",
    description: "List files under a directory, depth-limited.",
    inputSchema: {
      type: "object",
      properties: {
        path: { type: "string" },
        depth: { type: "number" },
      },
      required: ["path"],
    },
  },
  {
    name: "search",
    description: "Search for a literal pattern across the workspace (git grep).",
    inputSchema: {
      type: "object",
      properties: {
        query: { type: "string" },
        glob: { type: "string", description: "Optional path glob filter" },
      },
      required: ["query"],
    },
  },
  {
    name: "write_file",
    description: "Write or overwrite a file. Returns the resulting git diff.",
    inputSchema: {
      type: "object",
      properties: {
        path: { type: "string" },
        content: { type: "string" },
      },
      required: ["path", "content"],
    },
  },
  {
    name: "apply_patch",
    description: "Apply a unified diff to the workspace. Prefer this over write_file for edits.",
    inputSchema: {
      type: "object",
      properties: { diff: { type: "string" } },
      required: ["diff"],
    },
  },
  {
    name: "run_command",
    description: "Run an allowlisted command in the workspace. No shell interpolation.",
    inputSchema: {
      type: "object",
      properties: {
        command: { type: "string", description: "Allowlisted binary, e.g. go, npm, tsc, git" },
        args: { type: "array", items: { type: "string" } },
      },
      required: ["command"],
    },
  },
  {
    name: "git_diff",
    description: "Show the current uncommitted diff.",
    inputSchema: { type: "object", properties: {} },
  },
  {
    name: "git_status",
    description: "Show git status --porcelain.",
    inputSchema: { type: "object", properties: {} },
  },
];