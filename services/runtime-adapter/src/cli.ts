import "dotenv/config";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import process from "node:process";
import { providerFromEnv } from "./provider-factory";
import { provisionWorkspace, runHost } from "./workspace";
import { runAgent } from "./agent";
import { FileCheckpointer } from "./checkpoint";
import { TOOLS } from "./tools";
import { AgentRole } from "./roles";

const IMPLEMENTER_SYSTEM = `You are an autonomous implementer agent working inside a sandboxed workspace.

Tools: read_file, list_dir, search, write_file, apply_patch, run_command, git_diff, git_status.

Rules:
- Read before you write. Inspect the relevant files and tests before changing anything.
- Make the smallest change that satisfies the task.
- Prefer apply_patch over write_file for modifications to existing files.
- Run the project's tests with run_command before declaring done.
- Do not modify files outside the scope of the task.
- Do not touch .git directly.
- When finished, reply with a short summary of what changed and which tests passed.
- If blocked, reply with a clear explanation of what is preventing progress.`;



async function main() {
  const argv = process.argv.slice(2);
  if (argv[0] !== "run") {
    console.error("usage: forge run --repo <path> --task <prompt>");
    process.exit(2);
  }
  const repo = flag(argv, "--repo");
  const task = flag(argv, "--task");
  if (!repo || !task) {
    console.error("usage: forge run --repo <path> --task <prompt>");
    process.exit(2);
  }

  const model = process.env.MODEL_NAME ?? "";
  if (!model) {
    console.error("MODEL_NAME is not set in .env");
    process.exit(2);
  }

  const worktreeDir = await mkdtemp(join(tmpdir(), "forge-wt-"));
  const add = await runHost("git", ["-C", repo, "worktree", "add", "--detach", worktreeDir]);
  if (add.exitCode !== 0) {
    console.error("failed to create worktree:", add.stderr);
    process.exit(1);
  }

  const ws = await provisionWorkspace({
    image: process.env.WORKER_IMAGE ?? "forge-worker:dev",
    worktreePath: worktreeDir,
  });
  console.log(`workspace ${ws.containerName} at ${worktreeDir}\n`);

    const provider = providerFromEnv();
    console.log(`provider: ${provider.id}  model: ${process.env.MODEL_NAME}`);

//   const provider = new OllamaProvider({
//     baseUrl: process.env.MODEL_BASE_URL ?? "http://127.0.0.1:11434",
//     defaultModel: model,
//     timeoutMs: Number(process.env.MODEL_TIMEOUT_MS ?? 120_000),
//   });

  const runId = `run-${new Date().toISOString().replace(/[:.]/g, "-")}`;
  const checkpointer = new FileCheckpointer(join(process.cwd(), ".forge", "checkpoints"), runId);

  try {
    // Role can be overridden for one-off tests of other agents.
    const role = (process.env.FORGE_ROLE ?? "implementer") as AgentRole;

    const result = await runAgent(
      {
        provider,
        model,
        workspace: ws,
        role,
        tools: TOOLS,
        systemPrompt: IMPLEMENTER_SYSTEM,
        maxSteps: Number(process.env.MAX_AGENT_STEPS ?? 20),
        maxRetries: Number(process.env.MAX_RETRIES ?? 2),
        wallClockMs: Number(process.env.TASK_TIMEOUT_SECONDS ?? 1800) * 1000,
        onCheckpoint: (s) => checkpointer.save(s),
      },
      task,
    );
    // const result = await runAgent(
    //   {
    //     provider,
    //     model,
    //     workspace: ws,
    //     maxSteps: Number(process.env.MAX_AGENT_STEPS ?? 20),
    //     maxRetries: Number(process.env.MAX_RETRIES ?? 2),
    //     wallClockMs: Number(process.env.TASK_TIMEOUT_SECONDS ?? 1800) * 1000,
    //     systemPrompt: IMPLEMENTER_SYSTEM,
    //     onCheckpoint: (s) => checkpointer.save(s),
    //   },
    //   task!,
    // );

    console.log(`\nstatus: ${result.state.status}`);
    console.log(`steps:  ${result.state.stepCount}`);
    console.log(`final:  ${result.finalMessage.slice(0, 800)}`);

    const diff = await ws.exec(["git", "diff"]);
    console.log("\n=== diff ===");
    console.log(diff.stdout || "(no changes)");

    const status = await ws.exec(["git", "status", "--porcelain"]);
    console.log("\n=== status ===");
    console.log(status.stdout || "(clean)");
  } finally {
    await ws.destroy();
    await runHost("git", ["-C", repo, "worktree", "remove", "--force", worktreeDir]).catch(() => {});
    await rm(worktreeDir, { recursive: true, force: true });
  }
}

function flag(args: string[], name: string): string | undefined {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});