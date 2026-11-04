import assert from "node:assert/strict";
import { test } from "node:test";
import { runAgent, type AgentConfig } from "./agent";
import type { ModelProvider, ModelRequest, ModelResponse } from "./types";
import type { Workspace } from "./workspace";

const reply = (r: Partial<ModelResponse>): ModelResponse => ({
  content: "", toolCalls: [], finishReason: "stop", usage: { wallMs: 0, outputTokens: 10 }, ...r,
});

function agentWith(replies: ModelResponse[]) {
  const limits: Array<number | undefined> = [];
  const executed: string[] = [];
  const provider = {
    id: "fake",
    complete: async (req: ModelRequest) => {
      limits.push(req.maxTokens);
      const next = replies.shift();
      assert.ok(next, "more model calls than expected");
      return next;
    },
  } as unknown as ModelProvider;
  const workspace = {
    exec: async (argv: string[]) => { executed.push(argv.join(" ")); return { stdout: "", stderr: "", exitCode: 0 }; },
  } as unknown as Workspace;
  const cfg: AgentConfig = {
    provider, model: "m", workspace, role: "implementer" as AgentConfig["role"], tools: [],
    maxSteps: 5, maxRetries: 0, wallClockMs: 60_000, systemPrompt: "s",
  };
  return { cfg, limits, executed };
}

test("a cut-off reply is asked for again with a higher limit, and not kept", async () => {
  const { cfg, limits } = agentWith([
    reply({ content: "partial", finishReason: "length" }),
    reply({ content: "whole" }),
  ]);
  const { state, finalMessage } = await runAgent(cfg, "t");
  assert.deepEqual(limits, [16384, 32768]);
  assert.equal(finalMessage, "whole");
  assert.equal(state.status, "complete");
  assert.equal(state.usage.length, 2, "the cut-off call was still spent");
  assert.ok(!state.messages.some((m) => m.content === "partial"));
});

test("a tool call in a cut-off reply is not run", async () => {
  const { cfg, executed } = agentWith([
    reply({ finishReason: "length", toolCalls: [{ id: "1", name: "write_file", arguments: { path: "a.txt", content: "half" } }] }),
    reply({ content: "done" }),
  ]);
  await runAgent(cfg, "t");
  assert.deepEqual(executed, []);
});

test("a reply cut off at every limit blocks, and says so", async () => {
  const { cfg, limits } = agentWith([1, 2].map(() => reply({ finishReason: "length" })));
  const { state, finalMessage } = await runAgent(cfg, "t");
  assert.deepEqual(limits, [16384, 32768]);
  assert.equal(state.status, "blocked");
  assert.match(finalMessage, /cut off even at 32768.*MODEL_MAX_OUTPUT_TOKENS/);
});

test("the higher limit stays for the rest of the run", async () => {
  const { cfg, limits } = agentWith([
    reply({ finishReason: "length" }),
    reply({ finishReason: "tool_calls", toolCalls: [{ id: "1", name: "list_dir", arguments: {} }] }),
    reply({ content: "done" }),
  ]);
  await runAgent(cfg, "t");
  assert.deepEqual(limits, [16384, 32768, 32768]);
});

test("MODEL_MAX_OUTPUT_TOKENS sets the room for a reply", async () => {
  const { cfg, limits } = agentWith([reply({ finishReason: "length" }), reply({ content: "done" })]);
  await runAgent({ ...cfg, maxOutputTokens: 50000 }, "t");
  assert.deepEqual(limits, [50000, 100000]);
});
