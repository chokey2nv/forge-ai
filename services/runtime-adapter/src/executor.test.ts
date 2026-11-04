import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";
import { executeToolCall } from "./executor";
import type { Workspace } from "./workspace";

// search against a real git repository: the tool's arguments are git's to judge.
function repo(): Workspace {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "forge-search-"));
  const git = (...args: string[]) => execFileSync("git", ["-C", dir, ...args]);
  git("init", "-q");
  fs.mkdirSync(path.join(dir, "prototype"));
  fs.writeFileSync(path.join(dir, "prototype", "qc.html"), "<h2>Westgard rules</h2>\n-flag a.b\n");
  fs.writeFileSync(path.join(dir, "notes.txt"), "Westgard elsewhere\naxb\n");
  git("add", "-A");
  return {
    exec: async (argv: string[]) => {
      const r = spawnSync(argv[0], argv.slice(1), { cwd: dir, encoding: "utf8" });
      return { stdout: r.stdout, stderr: r.stderr, exitCode: r.status ?? 1 };
    },
  } as unknown as Workspace;
}

const search = (ws: Workspace, args: Record<string, unknown>) =>
  executeToolCall(ws, { id: "1", name: "search", arguments: args });

test("search finds plain text across the repository", async () => {
  const r = await search(repo(), { query: "Westgard" });
  assert.ok(r.ok);
  assert.match(r.output, /prototype\/qc\.html:1:/);
  assert.match(r.output, /notes\.txt:1:/);
});

test("search limits itself to a glob", async () => {
  const r = await search(repo(), { query: "Westgard", glob: "prototype/*.html" });
  assert.ok(r.ok);
  assert.match(r.output, /qc\.html/);
  assert.doesNotMatch(r.output, /notes\.txt/);
});

test("search takes queries that look like markup, options or patterns literally", async () => {
  const ws = repo();
  assert.match((await search(ws, { query: "<h2" })).output, /qc\.html:1:/);
  assert.match((await search(ws, { query: "-flag" })).output, /qc\.html:2:/);
  const dot = await search(ws, { query: "a.b" });
  assert.match(dot.output, /qc\.html:2:/);
  assert.doesNotMatch(dot.output, /axb/);
});

test("search with no matches is not an error", async () => {
  const r = await search(repo(), { query: "nothing like this" });
  assert.ok(r.ok);
  assert.equal(r.output, "(no matches)");
});
