import { ModelRefusedError, RateLimitError, type ModelMessage, type ModelProvider, type ModelRequest, type ModelResponse, type ModelTool, type ModelUsage } from "./types";
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
  tools: ModelTool[];
  maxSteps: number;
  maxRetries: number;
  /** How long one model call may wait out rate limits before the run blocks. */
  rateLimitMaxWaitMs?: number;
  wallClockMs: number;
  systemPrompt: string;
  /** Room for one model reply (MODEL_MAX_OUTPUT_TOKENS). */
  maxOutputTokens?: number;
  onCheckpoint?: (state: AgentState) => Promise<void>;
  resumeFrom?: {
    messages: ModelMessage[];
    stepCount: number;
    usage: ModelUsage[];
  };
  isCancelled?: () => boolean;
  /** The control plane's reason to stop, e.g. a spend cap reached (Phase 13.6). */
  stopReason?: () => string | undefined;
  submissionTool?: string;
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


// A long report or a whole file in a tool call needs room; only what the model
// writes is billed. A reply that's cut off anyway gets one more try with double.
const DEFAULT_MAX_OUTPUT_TOKENS = 16384;

// Rate limits are per minute, so a retry within seconds fails too. Waiting
// doesn't use up maxRetries, which is for calls that actually failed.
const RATE_LIMIT_MAX_WAIT_MS = 5 * 60_000;
const RATE_LIMIT_DEFAULT_WAIT_MS = 30_000;
const RATE_LIMIT_LONGEST_WAIT_MS = 60_000;

export async function runAgent(cfg: AgentConfig, taskPrompt: string): Promise<AgentResult> {
  const messages: ModelMessage[] = cfg.resumeFrom && cfg.resumeFrom.messages.length > 0
    ? cfg.resumeFrom.messages
    : [
        { role: "system", content: cfg.systemPrompt ?? "" },
        { role: "user", content: taskPrompt },
      ];

  const state: AgentState = {
    messages,
    stepCount: cfg.resumeFrom?.stepCount ?? 0,
    usage: cfg.resumeFrom?.usage ?? [],
    startedAt: new Date().toISOString(),
    status: "running",
  };

  const deadline = Date.now() + cfg.wallClockMs;
  const firstLimit = cfg.maxOutputTokens ?? DEFAULT_MAX_OUTPUT_TOKENS;
  let limit = firstLimit;

  while (state.stepCount < cfg.maxSteps) {
    if (cfg.isCancelled?.()) {
      state.status = "blocked";
      state.blockedReason = "cancelled";
      break;
    }
    const stop = cfg.stopReason?.();
    if (stop) {
      state.status = "blocked";
      state.blockedReason = stop;
      break;
    }
    if (Date.now() > deadline) {
      state.status = "blocked";
      state.blockedReason = `wall clock exceeded (${cfg.wallClockMs}ms)`;
      break;
    }

    const resp = await callWithRetry(cfg, state.messages, limit);
    if (typeof resp === "string") {
      state.status = "error";
      state.blockedReason = resp;
      break;
    }

    // Every call is spent, the last one (a submission or final answer) too.
    state.usage.push(resp.usage);

    // Cut off: any text is incomplete and a tool call's arguments may be too,
    // so none of it goes in the history or gets run.
    if (resp.finishReason === "length") {
      console.log(`[adapter] reply cut off at ${limit} output tokens`);
      await cfg.onCheckpoint?.(state); // reports the spend, and any stop that answers it
      if (limit === firstLimit) {
        limit = firstLimit * 2;
        continue;
      }
      state.status = "blocked";
      state.blockedReason = `the model's reply was cut off even at ${limit} output tokens; raise MODEL_MAX_OUTPUT_TOKENS or split the work into smaller pieces`;
      break;
    }

    let submission: Record<string, unknown> | undefined;

    // Persist the assistant turn (including tool_calls) before branching —
    // both the submission and tool-execution paths below assume it is in history.
    state.messages.push({ role: "assistant", content: resp.content, toolCalls: resp.toolCalls });

    const submissionName = cfg.submissionTool;
    const submissionCall = submissionName
      ? resp.toolCalls.find((c) => c.name === submissionName)
      : undefined;

    if (submissionCall) {
      submission = submissionCall.arguments;
      state.messages.push({
        role: "tool", toolCallId: submissionCall.id, toolName: submissionCall.name,
        content: JSON.stringify({ accepted: true }),
      });
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

/** The model's reply, or why there is none. */
async function callWithRetry(cfg: AgentConfig, messages: ModelMessage[], maxTokens: number): Promise<ModelResponse | string> {
  const failed = `model call failed after ${cfg.maxRetries} retries`;
  const maxWait = cfg.rateLimitMaxWaitMs ?? RATE_LIMIT_MAX_WAIT_MS;
  let waited = 0;
  let attempt = 0;
  while (attempt <= cfg.maxRetries) {
    try {
      const req: ModelRequest = {
        model: cfg.model,
        messages,
        tools: cfg.tools,
        temperature: 0.1,
        maxTokens,
      };
      return await cfg.provider.complete(req);
    } catch (e) {
      if (e instanceof ModelRefusedError) {
        console.error("[adapter] the model provider refused the call:", e.message);
        return `the model provider refused the call, so no agent can run until that's fixed (check the API key and credit): ${e.message}`;
      }
      if (e instanceof RateLimitError) {
        if (waited >= maxWait) {
          return `still rate limited by the model provider after waiting ${Math.round(waited / 1000)}s; resume it once other work has finished`;
        }
        const wait = Math.min(e.retryAfterMs ?? RATE_LIMIT_DEFAULT_WAIT_MS, RATE_LIMIT_LONGEST_WAIT_MS);
        console.error(`[adapter] rate limited; waiting ${Math.round(wait / 1000)}s:`, e.message);
        waited += wait;
        await new Promise((r) => setTimeout(r, wait));
        continue;
      }
      attempt++;
      console.error(`[adapter] model call failed (attempt ${attempt}/${cfg.maxRetries + 1}):`, e instanceof Error ? e.message : e);
      if (attempt > cfg.maxRetries) return failed;
      await new Promise((r) => setTimeout(r, 1000 * attempt));
    }
  }
  return failed;
}

function truncate(s: string, n: number): string {
  if (s.length <= n) return s;
  return s.slice(0, n) + `\n...[truncated ${s.length - n} chars]`;
}