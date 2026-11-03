// Parsing `git diff` output into files, hunks and numbered lines, so the
// console can show a change the way a code review does.

export type LineKind = "add" | "del" | "ctx" | "note";

export interface DiffLine {
  kind: LineKind;
  text: string;
  /** Line number on the old side (absent for added lines and notes). */
  oldNo?: number;
  /** Line number on the new side (absent for deleted lines and notes). */
  newNo?: number;
}

export interface Hunk {
  header: string;
  lines: DiffLine[];
}

export type FileStatus = "added" | "deleted" | "modified" | "renamed";

export interface DiffFile {
  oldPath: string;
  newPath: string;
  status: FileStatus;
  binary: boolean;
  additions: number;
  deletions: number;
  hunks: Hunk[];
}

/** The path a file is known by: its new path unless it was deleted. */
export const filePath = (f: DiffFile) => (f.status === "deleted" ? f.oldPath : f.newPath);

const HUNK = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/;

// "a/x b/x" as git prints it on the "diff --git" line; quoted paths (with
// spaces or unusual bytes) are unquoted.
function headerPaths(rest: string): [string, string] {
  const unquote = (s: string) => (s.startsWith('"') ? JSON.parse(s) as string : s);
  const m = rest.match(/^("(?:[^"\\]|\\.)*"|\S+) ("(?:[^"\\]|\\.)*"|\S+)$/);
  const [a, b] = m ? [unquote(m[1]), unquote(m[2])] : rest.split(" ");
  return [a.replace(/^a\//, ""), b.replace(/^b\//, "")];
}

const stripPrefix = (p: string) => {
  const s = p.startsWith('"') ? (JSON.parse(p) as string) : p;
  return s.replace(/^[ab]\//, "");
};

export function parseDiff(text: string): DiffFile[] {
  const files: DiffFile[] = [];
  let file: DiffFile | null = null;
  let hunk: Hunk | null = null;
  let oldNo = 0;
  let newNo = 0;

  for (const raw of text.split("\n")) {
    if (raw.startsWith("diff --git ")) {
      const [a, b] = headerPaths(raw.slice("diff --git ".length));
      file = { oldPath: a, newPath: b, status: "modified", binary: false, additions: 0, deletions: 0, hunks: [] };
      files.push(file);
      hunk = null;
      continue;
    }
    if (!file) continue;

    if (!hunk) {
      // Extended header lines, before the first hunk.
      if (raw.startsWith("new file mode")) file.status = "added";
      else if (raw.startsWith("deleted file mode")) file.status = "deleted";
      else if (raw.startsWith("rename from ")) { file.status = "renamed"; file.oldPath = raw.slice(12); }
      else if (raw.startsWith("rename to ")) { file.status = "renamed"; file.newPath = raw.slice(10); }
      else if (raw.startsWith("Binary files ") || raw === "GIT binary patch") file.binary = true;
      else if (raw.startsWith("--- ") && raw !== "--- /dev/null") file.oldPath = stripPrefix(raw.slice(4));
      else if (raw.startsWith("+++ ") && raw !== "+++ /dev/null") file.newPath = stripPrefix(raw.slice(4));
    }

    const m = raw.match(HUNK);
    if (m) {
      hunk = { header: raw, lines: [] };
      file.hunks.push(hunk);
      oldNo = Number(m[1]);
      newNo = Number(m[2]);
      continue;
    }
    if (!hunk) continue;

    if (raw.startsWith("+")) {
      hunk.lines.push({ kind: "add", text: raw.slice(1), newNo: newNo++ });
      file.additions++;
    } else if (raw.startsWith("-")) {
      hunk.lines.push({ kind: "del", text: raw.slice(1), oldNo: oldNo++ });
      file.deletions++;
    } else if (raw.startsWith(" ")) {
      hunk.lines.push({ kind: "ctx", text: raw.slice(1), oldNo: oldNo++, newNo: newNo++ });
    } else if (raw.startsWith("\\")) {
      hunk.lines.push({ kind: "note", text: raw.slice(2) }); // "\ No newline at end of file"
    }
    // An empty string is the split's last element; anything else ends nothing.
  }
  return files;
}
