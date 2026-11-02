// services/runtime-adapter/src/scripted-provider.ts
//
// Test double: replays canned model turns from a JSON script so pipeline
// failure paths (failed verification, reviewer rejection, crash + resume)
// can be exercised end to end, deterministically, without a real model.
//
// Script format (SCRIPTED_PROVIDER_FILE):
//   {
//     "head-engineer": [ Turn, ... ],
//     "implementer":   [ Turn, ... ],
//     "reviewer":      [ Turn, ... ]
//   }
//   Turn = { "content"?: string, "toolCalls"?: [{ "name", "arguments" }], "delayMs"?: number }
//
// Turns are consumed in order per role across all runs in this process, so a
// retry or a reviewer's second pass simply takes the next turn. The role is
// inferred from the submission tool offered in the request.
//
// When runs of one role overlap (parallel subtasks, parallel reviewers), a
// role may instead map substrings to queues; a call takes the first queue
// whose key appears in its system prompt or task prompt and that still has
// turns, else "*". Reviewers are told apart by their agent prompt:
//   "implementer": { "Add taxFor\n": [...], "Add formatPrice\n": [...] }
// (a subtask's prompt also lists its siblings, so key on its own title line)
//   "reviewer":    { "You review tests": [...], "You are a code reviewer": [...] }
import fs from "node:fs";
import process from "node:process";
import type { ModelCapabilities, ModelProvider, ModelRequest, ModelResponse, ModelToolCall } from "./types";

interface ScriptTurn {
  content?: string;
  toolCalls?: Array<{ name: string; arguments?: Record<string, unknown> }>;
  delayMs?: number;
}

type Queue = ScriptTurn[];
type Script = Record<string, Queue | Record<string, Queue>>;

// Module-level: the adapter builds a provider per run, but a scenario spans runs.
let loadedPath = "";
let queues: Script = {};
let callSeq = 0;

function load(path: string): void {
  if (path === loadedPath) return;
  queues = JSON.parse(fs.readFileSync(path, "utf8")) as Script;
  loadedPath = path;
}

function taskPrompt(req: ModelRequest): string {
  return req.messages.find((m) => m.role === "user")?.content ?? "";
}

function pick(role: string, req: ModelRequest): Queue | undefined {
  const entry = queues[role];
  if (!entry || Array.isArray(entry)) return entry;
  const system = req.messages.find((m) => m.role === "system")?.content ?? "";
  const text = system + "\n" + taskPrompt(req);
  for (const [key, queue] of Object.entries(entry)) {
    if (key !== "*" && text.includes(key) && queue.length > 0) return queue;
  }
  return entry["*"];
}

function roleOf(req: ModelRequest): string {
  const names = new Set((req.tools ?? []).map((t) => t.name));
  if (names.has("submit_task_graph")) return "head-engineer";
  if (names.has("submit_review")) return "reviewer";
  if (names.has("submit_plan")) return "planner";
  return "implementer";
}

export class ScriptedProvider implements ModelProvider {
  readonly id = "scripted";

  constructor(private path: string) {
    if (!path) throw new Error("SCRIPTED_PROVIDER_FILE is required when MODEL_PROVIDER=scripted");
    load(path);
  }

  async healthCheck() {
    return { ok: true, detail: `script ${this.path}` };
  }

  async listModels() {
    return ["scripted"];
  }

  async getCapabilities(_model: string): Promise<ModelCapabilities> {
    return { toolCalling: true, structuredOutput: true, streaming: false, contextWindow: 1_000_000 };
  }

  async complete(req: ModelRequest): Promise<ModelResponse> {
    const role = roleOf(req);
    // SCRIPTED_PROVIDER_LOG: one JSON line per call with the run's task prompt,
    // so tests can assert on what each agent was actually told.
    const log = process.env.SCRIPTED_PROVIDER_LOG;
    if (log) {
      fs.appendFileSync(log, JSON.stringify({ role, task: taskPrompt(req) }) + "\n");
    }
    const turn = pick(role, req)?.shift();
    if (!turn) throw new Error(`scripted provider: no turns left for role "${role}"`);
    console.log(`[scripted] ${role} turn: ${turn.toolCalls?.map((c) => c.name).join(",") || "(text)"}`);
    if (turn.delayMs) await new Promise((r) => setTimeout(r, turn.delayMs));

    const toolCalls: ModelToolCall[] = (turn.toolCalls ?? []).map((c) => ({
      id: `scripted_${++callSeq}`,
      name: c.name,
      arguments: c.arguments ?? {},
    }));
    return {
      content: turn.content ?? "",
      toolCalls,
      finishReason: toolCalls.length > 0 ? "tool_calls" : "stop",
      usage: { wallMs: turn.delayMs ?? 0 },
    };
  }
}
