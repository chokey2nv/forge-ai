import { Fragment, useEffect, useMemo, useState } from "react";
import {
  api, FEEDBACK_OPEN, isParent, threadWhere,
  type ReviewerNote, type Task, type TaskDiff, type Thread, type ThreadList,
} from "./api";
import { parseDiff, filePath, type DiffFile, type DiffLine } from "./diff";

// Above this many diff lines, files start collapsed.
const OPEN_LIMIT = 1500;

/** What the diff viewer needs to show and take feedback. */
interface Feedback {
  threads: ThreadList | null;
  /** Who is commenting: the decision bar's reviewer name. */
  author: string;
  onChange: () => void;
}

/**
 * A task's change, by file and hunk with line numbers. For a parent it is
 * the combined change of a revision (or the work in progress), optionally
 * compared with an earlier revision, with the human review threads and the
 * final reviewers' notes on their lines.
 */
export function DiffView({ task, feedback }: { task: Task; feedback?: Feedback }) {
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

  // Threads and notes sit on the revision they were made on; a "changes
  // since" view has other line numbers on its old side, so it shows none inline.
  const inline = parent && since === 0;
  const threads = (feedback?.threads?.threads ?? []).filter((t) => t.revision === revision);
  const notes = (feedback?.threads?.notes ?? []).filter((n) => n.revision === revision);
  const canComment = !!feedback && parent && since === 0 && FEEDBACK_OPEN.has(task.status);
  const ctx: FileCtx = { task, revision, canComment, feedback };

  // Notes whose file and line the diff shows go inline; the rest are listed.
  const shown = new Set<string>();
  for (const f of files) for (const h of f.hunks) for (const l of h.lines) if (l.newNo) shown.add(`${filePath(f)}:${l.newNo}`);
  const unplacedNotes = notes.filter((n) => !inline || !n.path || !n.line || !shown.has(`${n.path}:${n.line}`));

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
      {parent && feedback && (
        <ThreadIndex task={task} feedback={feedback} revision={revision} canComment={canComment} notes={unplacedNotes} />
      )}
      {canComment && <p className="hint">Click a line number to comment on that line.</p>}
      {files.map((f) => (
        <FileDiff key={filePath(f) + revision + "-" + since} file={f} open={lineCount <= OPEN_LIMIT}
          threads={inline ? threads.filter((t) => t.anchor?.path === filePath(f)) : []}
          notes={inline ? notes.filter((n) => n.path === filePath(f) && n.line && shown.has(`${n.path}:${n.line}`)) : []}
          ctx={ctx} />
      ))}
    </section>
  );
}

interface FileCtx {
  task: Task;
  revision: number;
  canComment: boolean;
  feedback?: Feedback;
}

const STATUS_LABEL = { added: "added", deleted: "deleted", modified: "", renamed: "renamed" };

// A line is commented on by its new-side number, or its old-side one for a deleted line.
const sideOf = (l: DiffLine): { side: "old" | "new"; line: number } | null =>
  l.newNo ? { side: "new", line: l.newNo } : l.oldNo ? { side: "old", line: l.oldNo } : null;
const lineKey = (side: string, line: number) => `${side}:${line}`;

function FileDiff({ file, open, threads, notes, ctx }: {
  file: DiffFile; open: boolean; threads: Thread[]; notes: ReviewerNote[]; ctx: FileCtx;
}) {
  const [composing, setComposing] = useState<string | null>(null);
  const byLine = new Map<string, Thread[]>();
  for (const t of threads) {
    const k = lineKey(t.anchor!.side, t.anchor!.line);
    byLine.set(k, [...(byLine.get(k) ?? []), t]);
  }
  const notesByLine = new Map<number, ReviewerNote[]>();
  for (const n of notes) notesByLine.set(n.line!, [...(notesByLine.get(n.line!) ?? []), n]);
  const open_ = threads.filter((t) => t.status !== "resolved").length;

  return (
    <details className="diff-file" open={open}>
      <summary>
        {STATUS_LABEL[file.status] && <span className={`file-status ${file.status}`}>{STATUS_LABEL[file.status]}</span>}
        <code>{file.status === "renamed" ? `${file.oldPath} → ${file.newPath}` : filePath(file)}</code>
        {threads.length > 0 && <span className="file-threads">{threads.length} thread{threads.length === 1 ? "" : "s"}{open_ ? `, ${open_} open` : ""}</span>}
        {notes.length > 0 && <span className="file-threads note">{notes.length} reviewer note{notes.length === 1 ? "" : "s"}</span>}
        <span className="file-counts"><span className="adds">+{file.additions}</span> <span className="dels">−{file.deletions}</span></span>
      </summary>
      {file.binary ? <p className="hint">Binary file.</p> : file.hunks.length === 0 ? <p className="hint">No content changes (mode or rename only).</p> : (
        <table className="diff-table">
          <tbody>
            {file.hunks.map((h, i) => (
              <Fragment key={`h${i}`}>
                <tr className="hunk"><td colSpan={3}>{h.header}</td></tr>
                {h.lines.map((l, j) => {
                  const at = sideOf(l);
                  const k = at ? lineKey(at.side, at.line) : "";
                  // A context line can be commented on from either side; both show its threads.
                  const here = at ? [...(byLine.get(k) ?? []), ...(l.kind === "ctx" && l.oldNo ? byLine.get(lineKey("old", l.oldNo)) ?? [] : [])] : [];
                  const lineNotes = l.newNo ? notesByLine.get(l.newNo) ?? [] : [];
                  const commentable = ctx.canComment && !!at;
                  const toggle = () => setComposing(composing === k ? null : k);
                  return (
                    <Fragment key={`${i}-${j}`}>
                      <tr className={`${l.kind}${composing === k ? " selected" : ""}`}>
                        <td className={`ln${commentable && l.kind === "del" ? " commentable" : ""}`} onClick={commentable && l.kind === "del" ? toggle : undefined}
                          title={commentable && l.kind === "del" ? "Comment on this line" : undefined}>{l.oldNo ?? ""}</td>
                        <td className={`ln${commentable && l.kind !== "del" ? " commentable" : ""}`} onClick={commentable && l.kind !== "del" ? toggle : undefined}
                          title={commentable && l.kind !== "del" ? "Comment on this line" : undefined}>{l.newNo ?? ""}</td>
                        <td className="code">{l.kind === "add" ? "+" : l.kind === "del" ? "−" : l.kind === "note" ? "\\" : " "}{l.kind === "note" ? ` ${l.text}` : l.text}</td>
                      </tr>
                      {(here.length > 0 || lineNotes.length > 0 || composing === k) && (
                        <tr className="inline-threads">
                          <td colSpan={3}>
                            {lineNotes.map((n, x) => <NoteView key={`n${x}`} note={n} />)}
                            {here.map((t) => <ThreadView key={t.id} thread={t} ctx={ctx} />)}
                            {composing === k && at && (
                              <Composer ctx={ctx} placeholder={`Comment on ${filePath(file)}:${at.line}${at.side === "old" ? " (old)" : ""}`}
                                onCancel={() => setComposing(null)}
                                submit={(body) => api.createThread(ctx.task.id, {
                                  author: ctx.feedback!.author, body, revision: ctx.revision,
                                  anchor: { path: filePath(file), side: at.side, line: at.line },
                                }).then(() => setComposing(null))} />
                            )}
                          </td>
                        </tr>
                      )}
                    </Fragment>
                  );
                })}
              </Fragment>
            ))}
          </tbody>
        </table>
      )}
    </details>
  );
}

/** Every thread on the change, the reviewer notes not shown on a line, and a general comment box. */
function ThreadIndex({ task, feedback, revision, canComment, notes }: {
  task: Task; feedback: Feedback; revision: number; canComment: boolean; notes: ReviewerNote[];
}) {
  const [composing, setComposing] = useState(false);
  const all = feedback.threads?.threads ?? [];
  const general = all.filter((t) => !t.anchor && t.revision === revision);
  const elsewhere = all.filter((t) => t.revision !== revision);
  const open = all.filter((t) => t.status !== "resolved").length;
  const ctx: FileCtx = { task, revision, canComment, feedback };
  if (!all.length && !notes.length && !canComment) return null;
  return (
    <div className="thread-index">
      <h4>Threads <span className="version">{all.length ? `${all.length} in all, ${open} unresolved` : "none yet"}</span></h4>
      {all.length > 0 && (
        <ul className="thread-list">
          {all.map((t) => (
            <li key={t.id}>
              <span className={`thread-status ${t.status}`}>{t.status}</span> <code>{threadWhere(t)}</code>
              <span className="version"> r{t.revision}</span> — {t.comments[0]?.body.slice(0, 100)}
              <span className="version"> ({t.comments[0]?.author}{t.comments.length > 1 ? `, ${t.comments.length - 1} repl${t.comments.length === 2 ? "y" : "ies"}` : ""})</span>
              {t.addressedBy && t.status !== "resolved" && <span className="version"> · taken on by a follow-up subtask</span>}
            </li>
          ))}
        </ul>
      )}
      {general.map((t) => <ThreadView key={t.id} thread={t} ctx={ctx} />)}
      {elsewhere.length > 0 && <p className="hint">{elsewhere.length} thread{elsewhere.length === 1 ? " is" : "s are"} on another revision; pick it above to see {elsewhere.length === 1 ? "it" : "them"} on its lines.</p>}
      {notes.length > 0 && (
        <div className="general-notes">
          <h4>Reviewer notes <span className="version">not on a line of this diff</span></h4>
          {notes.map((n, i) => <NoteView key={i} note={n} />)}
        </div>
      )}
      {canComment && (composing
        ? <Composer ctx={ctx} placeholder="Comment on the whole change" onCancel={() => setComposing(false)}
            submit={(body) => api.createThread(task.id, { author: feedback.author, body, revision }).then(() => setComposing(false))} />
        : <button className="link" onClick={() => setComposing(true)}>Comment on the whole change</button>)}
    </div>
  );
}

function ThreadView({ thread: t, ctx }: { thread: Thread; ctx: FileCtx }) {
  const [replying, setReplying] = useState(false);
  const [err, setErr] = useState("");
  const fb = ctx.feedback!;
  const can = FEEDBACK_OPEN.has(ctx.task.status);
  const setStatus = (action: "resolve" | "reopen") =>
    api.setThreadStatus(ctx.task.id, t.id, action, fb.author).then(fb.onChange).catch((e) => setErr(String(e)));
  return (
    <div className={`thread thread-${t.status}`}>
      <div className="thread-head">
        <span className={`thread-status ${t.status}`}>{t.status}</span> <code>{threadWhere(t)}</code>
        {t.resolvedBy && <span className="version"> resolved by {t.resolvedBy}</span>}
        {t.addressedBy && !t.resolvedBy && <span className="version"> · taken on by follow-up subtask <code>{t.addressedBy}</code></span>}
      </div>
      {t.comments.map((c, i) => (
        <div key={i} className={`comment comment-${c.kind}`}>
          <strong>{c.author}</strong>{c.kind !== "human" && <span className="version"> ({c.kind})</span>}
          <span className="version"> · {new Date(c.at).toLocaleString()}</span>
          <p>{c.body}</p>
        </div>
      ))}
      {err && <p className="error">{err}</p>}
      {can && (replying
        ? <Composer ctx={ctx} placeholder="Reply" onCancel={() => setReplying(false)}
            submit={(body) => api.replyThread(ctx.task.id, t.id, fb.author, body).then(() => setReplying(false))} />
        : <div className="thread-actions">
            <button className="link" onClick={() => setReplying(true)}>Reply</button>
            {t.status === "resolved"
              ? <button className="link" onClick={() => setStatus("reopen")}>Reopen</button>
              : <button className="link" onClick={() => setStatus("resolve")}>Resolve</button>}
          </div>)}
    </div>
  );
}

function NoteView({ note: n }: { note: ReviewerNote }) {
  return (
    <div className={`reviewer-note severity-${n.severity}`}>
      <span className="note-tag">{n.severity}</span> <strong>{n.reviewer}</strong>
      {n.location && <span className="version"> · {n.location}</span>}
      <p>{n.description}</p>
    </div>
  );
}

function Composer({ ctx, placeholder, submit, onCancel }: {
  ctx: FileCtx; placeholder: string; submit: (body: string) => Promise<unknown>; onCancel: () => void;
}) {
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const author = ctx.feedback!.author.trim();
  const send = async () => {
    setBusy(true);
    try { await submit(body); setBody(""); ctx.feedback!.onChange(); } catch (e) { setErr(String(e)); } finally { setBusy(false); }
  };
  return (
    <div className="composer">
      <textarea autoFocus value={body} onChange={(e) => setBody(e.target.value)} placeholder={placeholder} />
      {err && <p className="error">{err}</p>}
      <div>
        <button className="approve" disabled={busy || !body.trim() || !author} onClick={send}>{busy ? "Saving…" : "Comment"}</button>
        <button onClick={onCancel}>Cancel</button>
        <span className="version"> as {author || "— set your name in the Decision box"}</span>
      </div>
    </div>
  );
}
