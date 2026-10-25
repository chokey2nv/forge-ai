import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";

export interface WorkspaceConfig {
  image: string;
  worktreePath: string; // host path to the git worktree
  containerName?: string;
}

export interface ExecResult {
  stdout: string;
  stderr: string;
  exitCode: number;
}

export interface Workspace {
  id: string;
  containerName: string;
  worktreePath: string;
  exec(argv: string[]): Promise<ExecResult>;
  destroy(): Promise<void>;
}

const WORKTREE_MOUNT = "/workspace";

export async function provisionWorkspace(cfg: WorkspaceConfig): Promise<Workspace> {
  const id = randomUUID();
  const containerName = cfg.containerName ?? `forge-worker-${id.slice(0, 8)}`;

  await runHost("docker", [
    "run", "-d", "--rm",
    "--name", containerName,
    "--network=none",
    "--workdir", WORKTREE_MOUNT,
    "-v", `${cfg.worktreePath}:${WORKTREE_MOUNT}:rw`,
    "--memory", "2g",
    "--cpus", "2",
    "--pids-limit", "256",
    cfg.image,
    "sleep", "infinity",
  ]);

  return {
    id,
    containerName,
    worktreePath: cfg.worktreePath,
    exec: (argv) => runHost("docker", ["exec", containerName, ...argv]),
    async destroy() {
      await runHost("docker", ["rm", "-f", containerName]).catch(() => {});
    },
  };
}

export function runHost(cmd: string, args: string[], opts: { inherit?: boolean } = {}): Promise<ExecResult> {
  return new Promise((resolve, reject) => {
    const p = spawn(cmd, args, {
      stdio: opts.inherit ? "inherit" : ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    p.stdout?.on("data", (d) => (stdout += d.toString()));
    p.stderr?.on("data", (d) => (stderr += d.toString()));
    p.on("error", reject);
    p.on("close", (code) => resolve({ stdout, stderr, exitCode: code ?? -1 }));
  });
}