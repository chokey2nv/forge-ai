import type { ModelMessage, ModelProvider, ModelRequest, ModelUsage } from "./types";
import { TOOLS, toolsForRole } from "./tools";
import { executeToolCall } from "./executor";
import type { Workspace } from "./workspace";

import { type AgentRole } from "./roles";


export interface AgentResult {
  state: AgentState;
  finalMessage: string;
  submission?: Record<string, unknown>;   // populated when submit_* tool is called
}

export interface AgentConfig {
  provider: ModelProvider;
  model: string;
  workspace: Workspace;
  role: AgentRole;  
  maxSteps: number;
  maxRetries: number;
  wallClockMs: number;
  systemPrompt?: string;
  onCheckpoint?: (state: AgentState) => Promise<void>;
  resumeFrom?: {
    messages: ModelMessage[];
    stepCount: number;
    usage: ModelUsage[];
  };
  isCancelled?: () => boolean;
}

export interface AgentState {
  messages: ModelMessage[];
  stepCount: number;
  usage: ModelUsage[];
  startedAt: string;
  finishedAt?: string;
  status: "running" | "complete" | "blocked" | "error";
  blockedReason?: string;

}

export interface AgentResult {
  state: AgentState;
  finalMessage: string;
  submission?: Record<string, unknown>;   // populated when submit_* tool is called
}

export async function runAgent(cfg: AgentConfig, taskPrompt: string): Promise<AgentResult> {
  const state: AgentState = {
    messages: [
      { role: "system", content: cfg.systemPrompt ?? "" },
      { role: "user", content: taskPrompt },
    ],
    stepCount: 0,
    usage: [],
    startedAt: new Date().toISOString(),
    status: "running",
  };

  const deadline = Date.now() + cfg.wallClockMs;

  while (state.stepCount < cfg.maxSteps) {
    if (Date.now() > deadline) {
      state.status = "blocked";
      state.blockedReason = `wall clock exceeded (${cfg.wallClockMs}ms)`;
      break;
    }

    const resp = await callWithRetry(cfg, state.messages);
    if (!resp) {
      state.status = "error";
      state.blockedReason = `model call failed after ${cfg.maxRetries} retries`;
      break;
    }


    const tools = toolsForRole(cfg.role);
    const submissionToolName = cfg.role === "planner" ? "submit_plan"
                              : cfg.role === "reviewer" ? "submit_review"
                              : "";

    // Inside the while loop, replace the tool-execution block:
    let submission: Record<string, unknown> | undefined;

    for (const call of resp.toolCalls) {
      if (submissionToolName && call.name === submissionToolName) {
        submission = call.arguments;
        state.messages.push({
          role: "tool", toolCallId: call.id, toolName: call.name,
          content: JSON.stringify({ accepted: true }),
        });
        break;   // stop processing further tool calls
      }
      const result = await executeToolCall(cfg.workspace, call);
      state.messages.push({
        role: "tool",
        content: truncate(result.output, 8_000),
        toolCallId: call.id,
        toolName: call.name,
      });
    }

    if (submission) {
      state.status = "complete";
      await cfg.onCheckpoint?.(state);
      state.finishedAt = new Date().toISOString();
      return { state, finalMessage: "submission accepted", submission };
    }

    // If no tool calls at all, agent is done
    if (resp.toolCalls.length === 0) {
      state.status = "complete";
      await cfg.onCheckpoint?.(state);
      state.finishedAt = new Date().toISOString();
      return { state, finalMessage: resp.content };
    }





    state.stepCount++;
    state.usage.push(resp.usage);
    state.messages.push({ role: "assistant", content: resp.content });

    if (resp.toolCalls.length === 0) {
      state.status = "complete";
      await cfg.onCheckpoint?.(state);
      state.finishedAt = new Date().toISOString();
      return { state, finalMessage: resp.content };
    }

    for (const call of resp.toolCalls) {
      const result = await executeToolCall(cfg.workspace, call);
      state.messages.push({
        role: "tool",
        content: truncate(result.output, 8_000),
        toolCallId: call.id,
        toolName: call.name,
      });
    }

    await cfg.onCheckpoint?.(state);
  }

  if (state.status === "running") {
    state.status = "blocked";
    state.blockedReason ??= `max steps (${cfg.maxSteps}) exhausted`;
  }
  state.finishedAt = new Date().toISOString();
  return { state, finalMessage: state.blockedReason ?? "done" };
}

async function callWithRetry(cfg: AgentConfig, messages: ModelMessage[]) {
  let attempt = 0;
  while (attempt <= cfg.maxRetries) {
    try {
      const req: ModelRequest = {
        model: cfg.model,
        messages,
        tools: TOOLS,
        temperature: 0.1,
        maxTokens: 4096,
      };
      return await cfg.provider.complete(req);
    } catch (e) {
      attempt++;
      if (attempt > cfg.maxRetries) return null;
      await new Promise((r) => setTimeout(r, 1000 * attempt));
    }
  }
  return null;
}

function truncate(s: string, n: number): string {
  if (s.length <= n) return s;
  return s.slice(0, n) + `\n...[truncated ${s.length - n} chars]`;
}