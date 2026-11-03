import { test } from "node:test";
import assert from "node:assert/strict";
import { parseDiff, filePath } from "./diff.ts";

test("modified file: hunks and line numbers on both sides", () => {
  const [f] = parseDiff([
    "diff --git a/src/price.js b/src/price.js",
    "index 1111111..2222222 100644",
    "--- a/src/price.js",
    "+++ b/src/price.js",
    "@@ -10,4 +10,4 @@ export function applyDiscount(p, pct) {",
    " const a = 1;",
    "-  return p - pct;",
    "+  return Math.round(p * (100 - pct)) / 100;",
    " }",
    "",
  ].join("\n"));
  assert.equal(f.status, "modified");
  assert.equal(filePath(f), "src/price.js");
  assert.equal(f.additions, 1);
  assert.equal(f.deletions, 1);
  assert.deepEqual(f.hunks[0].lines.map((l) => [l.kind, l.oldNo, l.newNo]), [
    ["ctx", 10, 10], ["del", 11, undefined], ["add", undefined, 11], ["ctx", 12, 12],
  ]);
});

test("new, deleted, renamed and binary files", () => {
  const files = parseDiff([
    "diff --git a/tests/new.test.js b/tests/new.test.js",
    "new file mode 100644",
    "index 0000000..3333333",
    "--- /dev/null",
    "+++ b/tests/new.test.js",
    "@@ -0,0 +1,2 @@",
    "+a",
    "+b",
    "diff --git a/old.txt b/old.txt",
    "deleted file mode 100644",
    "index 4444444..0000000",
    "--- a/old.txt",
    "+++ /dev/null",
    "@@ -1 +0,0 @@",
    "-gone",
    "diff --git a/lib/a.js b/lib/b.js",
    "similarity index 90%",
    "rename from lib/a.js",
    "rename to lib/b.js",
    "index 5555555..6666666 100644",
    "--- a/lib/a.js",
    "+++ b/lib/b.js",
    "@@ -1,2 +1,2 @@",
    "-x",
    "+y",
    " z",
    "diff --git a/logo.png b/logo.png",
    "index 7777777..8888888 100644",
    "Binary files a/logo.png and b/logo.png differ",
    "",
  ].join("\n"));
  assert.deepEqual(files.map((f) => [f.status, f.oldPath, f.newPath, f.binary]), [
    ["added", "tests/new.test.js", "tests/new.test.js", false],
    ["deleted", "old.txt", "old.txt", false],
    ["renamed", "lib/a.js", "lib/b.js", false],
    ["modified", "logo.png", "logo.png", true],
  ]);
  assert.deepEqual(files[0].hunks[0].lines.map((l) => l.newNo), [1, 2]);
  assert.equal(filePath(files[1]), "old.txt");
  assert.deepEqual(files[2].hunks[0].lines.map((l) => [l.oldNo, l.newNo]), [[1, undefined], [undefined, 1], [2, 2]]);
});

test("no newline at end of file, and quoted paths", () => {
  const [f] = parseDiff([
    'diff --git "a/my file.txt" "b/my file.txt"',
    "--- \"a/my file.txt\"",
    "+++ \"b/my file.txt\"",
    "@@ -1 +1 @@",
    "-old",
    "\\ No newline at end of file",
    "+new",
    "\\ No newline at end of file",
  ].join("\n"));
  assert.equal(f.newPath, "my file.txt");
  assert.deepEqual(f.hunks[0].lines.map((l) => l.kind), ["del", "note", "add", "note"]);
  assert.equal(f.hunks[0].lines[1].text, "No newline at end of file");
});

test("a pure mode change or empty diff", () => {
  assert.deepEqual(parseDiff(""), []);
  const [f] = parseDiff("diff --git a/run.sh b/run.sh\nold mode 100644\nnew mode 100755\n");
  assert.equal(f.hunks.length, 0);
});
