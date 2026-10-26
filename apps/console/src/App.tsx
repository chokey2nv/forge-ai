import { useEffect, useState } from "react";
import { api, type Task, type Run, type Event, type VerificationRun, type EvalResult, type Approval, AgentDefinition, Skill } from "./api";

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
        {view.kind === "task"  && <TaskDetail id={view.id} onBack={() => setView({ kind: "tasks" })} />}
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
  const [err, setErr] = useState("");

  useEffect(() => {
    api.listTasks(status || undefined)
      .then(setTasks)
      .catch((e) => setErr(String(e)));
  }, [status]);

  return (
    <div>
      <div className="toolbar">
        <label>Status:&nbsp;
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">all</option>
            {["CREATED","RUNNING","VERIFYING","AGENT_REVIEW","REVIEW_PENDING","APPROVED","BLOCKED","REJECTED"].map(s =>
              <option key={s} value={s}>{s}</option>)}
          </select>
        </label>
      </div>
      {err && <p className="error">{err}</p>}
      <table>
        <thead><tr><th>Status</th><th>Requirement</th><th>Created</th></tr></thead>
        <tbody>
          {tasks.map((t) => (
            <tr key={t.id} onClick={() => onOpen(t.id)} className="clickable">
              <td><StatusBadge status={t.status} /></td>
              <td>{t.requirement.slice(0, 120)}</td>
              <td>{new Date(t.createdAt).toLocaleString()}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function TaskDetail({ id, onBack }: { id: string; onBack: () => void }) {
  const [task, setTask] = useState<Task | null>(null);
  const [run, setRun] = useState<Run | null>(null);
  const [events, setEvents] = useState<Event[]>([]);
  const [verification, setVerification] = useState<VerificationRun | null>(null);
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [reviewer, setReviewer] = useState("human");
  const [rationale, setRationale] = useState("");
  const [err, setErr] = useState("");

  const reload = async () => {
    try {
      const t = await api.getTask(id);
      setTask(t);
      if (t.latestRunId) {
        const r = await api.getRun(t.latestRunId);
        setRun(r);
        setEvents(await api.getEvents(t.latestRunId));
        setVerification(await api.getVerification(t.latestRunId));
      }
      setApprovals(await api.listApprovals(id));
    } catch (e) { setErr(String(e)); }
  };

  useEffect(() => { reload(); }, [id]);

  const decide = async (action: "approve" | "reject") => {
    try {
      if (action === "approve") await api.approve(id, reviewer, rationale);
      else                       await api.reject(id, reviewer, rationale);
      await reload();
    } catch (e) { setErr(String(e)); }
  };

  if (!task) return <p>Loading…</p>;

  

  return (
    <div className="detail">
      <button className="link" onClick={onBack}>← Back</button>
      <h2><StatusBadge status={task.status} /> {task.requirement}</h2>
      {err && <p className="error">{err}</p>}

      <section>
        <h3>Acceptance criteria</h3>
        <ol>{task.acceptance.map((a, i) => <li key={i}>{a}</li>)}</ol>
      </section>

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

      {run?.reviewerVerdict && (
        <section>
          <h3>Reviewer verdict</h3>
          <p><strong>{run.reviewerVerdict.toUpperCase()}</strong></p>
        </section>
      )}

      {verification && (
        <section>
          <h3>Verification</h3>
          <ul className="gates">
            {verification.gates.map((g, i) => (
              <li key={i} className={g.skipped ? "skipped" : g.passed ? "passed" : "failed"}>
                <strong>{g.name}</strong> {g.skipped ? "(skipped)" : g.passed ? "passed" : `FAILED (exit ${g.passed})`}
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

function StatusBadge({ status }: { status: string }) {
  return <span className={`badge badge-${status.toLowerCase()}`}>{status}</span>;
}