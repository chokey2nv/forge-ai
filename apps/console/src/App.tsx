import { useEffect, useState } from "react";
import {
  api, isParent, reviewRunId, TASK_STATUSES,
  type Task, type Run, type Event, type VerificationRun, type EvalResult, type Approval, type Review,
  type EscalationDecision, AgentDefinition, Skill,
} from "./api";

type View = { kind: "tasks" } | { kind: "task"; id: string } | { kind: "evals" };

export default function App() {
  const [view, setView] = useState<View>({ kind: "tasks" });
  return (
    <div className="app">
      <header>
        <h1>Forge</h1>
        <nav>
          <button className={view.kind === "tasks" ? "active" : ""} onClick={() => setView({ kind: "tasks" })}>Tasks</button>
          <button className={view.kind === "evals" ? "active" : ""} onClick={() => setView({ kind: "evals" })}>Evals</button>
        </nav>
      </header>
      <main>
        {view.kind === "tasks" && <Tasks onOpen={(id) => setView({ kind: "task", id })} />}
        {view.kind === "task"  && (
          <TaskDetail key={view.id} id={view.id} onOpen={(id) => setView({ kind: "task", id })} onBack={() => setView({ kind: "tasks" })} />
        )}
        {view.kind === "evals" && <Evals />}
      </main>
    </div>
  );
}

function Agents({ onOpen }: { onOpen: (name: string) => void }) {
  const [agents, setAgents] = useState<AgentDefinition[]>([]);
  const [err, setErr] = useState("");
  const [reloading, setReloading] = useState(false);

  const load = () => api.listAgents().then(setAgents).catch((e) => setErr(String(e)));
  useEffect(() => { load(); }, []);

  const reload = async () => {
    setReloading(true);
    try { await api.reloadAgents(); await load(); }
    catch (e) { setErr(String(e)); }
    finally { setReloading(false); }
  };

  return (
    <div>
      <div className="toolbar">
        <button onClick={reload} disabled={reloading}>
          {reloading ? "Reloading…" : "Reload from disk"}
        </button>
      </div>
      {err && <p className="error">{err}</p>}
      <table>
        <thead><tr><th>Name</th><th>Description</th><th>Skills</th><th>Tools</th><th>Reviewers</th></tr></thead>
        <tbody>
          {agents.map((a) => (
            <tr key={a.name} className="clickable" onClick={() => onOpen(a.name)}>
              <td><strong>{a.name}</strong> <span className="version">v{a.version}</span></td>
              <td>{a.description}</td>
              <td>{a.skills.join(", ") || "—"}</td>
              <td>{a.tools.length}</td>
              <td>{a.reviewers.join(", ") || "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function AgentDetail({ name, onBack }: { name: string; onBack: () => void }) {
  const [agent, setAgent] = useState<AgentDefinition | null>(null);
  const [skills, setSkills] = useState<Skill[]>([]);
  useEffect(() => {
    Promise.all([api.getAgent(name), api.listSkills()])
      .then(([a, s]) => { setAgent(a); setSkills(s.filter((x) => a.skills.includes(x.name))); })
      .catch(() => {});
  }, [name]);

  if (!agent) return <p>Loading…</p>;

  return (
    <div className="detail">
      <button className="link" onClick={onBack}>← Back</button>
      <h2>{agent.name} <span className="version">v{agent.version}</span></h2>
      <p>{agent.description}</p>

      <section>
        <h3>Tools</h3>
        <ul>{agent.tools.map((t) => <li key={t}><code>{t}</code></li>)}</ul>
      </section>

      <section>
        <h3>Skills</h3>
        {skills.length === 0 ? <p>—</p> : skills.map((s) => (
          <details key={s.name}>
            <summary><strong>{s.name}</strong> — {s.description}</summary>
            <pre className="skill-body">{s.content}</pre>
          </details>
        ))}
      </section>

      <section>
        <h3>Reviewers</h3>
        <p>{agent.reviewers.length ? agent.reviewers.join(", ") : "— (no agent review)"}</p>
      </section>

      <section>
        <h3>System prompt</h3>
        <pre className="system-prompt">{agent.systemPrompt}</pre>
      </section>

      <section>
        <h3>Metadata</h3>
        <p>hash: <code>{agent.contentHash}</code></p>
        <p>loaded: {new Date(agent.loadedAt).toLocaleString()}</p>
      </section>
    </div>
  );
}
function Tasks({ onOpen }: { onOpen: (id: string) => void }) {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [status, setStatus] = useState("");
  const [showSubtasks, setShowSubtasks] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    api.listTasks(status || undefined)
      .then(setTasks)
      .catch((e) => setErr(String(e)));
  }, [status]);

  // Subtasks are reached from their parent; listing them is opt-in. An
  // escalated subtask needs a human, so it is always listed.
  const shown = tasks.filter((t) => showSubtasks || !t.parentId || t.status === "ESCALATED");

  return (
    <div>
      <div className="toolbar">
        <label>Status:&nbsp;
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">all</option>
            {TASK_STATUSES.map((s) => <option key={s} value={s}>{s}</option>)}
          </select>
        </label>
        <label className="inline">
          <input type="checkbox" checked={showSubtasks} onChange={(e) => setShowSubtasks(e.target.checked)} /> Show subtasks
        </label>
      </div>
      {err && <p className="error">{err}</p>}
      <table>
        <thead><tr><th>Status</th><th>Task</th><th>Agent</th><th>Created</th></tr></thead>
        <tbody>
          {shown.map((t) => (
            <tr key={t.id} onClick={() => onOpen(t.id)} className="clickable">
              <td><StatusBadge status={t.status} /></td>
              <td>{t.parentId ? <span className="subtask">↳ {t.title ?? t.requirement.slice(0, 120)}</span> : t.requirement.slice(0, 120)}</td>
              <td>{t.assignedAgent ?? (isParent(t) ? "head-engineer" : "—")}</td>
              <td>{new Date(t.createdAt).toLocaleString()}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

const TERMINAL = new Set(["APPROVED", "MERGED", "REJECTED", "INTEGRATED"]);

const PRE_FINAL_REVIEW = new Set(["CREATED", "DECOMPOSING", "DISPATCHED", "INTEGRATING"]);

const sentence = (s: string) => (/[.!?]$/.test(s.trim()) ? s.trim() : s.trim() + ".");

function TaskDetail({ id, onBack, onOpen }: { id: string; onBack: () => void; onOpen: (id: string) => void }) {
  const [task, setTask] = useState<Task | null>(null);
  const [parent, setParent] = useState<Task | null>(null);
  const [children, setChildren] = useState<Task[]>([]);
  const [childEvents, setChildEvents] = useState<Record<string, Event[]>>({});
  const [run, setRun] = useState<Run | null>(null);
  const [reviewRun, setReviewRun] = useState<Run | null>(null);
  const [reviews, setReviews] = useState<Review[]>([]);
  const [events, setEvents] = useState<Event[]>([]);
  const [verification, setVerification] = useState<VerificationRun | null>(null);
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [reviewer, setReviewer] = useState("human");
  const [rationale, setRationale] = useState("");
  const [guidance, setGuidance] = useState("");
  const [err, setErr] = useState("");

  const reload = async () => {
    try {
      const t = await api.getTask(id);
      setTask(t);
      setParent(t.parentId ? await api.getTask(t.parentId) : null);
      const kids = isParent(t) ? await api.listChildren(t.id) : [];
      setChildren(kids);
      const evs: Record<string, Event[]> = {};
      for (const k of kids) evs[k.id] = await api.getTaskEvents(k.id).catch(() => []);
      setChildEvents(evs);
      if (t.latestRunId) {
        setRun(await api.getRun(t.latestRunId));
        setEvents(await api.getEvents(t.latestRunId));
      }
      // Reviews and gates belong to the run under review: a parent's final
      // review, otherwise the task's own run.
      // A parent's final-review run only exists once its subtasks are integrated.
      const rid = isParent(t) && PRE_FINAL_REVIEW.has(t.status) ? undefined : reviewRunId(t);
      if (rid) {
        setReviewRun(await api.getRun(rid).catch(() => null));
        setReviews(await api.listReviews(rid).catch(() => []));
        setVerification(await api.getVerification(rid));
      }
      setApprovals(await api.listApprovals(id));
      setErr("");
    } catch (e) { setErr(String(e)); }
  };

  useEffect(() => { reload(); }, [id]);
  // Follow a task while the pipeline is still moving it.
  useEffect(() => {
    if (!task || TERMINAL.has(task.status)) return;
    const t = setInterval(reload, 3000);
    return () => clearInterval(t);
  }, [id, task?.status]);

  const decide = async (action: "approve" | "reject") => {
    try {
      if (action === "approve") await api.approve(id, reviewer, rationale);
      else                       await api.reject(id, reviewer, rationale);
      await reload();
    } catch (e) { setErr(String(e)); }
  };

  const decideEscalation = async (decision: EscalationDecision) => {
    try {
      await api.decideEscalation(id, decision, reviewer, rationale, guidance);
      setRationale(""); setGuidance("");
      await reload();
    } catch (e) { setErr(String(e)); }
  };

  if (!task) return <p>Loading…</p>;

  const titleOf = (tid: string) => children.find((c) => c.id === tid)?.title ?? tid;

  return (
    <div className="detail">
      <button className="link" onClick={onBack}>← Back</button>
      {parent && (
        <p className="crumb">
          Subtask of <button className="link" onClick={() => onOpen(parent.id)}>{parent.requirement.slice(0, 80)}</button>
          {" "}<StatusBadge status={parent.status} />
          {task.assignedAgent && <> · assigned to <code>{task.assignedAgent}</code></>}
        </p>
      )}
      <h2><StatusBadge status={task.status} /> {task.title ?? task.requirement}</h2>
      {task.title && task.requirement.replace(task.title, "").trim() && (
        <p className="requirement">{task.requirement.replace(task.title, "").trim()}</p>
      )}
      {err && <p className="error">{err}</p>}

      <section>
        <h3>Acceptance criteria</h3>
        <ol>{task.acceptance.map((a, i) => <li key={i}>{a}</li>)}</ol>
      </section>

      {task.status === "ESCALATED" && (
        <section className="approval escalation">
          <h3>Escalated — your decision</h3>
          <p>{sentence(reviewRun?.blockedReason ?? "The reviewers kept rejecting this change")} Their findings are under <em>Reviews</em> below.</p>
          <input value={reviewer} onChange={(e) => setReviewer(e.target.value)} placeholder="your name" />
          <textarea value={rationale} onChange={(e) => setRationale(e.target.value)} placeholder="rationale (recorded with the decision)" />
          {!isParent(task) && (
            <textarea value={guidance} onChange={(e) => setGuidance(e.target.value)} placeholder="guidance for the implementer (used by Send back)" />
          )}
          <div>
            <button className="approve" onClick={() => decideEscalation("accept")}>
              {task.parentId ? "Accept and integrate" : "Accept"}
            </button>
            {!isParent(task) && <button className="neutral" onClick={() => decideEscalation("retry")}>Send back</button>}
            <button className="reject" onClick={() => decideEscalation("reject")}>Reject</button>
          </div>
          {isParent(task) && <p className="hint">A combined change has no implementer to send back to: accept or reject it.</p>}
        </section>
      )}

      {children.length > 0 && (
        <section>
          <h3>Subtasks</h3>
          <table>
            <thead><tr><th>Status</th><th>Subtask</th><th>Agent</th><th>Depends on</th></tr></thead>
            <tbody>
              {children.map((c) => (
                <tr key={c.id} className="clickable" onClick={() => onOpen(c.id)}>
                  <td><StatusBadge status={c.status} /></td>
                  <td>{c.title ?? c.requirement.slice(0, 100)}</td>
                  <td><code>{c.assignedAgent}</code></td>
                  <td>
                    {[...(c.dependsOn ?? []).map(titleOf), ...(c.implicitDependsOn ?? []).map((d) => `${titleOf(d)} (overlapping scope)`)]
                      .join(", ") || "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      )}

      {children.length > 1 && <Timeline children={children} events={childEvents} onOpen={onOpen} />}

      {reviews.length > 0 && <Reviews reviews={reviews} title={isParent(task) ? "Final review (combined change)" : "Reviews"} />}

      {task.status === "REVIEW_PENDING" && (
        <section className="approval">
          <h3>Decision</h3>
          <input value={reviewer} onChange={(e) => setReviewer(e.target.value)} placeholder="reviewer" />
          <textarea value={rationale} onChange={(e) => setRationale(e.target.value)} placeholder="rationale (optional)" />
          <div>
            <button className="approve" onClick={() => decide("approve")}>Approve</button>
            <button className="reject"  onClick={() => decide("reject")}>Reject</button>
          </div>
        </section>
      )}

      {run?.plan && (
        <section>
          <h3>Plan</h3>
          <ol>{run.plan.steps.map((s, i) => (
            <li key={i}>{s.description}{s.files?.length ? <span className="files"> [{s.files.join(", ")}]</span> : null}</li>
          ))}</ol>
          {run.plan.risks?.length ? <p><strong>Risks:</strong> {run.plan.risks.join("; ")}</p> : null}
        </section>
      )}

      {verification && (
        <section>
          <h3>Verification{isParent(task) ? " (combined change)" : ""}</h3>
          <ul className="gates">
            {verification.gates.map((g, i) => (
              <li key={i} className={g.skipped ? "skipped" : g.passed ? "passed" : "failed"}>
                <strong>{g.name}</strong> {g.skipped ? `(skipped${g.skipReason ? `: ${g.skipReason}` : ""})` : g.passed ? "passed" : `FAILED (exit ${g.exitCode})`}
                {g.output?.trim() && !g.skipped && (
                  <details><summary>output</summary><pre className="gate-output">{g.output}</pre></details>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}

      {run?.lastCheckpoint?.workspaceDiff && (
        <section>
          <h3>Diff</h3>
          <pre className="diff">{run.lastCheckpoint.workspaceDiff}</pre>
        </section>
      )}

      <section>
        <h3>Events</h3>
        <ul className="events">
          {events.map((e) => (
            <li key={e.id}>
              <code>{new Date(e.at).toLocaleTimeString()}</code> <strong>{e.type}</strong>
              {e.from && e.to ? ` ${e.from} → ${e.to}` : ""}
              {e.reason ? <span className="reason"> — {e.reason}</span> : ""}
            </li>
          ))}
        </ul>
      </section>

      {approvals.length > 0 && (
        <section>
          <h3>Approvals</h3>
          <ul>{approvals.map((a) => (
            <li key={a.id}>
              <strong>{a.action}</strong> by {a.reviewer} at {new Date(a.decidedAt).toLocaleString()}
              {a.rationale ? ` — ${a.rationale}` : ""}
            </li>
          ))}</ul>
        </section>
      )}
    </div>
  );
}

function Evals() {
  const [results, setResults] = useState<EvalResult[]>([]);
  const [err, setErr] = useState("");

  useEffect(() => {
    api.listEvals().then(setResults).catch((e) => setErr(String(e)));
  }, []);

  const byModel = new Map<string, EvalResult[]>();
  for (const r of results) {
    const k = `${r.provider}/${r.model}`;
    if (!byModel.has(k)) byModel.set(k, []);
    byModel.get(k)!.push(r);
  }

  return (
    <div>
      {err && <p className="error">{err}</p>}
      {Array.from(byModel.entries()).map(([k, list]) => {
        const passed = list.filter((r) => r.passed).length;
        return (
          <section key={k}>
            <h3>{k} — {passed}/{list.length} passing</h3>
            <table>
              <thead><tr><th>Case</th><th>Status</th><th>Result</th><th>Duration</th><th>Started</th></tr></thead>
              <tbody>
                {list.map((r) => (
                  <tr key={r.id}>
                    <td>{r.caseId}</td>
                    <td>{r.status}</td>
                    <td className={r.passed ? "passed" : "failed"}>{r.passed ? "PASS" : "FAIL"}</td>
                    <td>{(r.durationMs / 1000).toFixed(1)}s</td>
                    <td>{new Date(r.startedAt).toLocaleString()}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </section>
        );
      })}
    </div>
  );
}

const SETTLED = new Set(["INTEGRATED", "BLOCKED", "REJECTED", "ESCALATED"]);

/** Subtasks on one time axis, from claim to settling: overlapping bars ran at the same time. */
function Timeline({ children, events, onOpen }: { children: Task[]; events: Record<string, Event[]>; onOpen: (id: string) => void }) {
  const now = Date.now();
  const spans = children.flatMap((c) => {
    const tr = (events[c.id] ?? []).filter((e) => e.type === "state.transition");
    const claim = tr.find((e) => e.to === "DISPATCHED");
    if (!claim) return [];
    const end = tr.find((e) => e.to && SETTLED.has(e.to));
    return [{ task: c, start: Date.parse(claim.at), end: end ? Date.parse(end.at) : now, open: !end }];
  });
  if (spans.length === 0) return null;
  const t0 = Math.min(...spans.map((s) => s.start));
  const t1 = Math.max(...spans.map((s) => s.end));
  const total = Math.max(t1 - t0, 1);
  const secs = (ms: number) => `${Math.round(ms / 1000)}s`;
  const busy = spans.reduce((sum, s) => sum + (s.end - s.start), 0);
  return (
    <section>
      <h3>Timeline</h3>
      <p className="hint">
        Wall time {secs(t1 - t0)} for {secs(busy)} of subtask work. Bars that overlap ran at the same time.
      </p>
      <div className="timeline">
        {spans.map((s) => {
          const alongside = spans.filter((o) => o !== s && o.start < s.end && s.start < o.end).map((o) => o.task.title ?? o.task.id);
          return (
            <div key={s.task.id} className="timeline-row clickable" onClick={() => onOpen(s.task.id)}>
              <span className="timeline-label">{s.task.title ?? s.task.id}</span>
              <span className="timeline-track">
                <span
                  className={`timeline-bar bar-${s.task.status.toLowerCase()}${s.open ? " bar-open" : ""}`}
                  style={{ left: `${((s.start - t0) / total) * 100}%`, width: `${Math.max(((s.end - s.start) / total) * 100, 1)}%` }}
                  title={alongside.length ? `ran alongside ${alongside.join(", ")}` : "ran alone"}
                />
              </span>
              <span className="timeline-dur">{secs(s.end - s.start)}{alongside.length ? ` · with ${alongside.join(", ")}` : ""}</span>
            </div>
          );
        })}
      </div>
    </section>
  );
}

/** Every reviewer's verdict, grouped by review round, newest round first. */
function Reviews({ reviews, title }: { reviews: Review[]; title: string }) {
  const rounds = Array.from(new Set(reviews.map((r) => r.round))).sort((a, b) => b - a);
  return (
    <section>
      <h3>{title}</h3>
      {rounds.map((round) => {
        const inRound = reviews.filter((r) => r.round === round).sort((a, b) => a.index - b.index);
        const passed = inRound.every((r) => r.verdict === "pass");
        return (
          <div key={round} className="review-round">
            <h4>Round {round} — {passed ? "passed" : "rejected"}</h4>
            {inRound.map((r) => (
              <div key={r.id} className={`review review-${r.verdict}`}>
                <div><span className={`verdict verdict-${r.verdict}`}>{r.verdict.toUpperCase()}</span> <strong>{r.reviewer}</strong>
                  {r.agentDefinition && <span className="version"> {r.agentDefinition.replace(/^.*@/, "")}</span>}</div>
                {r.summary && <p>{r.summary}</p>}
                {r.defects?.length ? (
                  <ul className="defects">
                    {r.defects.map((d, i) => (
                      <li key={i}><span className={`severity severity-${d.severity}`}>{d.severity}</span> {d.description}
                        {d.location && <code> {d.location}</code>}</li>
                    ))}
                  </ul>
                ) : null}
              </div>
            ))}
          </div>
        );
      })}
    </section>
  );
}

function StatusBadge({ status }: { status: string }) {
  return <span className={`badge badge-${status.toLowerCase()}`}>{status}</span>;
}