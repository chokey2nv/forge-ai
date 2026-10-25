import "dotenv/config";
import { mkdtemp, writeFile, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { providerFromEnv } from "../src/provider-factory";
import type { ModelRequest, ModelTool } from "../src/types";
import process from "node:process";
import { readFileSync } from "node:fs";

const BASE = process.env.MODEL_BASE_URL ?? "http://127.0.0.1:11434";
const MODEL = process.env.MODEL_NAME ?? "";
const TIMEOUT = Number(process.env.MODEL_TIMEOUT_MS ?? 120_000);

if (!MODEL) {
  console.error("MODEL_NAME is not set in .env");
  process.exit(2);
}

let provider: ReturnType<typeof providerFromEnv> | undefined = providerFromEnv();

if (!provider) {
  console.error("provider init failed");
  process.exit(2);
    
}

console.log(`smoke-testing provider=${provider?.id} model=${MODEL}\n`);

// const provider = providerFromEnv();
console.log(`provider: ${provider?.id}  model: ${process.env.MODEL_NAME}`);
// const provider = new OllamaProvider({ baseUrl: BASE, defaultModel: MODEL, timeoutMs: TIMEOUT });

type Check = { name: string; pass: boolean; detail: string };
const checks: Check[] = [];
function record(name: string, pass: boolean, detail = "") {
  checks.push({ name, pass, detail });
  console.log(`${pass ? "PASS" : "FAIL"}  ${name}${detail ? ` — ${detail}` : ""}`);
}

// ---- fixtures: a temp workspace with two files ----
const dir = await mkdtemp(join(tmpdir(), "forge-smoke-"));
await writeFile(join(dir, "greet.ts"), `export function greet(name: string) {\n  return \`hello \${name}\`;\n}\n`);
await writeFile(join(dir, "README.md"), `# fixture\nUses greet.ts.\n`);

const readFileTool: ModelTool = {
  name: "read_file",
  description: "Read a file from the workspace. Argument: path (string).",
  inputSchema: {
    type: "object",
    properties: { path: { type: "string" } },
    required: ["path"],
  },
};

function executeTool(name: string, args: Record<string, unknown>): string {
  if (name !== "read_file") return `unknown tool ${name}`;
  const p = String(args.path ?? "");
  if (p.includes("..")) return "path escapes workspace";
  try { return readFileSync(join(dir, p), "utf8"); }
  catch { return `ENOENT ${p}`; }
}

// ---- 1. health ----
try {
  const h = await provider.healthCheck();
  record("healthCheck", h.ok, h.detail);
} catch (e) { record("healthCheck", false, String(e)); }

// ---- 2. listModels ----
try {
  const models = await provider.listModels();
  record("listModels contains MODEL_NAME", models.includes(MODEL), `${models.length} models`);
} catch (e) { record("listModels", false, String(e)); }

// ---- 3. single-turn code gen ----
try {
  const r = await provider.complete({
    messages: [
      { role: "system", content: "You are a concise code generator. Reply with code only." },
      { role: "user", content: "Write a TypeScript function `add(a: number, b: number): number`." },
    ],
    maxTokens: 200,
  });
  const ok = /function\s+add\s*\(\s*a:\s*number\s*,\s*b:\s*number\s*\)\s*:\s*number/.test(r.content);
  record("single-turn code gen", ok, `wall ${r.usage.wallMs}ms`);
} catch (e) { record("single-turn code gen", false, String(e)); }

// ---- 4. single tool call ----
try {
  const r = await provider.complete({
    messages: [
      { role: "system", content: "Use the read_file tool when you need file contents." },
      { role: "user", content: "Read greet.ts and show me its contents." },
    ],
    tools: [readFileTool],
  });
  const valid = r.toolCalls.length > 0
    && r.toolCalls[0].name === "read_file"
    && typeof r.toolCalls[0].arguments.path === "string";
  record("single tool call valid", valid,
    r.toolCalls[0] ? `${r.toolCalls[0].name}(${JSON.stringify(r.toolCalls[0].arguments)})` : "no tool call");
} catch (e) { record("single tool call valid", false, String(e)); }

// ---- 5. multi-step tool use ----
try {
  const messages: ModelRequest["messages"] = [
    { role: "system", content: "Use read_file then explain. Do not guess file contents." },
    { role: "user", content: "What does greet.ts do? Read it first." },
  ];
  const r1 = await provider.complete({ messages, tools: [readFileTool] });
  if (r1.toolCalls.length === 0) {
    record("multi-step tool use", false, "no first tool call");
  } else {
    const tc = r1.toolCalls[0];
    const result = executeTool(tc.name, tc.arguments);
    messages.push({ role: "assistant", content: r1.content });
    messages.push({ role: "tool", toolCallId: tc.id, toolName: tc.name, content: result });
    const r2 = await provider.complete({ messages, tools: [readFileTool] });
    const grounded = /hello|greet|name/i.test(r2.content);
    record("multi-step tool use", grounded, `final wall ${r2.usage.wallMs}ms`);
  }
} catch (e) { record("multi-step tool use", false, String(e)); }

// ---- 6. structured output ----
try {
  const r = await provider.complete({
    messages: [{ role: "user", content: "Return JSON with keys name (\"forge\") and version (1)." }],
    responseSchema: {
      type: "object",
      properties: { name: { type: "string" }, version: { type: "number" } },
      required: ["name", "version"],
    },
    maxTokens: 100,
  });
  let ok = false; let detail = "";
  try {
    const parsed = JSON.parse(r.content);
    ok = parsed.name === "forge" && parsed.version === 1;
    detail = JSON.stringify(parsed);
  } catch { detail = "invalid JSON"; }
  record("structured output valid", ok, detail);
} catch (e) { record("structured output valid", false, String(e)); }

// ---- 7. tool-call validity over a few tasks ----
try {
  const prompts = [
    "Read README.md.",
    "Read greet.ts to see the exported function.",
    "Open greet.ts and quote its first line.",
    "Inspect README.md contents.",
    "Fetch greet.ts.",
  ];
  let parsed = 0;
  for (const p of prompts) {
    try {
      const r = await provider.complete({
        messages: [
          { role: "system", content: "Always call read_file when a file is named." },
          { role: "user", content: p },
        ],
        tools: [readFileTool],
        maxTokens: 150,
      });
      if (r.toolCalls.length > 0 && typeof r.toolCalls[0].arguments.path === "string") parsed++;
    } catch { /* count as failure */ }
  }
  const ratio = parsed / prompts.length;
  record("tool-call validity ≥ 90%", ratio >= 0.9, `${parsed}/${prompts.length}`);
} catch (e) { record("tool-call validity", false, String(e)); }

await rm(dir, { recursive: true, force: true });

// ---- summary ----
const passed = checks.filter((c) => c.pass).length;
const total = checks.length;
console.log(`\n${passed}/${total} checks passed on model: ${MODEL}`);
process.exit(passed === total ? 0 : 1);