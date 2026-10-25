import "dotenv/config";
import express from "express";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { providerFromEnv } from "./provider-factory.js";
import { runAgent, type AgentState } from "./agent.js";
import type { ModelMessage } from "./types.js";
import process from "node:process";

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

const app = express();
app.use(express.json({ limit: "25mb" }));

interface ActiveRun {
  runId: string;
  cancel: () => void;
}
const active = new Map<string, ActiveRun>();

interface StartBody {
  runId: string;
  containerName: string;
  worktreePath: string;
  task: string;
  systemPrompt?: string;
  callbackUrl: string;
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
}

app.post("/runs", async (req, res) => {
  const body = req.body as StartBody;
  if (!body.runId || !body.containerName || !body.worktreePath || !body.task || !body.callbackUrl) {
    return res.status(400).json({ error: "runId, containerName, worktreePath, task, callbackUrl are required" });
  }
  if (active.has(body.runId)) {
    return res.status(409).json({ error: "run already active" });
  }

  // Build a workspace proxy that just execs into the pre-provisioned container.
  const ws = {
    id: body.runId,
    containerName: body.containerName,
    worktreePath: body.worktreePath,
    exec: (argv: string[]) => execInContainer(body.containerName, argv),
    destroy: async () => { /* lifecycle owned by control plane */ },
  };

  const provider = providerFromEnv();
  let cancelled = false;
  const cancelFn = () => { cancelled = true; };
  active.set(body.runId, { runId: body.runId, cancel: cancelFn });

  res.status(202).json({ accepted: true });

  // Fire and forget — the loop posts callbacks to the control plane.
  void (async () => {
    try {
      await runAgent(
        {
          provider,
          model: body.model ?? process.env.MODEL_NAME ?? "",
          workspace: ws as any,
          maxSteps: body.maxSteps ?? 20,
          maxRetries: body.maxRetries ?? 2,
          wallClockMs: body.wallClockMs ?? 1_800_000,
          systemPrompt: body.systemPrompt ?? IMPLEMENTER_SYSTEM,
          resumeFrom: body.checkpoint
            ? {
                messages: body.checkpoint.messages,
                stepCount: body.checkpoint.stepCount,
                usage: body.checkpoint.usage,
              }
            : undefined,
          isCancelled: () => cancelled,
          onCheckpoint: async (state) => {
            const diff = await getWorkspaceDiff(body.containerName);
            await postCallback(body.callbackUrl + `/runs/${body.runId}/checkpoint`, {
              messages: state.messages,
              stepCount: state.stepCount,
              usage: state.usage,
              status: state.status,
              workspaceDiff: diff,
            });
          },
        },
        body.task,
      ).then(async (result) => {
        const diff = await getWorkspaceDiff(body.containerName);
        await postCallback(body.callbackUrl + `/runs/${body.runId}/complete`, {
          status: result.state.status,
          finalMessage: result.finalMessage,
          stepCount: result.state.stepCount,
          usage: result.state.usage,
          messages: result.state.messages,
          workspaceDiff: diff,
        });
      });
    } catch (e) {
      await postCallback(body.callbackUrl + `/runs/${body.runId}/complete`, {
        status: "error",
        finalMessage: String(e),
        stepCount: 0,
        usage: [],
        messages: [],
        workspaceDiff: "",
      }).catch(() => {});
    } finally {
      active.delete(body.runId);
    }
  })();
});

app.post("/runs/:id/cancel", (req, res) => {
  const a = active.get(req.params.id);
  if (a) { a.cancel(); return res.json({ ok: true }); }
  res.status(404).json({ ok: false, error: "not running" });
});

app.get("/health", (_req, res) => res.json({ ok: true }));

// -- helpers --

async function execInContainer(containerName: string, argv: string[]) {
  const { spawn } = await import("node:child_process");
  return new Promise<{ stdout: string; stderr: string; exitCode: number }>((resolve, reject) => {
    const p = spawn("docker", ["exec", containerName, ...argv]);
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

async function postCallback(url: string, body: unknown) {
  const res = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`callback ${url}: HTTP ${res.status}`);
}

const port = Number(process.env.ADAPTER_PORT ?? 8081);
app.listen(port, () => console.log(`adapter listening on :${port}`));