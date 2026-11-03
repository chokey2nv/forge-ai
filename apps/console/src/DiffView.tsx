import { useEffect, useMemo, useState } from "react";
import { api, type Task, type TaskDiff, isParent } from "./api";
import { parseDiff, filePath, type DiffFile } from "./diff";

// Above this many diff lines, files start collapsed.
const OPEN_LIMIT = 1500;

/**
 * A task's change, by file and hunk with line numbers. For a parent it is
 * the combined change of a revision (or the work in progress), optionally
 * compared with an earlier revision.
 */
export function DiffView({ task }: { task: Task }) {
  const parent = isParent(task);
  const revisions = task.revisions ?? [];
  // null follows the latest revision as new ones arrive.
  const [picked, setPicked] = useState<number | null>(null);
  const [since, setSince] = useState(0);
  const [data, setData] = useState<TaskDiff | null>(null);
  const [err, setErr] = useState("");

  const revision = picked ?? revisions.length;
  useEffect(() => {
    let live = true;
    api.getTaskDiff(task.id, parent ? revision : undefined, since || undefined)
      .then((d) => { if (live) { setData(d); setErr(""); } })
      .catch((e) => { if (live) setErr(String(e)); });
    return () => { live = false; };
    // Work in progress moves with the task; a recorded revision never changes.
  }, [task.id, parent, revision, since, revision === 0 ? task.updatedAt : ""]);

  const files = useMemo(() => parseDiff(data?.diff ?? ""), [data?.diff]);
  const additions = files.reduce((n, f) => n + f.additions, 0);
  const deletions = files.reduce((n, f) => n + f.deletions, 0);
  const lineCount = files.reduce((n, f) => n + f.hunks.reduce((m, h) => m + h.lines.length, 0), 0);

  if (!parent && !data?.diff) return null;

  const pick = (n: number) => { setPicked(n === revisions.length ? null : n); if (since >= n) setSince(0); };

  return (
    <section className="diff-view">
      <div className="diff-head">
        <h3>{parent ? "Combined change" : "Diff"}</h3>
        {parent && (
          <div className="diff-pickers">
            <label>Revision{" "}
              <select value={revision} onChange={(e) => pick(Number(e.target.value))}>
                {revisions.map((r) => (
                  <option key={r.n} value={r.n}>r{r.n} · {new Date(r.at).toLocaleString()} · {r.commit.slice(0, 8)}</option>
                ))}
                <option value={0}>Work in progress (branch tip)</option>
              </select>
            </label>
            {revision >= 2 && (
              <label>Compare with{" "}
                <select value={since} onChange={(e) => setSince(Number(e.target.value))}>
                  <option value={0}>the base (whole change)</option>
                  {revisions.filter((r) => r.n < revision).map((r) => (
                    <option key={r.n} value={r.n}>r{r.n} (changes since)</option>
                  ))}
                </select>
              </label>
            )}
          </div>
        )}
      </div>
      {err && <p className="error">{err}</p>}
      {data && (
        <p className="hint">
          {parent && revision === 0 && revisions.length === 0 ? "Not put up for review yet; this is the parent branch as it stands. " : ""}
          {files.length === 0
            ? "No changes."
            : <>{files.length} file{files.length === 1 ? "" : "s"} changed, <span className="adds">+{additions}</span> <span className="dels">−{deletions}</span>
                {data.from && <> · <code>{data.from.slice(0, 8)}</code>…<code>{data.to.length === 40 ? data.to.slice(0, 8) : data.to}</code></>}</>}
        </p>
      )}
      {files.map((f) => <FileDiff key={filePath(f) + revision + "-" + since} file={f} open={lineCount <= OPEN_LIMIT} />)}
    </section>
  );
}

const STATUS_LABEL = { added: "added", deleted: "deleted", modified: "", renamed: "renamed" };

function FileDiff({ file, open }: { file: DiffFile; open: boolean }) {
  return (
    <details className="diff-file" open={open}>
      <summary>
        {STATUS_LABEL[file.status] && <span className={`file-status ${file.status}`}>{STATUS_LABEL[file.status]}</span>}
        <code>{file.status === "renamed" ? `${file.oldPath} → ${file.newPath}` : filePath(file)}</code>
        <span className="file-counts"><span className="adds">+{file.additions}</span> <span className="dels">−{file.deletions}</span></span>
      </summary>
      {file.binary ? <p className="hint">Binary file.</p> : file.hunks.length === 0 ? <p className="hint">No content changes (mode or rename only).</p> : (
        <table className="diff-table">
          <tbody>
            {file.hunks.map((h, i) => [
              <tr key={`h${i}`} className="hunk"><td colSpan={3}>{h.header}</td></tr>,
              ...h.lines.map((l, j) => (
                <tr key={`${i}-${j}`} className={l.kind}>
                  <td className="ln">{l.oldNo ?? ""}</td>
                  <td className="ln">{l.newNo ?? ""}</td>
                  <td className="code">{l.kind === "add" ? "+" : l.kind === "del" ? "−" : l.kind === "note" ? "\\" : " "}{l.kind === "note" ? ` ${l.text}` : l.text}</td>
                </tr>
              )),
            ])}
          </tbody>
        </table>
      )}
    </details>
  );
}
