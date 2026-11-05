import "dotenv/config";
import express, { Request, Response } from "express";
import { providerFromEnv } from "./provider-factory";
import { runAgent, type AgentState } from "./agent";
import type { ModelMessage } from "./types";
import process from "process";
import dotenv from "dotenv";
import path from "path";
import fs from "fs";
import { randomUUID, timingSafeEqual } from "crypto";

(function loadRootEnv() {
  let dir = process.cwd();
  for (;;) {
    const candidate = path.join(dir, ".env");
    if (fs.existsSync(candidate)) {
      dotenv.config({ path: candidate });
      console.log(`[adapter] loaded env from ${candidate}`);
      return;
    }
    const parent = path.dirname(dir);
    if (parent === dir) return;
    dir = parent;
  }
})();

const HEARTBEAT_MS = 5_000;

const IMPLEMENTER_SYSTEM = `You are an autonomous implementer agent working inside a sandboxed workspace.

Tools: read_file, list_dir, search, write_file, apply_patch, run_command, git_diff, git_status.

Rules:
- Read before you write. Inspect relevant files and tests before changing anything.
- Make the smallest change that satisfies the task.
- Prefer apply_patch over write_file for modifications.
- Run the project's tests with run_command before declaring done.
- Do not touch .git directly.
- When finished, reply with a short summary.
- If blocked, reply with a clear explanation.`;

// Phase 13.1: the adapter takes runs only from the control plane (shared
// secret) and reports only to it (its configured URL, not one a request names).
const ADAPTER_TOKEN = process.env.FORGE_ADAPTER_TOKEN ?? "";
const CONTROL_PLANE_URL = (process.env.CONTROL_PLANE_URL ?? process.env.CONTROL_PLANE_SELF_URL ?? "").replace(/\/+$/, "");
if (!ADAPTER_TOKEN) {
  console.error("[adapter] FORGE_ADAPTER_TOKEN is not set. Put the same random value the control plane uses in the environment (e.g. in .env).");
  process.exit(2);
}
if (!CONTROL_PLANE_URL) {
  console.error("[adapter] CONTROL_PLANE_URL (or CONTROL_PLANE_SELF_URL) is not set: the adapter reports runs only to that control plane.");
  process.exit(2);
}

/** Constant-time check of the "Authorization: Bearer" secret. */
export function authorized(header: string | undefined, secret: string): boolean {
  const got = Buffer.from((header ?? "").startsWith("Bearer ") ? header!.slice(7).trim() : "");
  const want = Buffer.from(secret);
  return secret.length > 0 && got.length === want.length && timingSafeEqual(got, want);
}

const app = express();
app.use(express.json({ limit: "25mb" }));
app.use((req: Request, res: Response, next: express.NextFunction) => {
  if (req.path === "/health" || authorized(req.headers.authorization, ADAPTER_TOKEN)) return next();
  res.status(401).json({ error: "missing or wrong adapter token" });
});

interface ActiveRun {
  runId: string;
  cancel: () => void;
}
const active = new Map<string, ActiveRun>();

interface ToolSchema {
  name: string;
  description: string;
  inputSchema: Record<string, unknown>;
}

interface StartBody {
  runId: string;
  callbackRunId: string;
  containerName: string;
  worktreePath: string;
  role?: "planner" | "implementer" | "reviewer";   // new, defaults to implementer
  task: string;
  systemPrompt: string;
  tools: ToolSchema[];
  /** Ignored since 13.1: callbacks go to CONTROL_PLANE_URL. */
  callbackUrl?: string;
  /** Presented on this run's callbacks; the control plane issued it. */
  callbackToken?: string;
  provider?: string;
  model?: string;
  maxSteps?: number;
  maxRetries?: number;
  wallClockMs?: number;
  checkpoint?: {
    messages: ModelMessage[];
    stepCount: number;
    usage: any[];
    status: string;
    workspaceDiff: string;
  };
  diagnostics?: string;
  submissionTool?: string;
}

app.post("/runs", async (req: express.Request, res: express.Response) => {
  const body = req.body as StartBody;
  console.log(`[adapter] /runs received runId=${body.runId} role=${body.role ?? "implementer"} container=${body.containerName} taskLen=${(body.task || "").length} tools=${(body.tools || []).length} model=${body.model ?? ""}`);
  if (!body.runId || !body.containerName || !body.worktreePath || !body.task || !body.callbackToken) {
    return res.status(400).json({ error: "runId, containerName, worktreePath, task, callbackToken are required" });
  }
  if (body.callbackUrl && body.callbackUrl.replace(/\/+$/, "") !== CONTROL_PLANE_URL) {
    console.warn(`[adapter] run ${body.runId}: ignoring callbackUrl ${body.callbackUrl}; reporting to ${CONTROL_PLANE_URL}`);
  }
  const callback = (path: string, payload: unknown) => postCallback(CONTROL_PLANE_URL + path, body.callbackToken!, payload);
  if (active.has(body.runId)) {
    return res.status(409).json({ error: "run already active" });
  }

  // Reviewers run in parallel in the implementer's container (Phase 10). They
  // only read: skip workspace-diff capture (it stages and resets the index)
  // and keep their git commands from taking the index lock.
  const isReview = body.submissionTool === "submit_review";
  const execEnv = isReview ? { GIT_OPTIONAL_LOCKS: "0" } : undefined;
  const workspaceDiff = async () => (isReview ? "" : getWorkspaceDiff(body.containerName));

  // Build a workspace proxy that just execs into the pre-provisioned container.
  const ws = {
    id: body.runId,
    containerName: body.containerName,
    worktreePath: body.worktreePath,
    exec: (argv: string[]) => execInContainer(body.containerName, argv, execEnv),
    destroy: async () => { /* lifecycle owned by control plane */ },
  };

  const provider = providerFromEnv();
  let cancelled = false;
  const cancelFn = () => { cancelled = true; };
  active.set(body.runId, { runId: body.runId, cancel: cancelFn });

  res.status(202).json({ accepted: true });

  // Fire and forget — the loop posts callbacks to the control plane.
  let heartbeat: ReturnType<typeof setInterval> | undefined;
  void (async () => {
    const role = body.role ?? "implementer";

    const messages: ModelMessage[] = [];
    if (body.checkpoint) {
      messages.push(...body.checkpoint.messages);
      if (body.diagnostics) {
        messages.push({ role: "user", content: body.diagnostics });
      }
    }

    const resumeFrom = body.checkpoint
      ? { messages, stepCount: body.checkpoint.stepCount, usage: body.checkpoint.usage }
      : undefined;

    // Phase 13.6: this invocation's model usage, reported with every
    // callback. A resumed history's usage was reported by its own invocation.
    const model = body.model || process.env.MODEL_NAME || "";
    const invocation = randomUUID();
    const priorCalls = resumeFrom?.usage?.length ?? 0;
    const spend = (usage: any[]) => ({ invocation, model, usage: usage.slice(priorCalls) });
    let stopReason: string | undefined;

    // Liveness, separate from checkpoints: one step can far outlast
    // STALE_AFTER. It stops with the adapter, and past the run's wall clock,
    // so a truly stuck call is still found.
    const callbackId = body.callbackRunId ?? body.runId;
    const heartbeatUntil = Date.now() + (body.wallClockMs ?? 1_800_000);
    heartbeat = setInterval(() => {
      if (Date.now() > heartbeatUntil) return;
      callback(`/runs/${callbackId}/heartbeat`, {}).catch(() => {});
    }, HEARTBEAT_MS);

    const result = await runAgent({
      provider,
      model,
      workspace: ws as any,
      role,
      tools: body.tools,
      maxSteps: body.maxSteps ?? 20,
      maxOutputTokens: Number(process.env.MODEL_MAX_OUTPUT_TOKENS) || undefined,
      maxRetries: body.maxRetries ?? 2,
      wallClockMs: body.wallClockMs ?? 1_800_000,
      systemPrompt: body.systemPrompt,   // optional override
      submissionTool: body.submissionTool,
      resumeFrom,
      isCancelled: () => cancelled,
      stopReason: () => stopReason,
      onCheckpoint: async (state) => {
        const diff = await workspaceDiff();
        const answer = await callback(`/runs/${callbackId}/checkpoint`, {
          role,
          messages: state.messages,
          stepCount: state.stepCount,
          usage: state.usage,
          status: state.status,
          workspaceDiff: diff,
          spend: spend(state.usage),
        });
        if (answer?.stop) {
          console.log(`[adapter] run ${body.runId}: control plane says stop: ${answer.stop}`);
          stopReason = answer.stop;
        }
      },
    }, body.task);

    const diff = await workspaceDiff();
    await callback(`/runs/${callbackId}/complete`, {
      role,
      status: result.state.status,
      finalMessage: result.finalMessage,
      submission: result.submission,
      stepCount: result.state.stepCount,
      usage: result.state.usage,
      messages: result.state.messages,
      workspaceDiff: diff,
      spend: spend(result.state.usage),
    });
  })().catch((err) => {
    console.error(`[adapter] run ${body.runId} failed during completion:`, err);
  }).finally(() => {
    clearInterval(heartbeat);
    active.delete(body.runId);
  });
});

app.post("/runs/:id/cancel", (req: Request, res: Response) => {
  const a = active.get(req.params.id as string);
  if (a) { a.cancel(); return res.json({ ok: true }); }
  res.status(404).json({ ok: false, error: "not running" });
});

app.get("/health", (_req: Request, res: Response) => res.json({ ok: true }));

// -- helpers --

async function execInContainer(containerName: string, argv: string[], env?: Record<string, string>) {
  const { spawn } = await import("node:child_process");
  const envArgs = Object.entries(env ?? {}).flatMap(([k, v]) => ["-e", `${k}=${v}`]);
  return new Promise<{ stdout: string; stderr: string; exitCode: number }>((resolve, reject) => {
    const p = spawn("docker", ["exec", ...envArgs, containerName, ...argv]);
    let stdout = "", stderr = "";
    p.stdout.on("data", (d) => (stdout += d.toString()));
    p.stderr.on("data", (d) => (stderr += d.toString()));
    p.on("error", reject);
    p.on("close", (code) => resolve({ stdout, stderr, exitCode: code ?? -1 }));
  });
}

async function getWorkspaceDiff(containerName: string): Promise<string> {
  // Capture all changes including untracked: stage everything then diff HEAD, then unstage.
  await execInContainer(containerName, ["sh", "-c", "git add -A"]);
  const r = await execInContainer(containerName, ["git", "diff", "--cached", "HEAD"]);
  await execInContainer(containerName, ["git", "reset"]);
  return r.stdout;
}

async function postCallback(url: string, token: string, body: unknown) {
  const res = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`callback ${url}: HTTP ${res.status}`);
  // A checkpoint may answer {"stop": reason} (Phase 13.6); otherwise 204.
  if (res.status === 200 && res.headers.get("content-type")?.includes("application/json")) {
    return (await res.json()) as { stop?: string };
  }
  return undefined;
}

app.use((err: Error, _req: express.Request, res: express.Response, _next: express.NextFunction) => {
  console.error("[adapter] unhandled error:", err);
  if (!res.headersSent) res.status(500).json({ ok: false, error: String(err) });
});

const port = Number(process.env.ADAPTER_PORT ?? 8081);
// Localhost unless ADAPTER_HOST says otherwise: only the control plane calls it.
const host = process.env.ADAPTER_HOST ?? "127.0.0.1";
app.listen(port, host, () => console.log(`adapter listening on ${host}:${port}, reporting to ${CONTROL_PLANE_URL}`));