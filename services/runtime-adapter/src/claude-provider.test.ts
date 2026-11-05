import assert from "node:assert/strict";
import { afterEach, test } from "node:test";
import { ClaudeProvider } from "./claude-provider";
import { ModelRefusedError, RateLimitError } from "./types";

const realFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = realFetch; });

function respondWith(res: Response) {
  const bodies: Array<Record<string, any>> = [];
  globalThis.fetch = (async (_url: string, init: RequestInit) => {
    bodies.push(JSON.parse(String(init.body)));
    return res;
  }) as typeof fetch;
  return bodies;
}

const ok = () => new Response(JSON.stringify({
  content: [{ type: "text", text: "hi" }], stop_reason: "end_turn",
  usage: { input_tokens: 5, output_tokens: 2, cache_read_input_tokens: 900, cache_creation_input_tokens: 40 },
}));
const request = { messages: [{ role: "system" as const, content: "rules" }, { role: "user" as const, content: "go" }] };
const provider = (promptCache?: boolean) => new ClaudeProvider({ apiKey: "k", defaultModel: "m", promptCache });

test("the system prompt and the conversation are cached", async () => {
  const bodies = respondWith(ok());
  await provider().complete(request);
  assert.deepEqual(bodies[0].system, [{ type: "text", text: "rules", cache_control: { type: "ephemeral" } }]);
  assert.deepEqual(bodies[0].cache_control, { type: "ephemeral" });
});

test("caching can be turned off", async () => {
  const bodies = respondWith(ok());
  await provider(false).complete(request);
  assert.equal(bodies[0].system, "rules");
  assert.equal(bodies[0].cache_control, undefined);
});

test("cached tokens are reported apart", async () => {
  respondWith(ok());
  const { usage } = await provider().complete(request);
  assert.equal(usage.inputTokens, 5);
  assert.equal(usage.cacheReadTokens, 900);
  assert.equal(usage.cacheWriteTokens, 40);
});

test("a 429 says how long to wait", async () => {
  respondWith(new Response("{}", { status: 429, headers: { "retry-after": "12" } }));
  await assert.rejects(provider().complete(request), (e: unknown) => e instanceof RateLimitError && e.retryAfterMs === 12_000);
});

test("a 429 without retry-after is still a rate limit, and a 500 is not", async () => {
  respondWith(new Response("{}", { status: 429 }));
  await assert.rejects(provider().complete(request), (e: unknown) => e instanceof RateLimitError && e.retryAfterMs === undefined);
  respondWith(new Response("{}", { status: 500 }));
  await assert.rejects(provider().complete(request), (e: unknown) => e instanceof Error && !(e instanceof RateLimitError));
});

test("no credit or a bad key is refused, not worth retrying", async () => {
  const lowCredit = JSON.stringify({ error: { message: "Your credit balance is too low to access the API" } });
  for (const res of [new Response(lowCredit, { status: 400 }), new Response("{}", { status: 401 }), new Response("{}", { status: 402 }), new Response("{}", { status: 403 })]) {
    respondWith(res);
    await assert.rejects(provider().complete(request), (e: unknown) => e instanceof ModelRefusedError);
  }
  respondWith(new Response("{}", { status: 400 }));
  await assert.rejects(provider().complete(request), (e: unknown) => e instanceof Error && !(e instanceof ModelRefusedError));
});
