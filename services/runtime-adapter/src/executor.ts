import { Buffer } from "node:buffer";
import type { ModelToolCall } from "./types.js";
import type { Workspace } from "./workspace.js";

const ALLOWED_COMMANDS = new Set([
  "gofmt", "go", "npm", "pnpm", "npx", "tsc", "node", "git",
]);

const MAX_OUTPUT_BYTES = 20_000;

function confine(p: string): string {
  if (!p || p.startsWith("/") || p.split("/").includes("..")) {
    throw new Error(`path escapes workspace: ${p}`);
  }
  return p;
}

function shellQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`;
}

export interface ToolResult {
  ok: boolean;
  output: string;
  metadata?: Record<string, unknown>;
}

export async function executeToolCall(
  ws: Workspace,
  call: ModelToolCall,
): Promise<ToolResult> {
  try {
    switch (call.name) {
      case "read_file": {
        const p = confine(String(call.arguments.path ?? ""));
        const r = await ws.exec(["cat", p]);
        if (r.exitCode !== 0) return { ok: false, output: r.stderr || `read failed: ${p}` };
        const start = call.arguments.startLine as number | undefined;
        const end = call.arguments.endLine as number | undefined;
        if (start || end) {
          const lines = r.stdout.split("\n");
          const from = Math.max(0, (start ?? 1) - 1);
          const to = end ?? lines.length;
          return { ok: true, output: lines.slice(from, to).join("\n") };
        }
        return { ok: true, output: r.stdout };
      }

      case "list_dir": {
        const p = confine(String(call.arguments.path ?? "."));
        const depth = Math.min(Number(call.arguments.depth ?? 2), 5);
        const r = await ws.exec(["find", p, "-maxdepth", String(depth), "-type", "f"]);
        return { ok: r.exitCode === 0, output: r.stdout || r.stderr };
      }

      case "search": {
        const query = String(call.arguments.query ?? "");
        if (!query) return { ok: false, output: "query is required" };
        const glob = call.arguments.glob as string | undefined;
        const args = ["git", "grep", "-n", "-I", "--"];
        if (glob) args.push(glob);
        args.push(query);
        const r = await ws.exec(args);
        // git grep: 0 = matches, 1 = no matches (not an error), >1 = error
        if (r.exitCode > 1) return { ok: false, output: r.stderr };
        return { ok: true, output: r.stdout || "(no matches)" };
      }

      case "write_file": {
        const p = confine(String(call.arguments.path ?? ""));
        const content = String(call.arguments.content ?? "");
        const b64 = Buffer.from(content, "utf8").toString("base64");
        const r = await ws.exec([
          "sh", "-c",
          `printf %s ${shellQuote(b64)} | base64 -d > ${shellQuote(p)}`,
        ]);
        if (r.exitCode !== 0) return { ok: false, output: r.stderr || `write failed: ${p}` };
        const d = await ws.exec(["git", "diff", "--", p]);
        return { ok: true, output: d.stdout || `wrote ${p}` };
      }

      case "apply_patch": {
        const diff = String(call.arguments.diff ?? "");
        if (!diff) return { ok: false, output: "diff is required" };
        const b64 = Buffer.from(diff, "utf8").toString("base64");
        const r = await ws.exec([
          "sh", "-c",
          `printf %s ${shellQuote(b64)} | base64 -d | git apply --whitespace=nowarn`,
        ]);
        return {
          ok: r.exitCode === 0,
          output: r.exitCode === 0 ? "patch applied" : r.stderr,
        };
      }

      case "run_command": {
        const cmd = String(call.arguments.command ?? "");
        if (!ALLOWED_COMMANDS.has(cmd)) {
          return { ok: false, output: `command not allowed: ${cmd}. Allowed: ${[...ALLOWED_COMMANDS].join(", ")}` };
        }
        const args = Array.isArray(call.arguments.args)
          ? (call.arguments.args as unknown[]).map(String)
          : [];
        const r = await ws.exec([cmd, ...args]);
        const combined = (r.stdout + r.stderr).slice(0, MAX_OUTPUT_BYTES);
        return { ok: r.exitCode === 0, output: combined, metadata: { exitCode: r.exitCode } };
      }

      case "git_diff": {
        const r = await ws.exec(["git", "diff"]);
        return { ok: true, output: r.stdout || "(no changes)" };
      }

      case "git_status": {
        const r = await ws.exec(["git", "status", "--porcelain"]);
        return { ok: true, output: r.stdout || "(clean)" };
      }

      default:
        return { ok: false, output: `unknown tool: ${call.name}` };
    }
  } catch (e) {
    return { ok: false, output: String(e) };
  }
}