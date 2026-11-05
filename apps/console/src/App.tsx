import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import {
  api, SIGNED_OUT, allows, type Project, type ProjectRole, type User, isParent, reviewRunId, TASK_STATUSES,
  type Task, type Run, type Event, type VerificationRun, type EvalResult, type Approval, type Review,
  type EscalationDecision, type DeliveryPreview, type ThreadList, type RepoInspection, type Config, type Limits, type SpendView, type TaskSpend, AgentDefinition, Skill,
} from "./api";
import { DiffView } from "./DiffView";

type View = { kind: "tasks" } | { kind: "task"; id: string } | { kind: "evals" } | { kind: "new" } | { kind: "project" };

/** The signed-in person (Phase 13.2). */
const CurrentUser = createContext<User | null>(null);

/** The projects you are in, and the one you are working in (Phase 13.4). */
interface ProjectsState { list: Project[]; current: Project | null; select: (id: string) => void; reload: () => Promise<void> }
const Projects = createContext<ProjectsState>({ list: [], current: null, select: () => {}, reload: async () => {} });
const LAST_PROJECT = "forge.project";

/** Your role in a project; instance admins are admins everywhere. */
function useProjectRole(projectId?: string): ProjectRole | "" {
  const me = useContext(CurrentUser);
  const { list } = useContext(Projects);
  if (me?.instanceAdmin) return "admin";
  return list.find((p) => p.id === projectId)?.myRole ?? "";
}

export default function App() {
  const [view, setView] = useState<View>({ kind: "tasks" });
  // undefined: still asking who is signed in; null: nobody.
  const [user, setUser] = useState<User | null | undefined>(undefined);
  useEffect(() => {
    api.me().then((m) => setUser(m.user)).catch(() => setUser(null));
    const out = () => setUser(null);
    window.addEventListener(SIGNED_OUT, out);
    return () => window.removeEventListener(SIGNED_OUT, out);
  }, []);
  const [projects, setProjects] = useState<Project[]>([]);
  const [projectId, setProjectId] = useState(remembered(LAST_PROJECT));
  const reloadProjects = async () => { setProjects(await api.listProjects().catch(() => [])); };
  useEffect(() => { if (user) reloadProjects(); }, [user?.id]);
  const current = projects.find((p) => p.id === projectId) ?? projects[0] ?? null;
  const select = (id: string) => { setProjectId(id); remember(LAST_PROJECT, id); };

  if (user === undefined) return <div className="app"><p className="hint">Loading…</p></div>;
  if (user === null) return <SignIn onSignedIn={setUser} />;
  const signOut = async () => { await api.logout().catch(() => undefined); setUser(null); };

  const myRole: ProjectRole | "" = user.instanceAdmin ? "admin" : current?.myRole ?? "";

  return (
    <CurrentUser.Provider value={user}>
    <Projects.Provider value={{ list: projects, current, select, reload: reloadProjects }}>
    <div className="app">
      <header>
        <h1>Forge</h1>
        <nav>
          {projects.length > 0 && (
            <select className="project-switch" value={current?.id ?? ""} onChange={(e) => { select(e.target.value); setView({ kind: "tasks" }); }} title="Project">
              {projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
          )}
          <span className="signed-in">{user.name} · {myRole || "no project"}</span>
          {allows(myRole, "member") && <button className={view.kind === "new" ? "active" : ""} onClick={() => setView({ kind: "new" })}>New task</button>}
          <button className={view.kind === "tasks" ? "active" : ""} onClick={() => setView({ kind: "tasks" })}>Tasks</button>
          <button className={view.kind === "project" ? "active" : ""} onClick={() => setView({ kind: "project" })}>Project</button>
          <button className={view.kind === "evals" ? "active" : ""} onClick={() => setView({ kind: "evals" })}>Evals</button>
          <button onClick={signOut}>Sign out</button>
        </nav>
      </header>
      <main>
        {view.kind === "tasks" && <Tasks onOpen={(id) => setView({ kind: "task", id })} />}
        {view.kind === "task"  && (
          <TaskDetail key={view.id} id={view.id} onOpen={(id) => setView({ kind: "task", id })} onBack={() => setView({ kind: "tasks" })} />
        )}
        {view.kind === "evals" && <Evals />}
        {view.kind === "new" && <NewTaskForm onCreated={(id) => setView({ kind: "task", id })} onCancel={() => setView({ kind: "tasks" })} />}
        {view.kind === "project" && <ProjectPage />}
      </main>
    </div>
    </Projects.Provider>
    </CurrentUser.Provider>
  );
}

function SignIn({ onSignedIn }: { onSignedIn: (u: User) => void }) {
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try { onSignedIn((await api.login(name, password)).user); }
    catch { setErr("Wrong name or password."); }
    finally { setBusy(false); }
  };
  return (
    <div className="app">
      <form className="sign-in" onSubmit={submit}>
        <h1>Forge</h1>
        <label>Name <input autoFocus autoComplete="username" value={name} onChange={(e) => setName(e.target.value)} /></label>
        <label>Password <input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} /></label>
        {err && <p className="error">{err}</p>}
        <button className="approve" disabled={busy || !name || !password}>{busy ? "Signing in…" : "Sign in"}</button>
        <p className="hint">No account yet? An instance admin creates one: <code>forge users add &lt;name&gt;</code>.</p>
      </form>
    </div>
  );
}

const LAST_REPO = "forge.newTask.repoPath";
const remembered = (k: string) => { try { return localStorage.getItem(k) ?? ""; } catch { return ""; } };
const remember = (k: string, v: string) => { try { localStorage.setItem(k, v); } catch { /* private mode */ } };

/**
 * Drop a task on the board: where it runs, what it must do, and — before
 * anything is created — which branch it will get and what approving it will
 * do. Creating it creates its branch; starting it hands it to the head engineer.
 */
function NewTaskForm({ onCreated, onCancel }: { onCreated: (id: string) => void; onCancel: () => void }) {
  const { current: project } = useContext(Projects);
  const repos = project?.repos ?? [];
  const [repoPath, setRepoPath] = useState(() => repos.some((r) => r.path === remembered(LAST_REPO)) ? remembered(LAST_REPO) : repos[0]?.path ?? "");
  const [target, setTarget] = useState("");
  const [requirement, setRequirement] = useState("");
  const [acceptance, setAcceptance] = useState("");
  const [config, setConfig] = useState<Config | null>(null);
  const [provider, setProvider] = useState("");
  const [model, setModel] = useState("");
  const [maxSteps, setMaxSteps] = useState(30);
  const [maxTokens, setMaxTokens] = useState("");
  const [maxCost, setMaxCost] = useState("");
  const [maxRetries, setMaxRetries] = useState(2);
  const [minutes, setMinutes] = useState(30);
  const [startNow, setStartNow] = useState(true);
  const [inspection, setInspection] = useState<RepoInspection | null>(null);
  const [busy, setBusy] = useState<"" | "creating" | "starting">("");
  const [err, setErr] = useState("");

  useEffect(() => {
    api.getConfig().then((c) => { setConfig(c); setProvider(c.defaultProvider); setModel(c.defaultModel); })
      .catch((e) => setErr(String(e)));
  }, []);

  // Re-inspect as the path, target or wording change (debounced): the
  // branch name comes from the requirement's first words.
  useEffect(() => {
    if (!repoPath.trim() || !project) { setInspection(null); return; }
    const t = setTimeout(() => {
      api.inspectRepo(project.id, repoPath.trim(), target, requirement).then(setInspection).catch((e) => setInspection({ error: String(e) }));
    }, 400);
    return () => clearTimeout(t);
  }, [project?.id, repoPath, target, requirement]);

  const repo = inspection?.repo;
  const d = inspection?.delivery;
  const criteria = acceptance.split("\n").map((l) => l.replace(/^\s*[-*\d.)]+\s*/, "").trim()).filter(Boolean);
  const ready = !!repo && !inspection?.error && requirement.trim() !== "" && criteria.length > 0 && !busy;

  const submit = async () => {
    setBusy("creating"); setErr("");
    try {
      const task = await api.createTask({
        projectId: project!.id,
        requirement: requirement.trim(), acceptance: criteria, repoPath: repoPath,
        targetBranch: target || undefined,
        budget: { maxSteps, maxRetries, wallClockMs: minutes * 60_000, maxTokens: Number(maxTokens) || 0, maxCostUsd: Number(maxCost) || 0 },
      });
      remember(LAST_REPO, repoPath.trim());
      if (startNow) {
        setBusy("starting");
        // Returns once the head engineer's workspace is up (a container and
        // the repository's setup), so this can take a while.
        // If it fails, the task exists anyway and its page shows why (BLOCKED).
        await api.startTask(task.id, provider, model).catch(() => {});
      }
      onCreated(task.id);
    } catch (e) { setErr(String(e)); setBusy(""); }
  };

  return (
    <div className="detail new-task">
      <h2>New task</h2>
      {err && <p className="error">{err}</p>}

      <section>
        <label>Repository (one of project {project?.name ?? "—"}'s)
          {repos.length > 0
            ? <select value={repoPath} onChange={(e) => { setRepoPath(e.target.value); setTarget(""); }}>
                {repos.map((r) => <option key={r.path} value={r.path}>{r.path}</option>)}
              </select>
            : <p className="hint">This project has no repositories yet. A project admin adds them on the <em>Project</em> page.</p>}
        </label>
        {inspection?.error && <p className="error">{inspection.error}</p>}
        {repo && (
          <label>Target branch (where the work is delivered)
            <select value={target || repo.currentBranch || ""} onChange={(e) => setTarget(e.target.value)}>
              {repo.branches.map((b) => <option key={b} value={b}>{b}{b === repo.currentBranch ? " (checked out)" : ""}</option>)}
            </select>
          </label>
        )}
      </section>

      <section>
        <label>Requirement
          <textarea rows={4} value={requirement} onChange={(e) => setRequirement(e.target.value)}
            placeholder="What should change, and why. The head engineer splits it into subtasks." />
        </label>
        <label>Acceptance criteria (one per line)
          <textarea rows={4} value={acceptance} onChange={(e) => setAcceptance(e.target.value)}
            placeholder={"applyDiscount(200, 10) returns 180\nnpm test passes"} />
        </label>
      </section>

      {d && repo && (
        <section className={`delivery delivery-${d.mode}`}>
          <h3>What happens</h3>
          <ol>
            <li>Creating the task creates branch <code>{d.branch}</code> from <code>{d.targetBranch}</code>; each subtask branches off it and is merged back after review.</li>
            <li>{d.action}</li>
          </ol>
          <p className="hint">
            {d.remote
              ? <>Remote <code>{d.remote.name}</code> found ({d.remote.host ? `${d.remote.host}/${d.remote.repo}` : d.remote.url}): approval opens a pull request.</>
              : <>No remote: approval merges into <code>{d.targetBranch}</code> in this repository.</>}
          </p>
          {d.warnings.map((w, i) => <p key={i} className="warning">⚠ {w}</p>)}
        </section>
      )}

      <section>
        <details>
          <summary>Model and budget</summary>
          <div className="row">
            <label>Provider
              <select value={provider} onChange={(e) => setProvider(e.target.value)}>
                {(config?.providers ?? []).map((p) => <option key={p} value={p}>{p}</option>)}
              </select>
            </label>
            <label>Model <input value={model} onChange={(e) => setModel(e.target.value)} /></label>
          </div>
          <div className="row">
            <label>Max steps <input type="number" min={1} value={maxSteps} onChange={(e) => setMaxSteps(+e.target.value)} /></label>
            <label>Max retries <input type="number" min={0} value={maxRetries} onChange={(e) => setMaxRetries(+e.target.value)} /></label>
            <label>Time limit (minutes) <input type="number" min={1} value={minutes} onChange={(e) => setMinutes(+e.target.value)} /></label>
          </div>
          <div className="row">
            <label>Token cap <input type="number" min={0} value={maxTokens} onChange={(e) => setMaxTokens(e.target.value)} placeholder="default" /></label>
            <label>Cost cap ($) <input type="number" min={0} step="0.01" value={maxCost} onChange={(e) => setMaxCost(e.target.value)} placeholder="default" /></label>
          </div>
        </details>
        <label className="inline"><input type="checkbox" checked={startNow} onChange={(e) => setStartNow(e.target.checked)} /> Start now (hand it to the head engineer)</label>
      </section>

      <div>
        <button className="approve" disabled={!ready} onClick={submit}>{busy === "creating" ? "Creating…" : busy === "starting" ? "Starting… (preparing the workspace)" : startNow ? "Create and start" : "Create"}</button>
        <button className="neutral" onClick={onCancel}>Cancel</button>
      </div>
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
  const { current: project, list } = useContext(Projects);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [status, setStatus] = useState("");
  const [showSubtasks, setShowSubtasks] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    if (!project) { setTasks([]); return; }
    api.listTasks(status || undefined, project.id)
      .then(setTasks)
      .catch((e) => setErr(String(e)));
  }, [status, project?.id]);

  if (!project) {
    return <p className="hint">{list.length === 0 ? "You are not in any project yet. An instance admin creates projects and a project admin adds you." : "Pick a project."}</p>;
  }

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

const TERMINAL = new Set(["MERGED", "REJECTED", "INTEGRATED", "ROLLED_BACK"]);

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
  const [threads, setThreads] = useState<ThreadList | null>(null);
  const [delivery, setDelivery] = useState<DeliveryPreview | null>(null);
  const [spend, setSpend] = useState<TaskSpend | null>(null);
  const [blockedWhy, setBlockedWhy] = useState("");
  const [resuming, setResuming] = useState(false);
  // Decisions and comments are recorded as the signed-in user (13.3); what
  // they may do follows their role in the task's project (13.4).
  const me = useContext(CurrentUser);
  const role = useProjectRole(task?.projectId);
  const mayDecide = allows(role, "reviewer");
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
      setThreads(isParent(t) ? await api.listThreads(id).catch(() => null) : null);
      setDelivery(!t.parentId ? await api.getDelivery(id).catch(() => null) : null);
      setSpend(await api.getTaskSpend(id).catch(() => null));
      setBlockedWhy(t.status === "BLOCKED" ? (await api.getTaskEvents(id).catch(() => [] as Event[])).filter((e) => e.to === "BLOCKED").pop()?.reason ?? "" : "");
      setErr("");
    } catch (e) { setErr(String(e)); }
  };

  const childWhy = (c: Task) => (childEvents[c.id] ?? []).filter((e) => e.to === "BLOCKED").pop()?.reason ?? "";
  const blockedChild = children.find((c) => c.status === "BLOCKED");

  useEffect(() => { reload(); }, [id]);
  // Follow a task while the pipeline is still moving it.
  useEffect(() => {
    if (!task || TERMINAL.has(task.status)) return;
    const t = setInterval(reload, 3000);
    return () => clearInterval(t);
  }, [id, task?.status]);

  const reloadThreads = () => { api.listThreads(id).then(setThreads).catch((e) => setErr(String(e))); };
  const unresolved = (threads?.threads ?? []).filter((t) => t.status !== "resolved").length;

  const [sendingBack, setSendingBack] = useState(false);
  const requestChanges = async () => {
    setSendingBack(true);
    try { await api.requestChanges(id, rationale); setRationale(""); await reload(); } catch (e) { setErr(String(e)); } finally { setSendingBack(false); }
  };

  const decide = async (action: "approve" | "reject") => {
    try {
      if (action === "approve") await api.approve(id, rationale);
      else                       await api.reject(id, rationale);
      await reload();
    } catch (e) { setErr(String(e)); }
  };

  const rollback = async (why: string) => {
    try { await api.rollback(id, why); await reload(); } catch (e) { setErr(String(e)); }
  };

  const retryDelivery = async () => {
    try { await api.retryDelivery(id); await reload(); } catch (e) { setErr(String(e)); }
  };

  const decideEscalation = async (decision: EscalationDecision) => {
    try {
      await api.decideEscalation(id, decision, rationale, guidance);
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

      {spend && (
        <SpendPanel title={task.parentId ? "Spend (with the parent task)" : "Spend"} view={spend.task}
          onSave={!task.parentId && allows(role, "admin") ? async (l) => { await api.setTaskBudget(task.id, l); await reload(); } : undefined}>
          {task.parentId && <p className="hint">Subtasks share their parent's budget; change it on the parent.</p>}
          {task.status === "BLOCKED" && !task.parentId && children.length === 0 && !task.delivery && (
            <p>
              Blocked before its subtasks were planned{blockedWhy ? `: ${blockedWhy}` : ""}.{" "}
              {allows(role, "member") && <button className="neutral" onClick={() => api.startTask(task.id, run?.provenance?.provider ?? "", run?.provenance?.model ?? "").then(reload).catch((e) => setErr(String(e)))}>Start again</button>}
            </p>
          )}
          {task.status === "BLOCKED" && !!task.parentId && run?.status === "BLOCKED" && /budget reached/.test(run.blockedReason ?? "") && (
            <p>
              {sentence(run.blockedReason!)}{" "}
              {allows(role, "member") && <button className="neutral" onClick={() => api.resumeRun(run.id).then(reload).catch((e) => setErr(String(e)))}>Resume</button>}
            </p>
          )}
        </SpendPanel>
      )}

      {task.delivery ? <DeliveryRecord task={task} onRetry={mayDecide ? retryDelivery : undefined} onRollback={mayDecide ? rollback : undefined} /> : delivery && <DeliveryHint preview={delivery} />}

      {task.status === "ESCALATED" && !mayDecide && (
        <p className="hint">Escalated: waiting for a reviewer to decide.</p>
      )}
      {task.status === "ESCALATED" && mayDecide && (
        <section className="approval escalation">
          <h3>Escalated — your decision</h3>
          <p>{sentence(reviewRun?.blockedReason ?? "The reviewers kept rejecting this change")} Their findings are under <em>Reviews</em> below.</p>
          <textarea value={rationale} onChange={(e) => setRationale(e.target.value)} placeholder="rationale (recorded with the decision)" />
          <textarea value={guidance} onChange={(e) => setGuidance(e.target.value)}
            placeholder={`guidance for the ${isParent(task) ? "head engineer" : "implementer"} (used by Send back)`} />
          <div>
            <button className="approve" onClick={() => decideEscalation("accept")}>
              {task.parentId ? "Accept and integrate" : "Accept"}
            </button>
            <button className="neutral" onClick={() => decideEscalation("retry")}>Send back</button>
            <button className="reject" onClick={() => decideEscalation("reject")}>Reject</button>
          </div>
          {isParent(task) && <p className="hint">Send back asks the head engineer to plan follow-up work from the change as it stands, with your guidance, the reviewers' findings and any open threads.</p>}
        </section>
      )}

      {task.status === "RESUMABLE" && run && (
        <p className="warning blocked-why">
          This run was interrupted: its agent stopped reporting, because the agent or Forge stopped or lost contact. Its work up to the last checkpoint is kept.{" "}
          {allows(role, "member") && (
            <button className="neutral" disabled={resuming}
              onClick={() => { setResuming(true); api.resumeRun(run.id).then(reload).catch((e) => setErr(String(e))).finally(() => setResuming(false)); }}>
              {resuming ? "Resuming… (installing packages can take minutes)" : "Resume"}
            </button>
          )}
        </p>
      )}

      {task.status === "BLOCKED" && !task.delivery && (children.length > 0 || task.parentId) && (
        <p className="warning blocked-why">
          {blockedChild
            ? <>Blocked by subtask “{blockedChild.title ?? blockedChild.requirement.slice(0, 100)}”{childWhy(blockedChild) ? `: ${childWhy(blockedChild)}` : "."}</>
            : <>Blocked{blockedWhy ? `: ${blockedWhy}` : "."}</>}
        </p>
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
                  <td>
                    {c.title ?? c.requirement.slice(0, 100)}
                    {c.status === "BLOCKED" && childWhy(c) && <div className="warning blocked-why">{childWhy(c)}</div>}
                    {c.status === "RESUMABLE" && <div className="warning blocked-why">Interrupted: open it to resume.</div>}
                  </td>
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

      {task.status === "REVIEW_PENDING" && !mayDecide && (
        <p className="hint">Waiting for a reviewer's decision.{allows(role, "member") && isParent(task) ? " You can comment on the change below." : ""}</p>
      )}
      {task.status === "REVIEW_PENDING" && mayDecide && (
        <section className="approval">
          <h3>Decision</h3>
          {delivery && <p className="hint">{delivery.deliversOnApproval ? delivery.action : "Approval records the decision only."}</p>}
          <p className="hint">Deciding as <strong>{me?.name}</strong>.</p>
          <textarea value={rationale} onChange={(e) => setRationale(e.target.value)}
            placeholder={isParent(task) ? "rationale (optional); for Request changes, what to change beyond the threads" : "rationale (optional)"} />
          {unresolved > 0 && (
            <p className="warning">
              {unresolved} unresolved thread{unresolved === 1 ? "" : "s"} on this change. Approving leaves {unresolved === 1 ? "it" : "them"} open:
              {" "}{unresolved === 1 ? "it is" : "they are"} listed in the approval record{delivery?.mode === "pull_request" ? " and the pull request description" : ""}.
            </p>
          )}
          <div>
            <button className="approve" onClick={() => decide("approve")}>{unresolved > 0 ? `Approve with ${unresolved} open` : "Approve"}</button>
            {isParent(task) && (
              <button className="neutral" disabled={sendingBack || (!unresolved && !rationale.trim())} onClick={requestChanges}
                title={!unresolved && !rationale.trim() ? "Comment on the change, or say what to change above" : undefined}>
                {sendingBack ? "Starting the head engineer…" : `Request changes${unresolved ? ` (${unresolved} thread${unresolved === 1 ? "" : "s"})` : ""}`}
              </button>
            )}
            <button className="reject"  onClick={() => decide("reject")}>Reject</button>
          </div>
          {isParent(task) && (
            <p className="hint">Request changes sends the open threads and your note to the head engineer, who plans follow-up subtasks; the change comes back as revision {(task.revisions?.length ?? 0) + 1}.</p>
          )}
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

      {(task.followUps?.length ?? 0) > 0 && (
        <section>
          <h3>Follow-ups</h3>
          <ul className="followups">{task.followUps!.map((f) => (
            <li key={f.n}>
              <strong>{f.kind === "request_changes" ? `Changes requested on r${f.revision}` : "Escalation sent back"}</strong> by {f.reviewer}
              <span className="version"> · {new Date(f.at).toLocaleString()}</span>
              {f.summary && <> — {f.summary}</>}
              <div className={f.outcome?.startsWith("refused") ? "error" : "hint"}>
                {f.outcome === "planned" ? "The head engineer planned follow-up subtasks." : f.outcome ?? "The head engineer is planning…"}
              </div>
            </li>
          ))}</ul>
        </section>
      )}

      <DiffView task={task} feedback={isParent(task) ? { threads, user: me, role, onChange: reloadThreads } : undefined} />

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
              {a.unresolvedThreads?.length ? (
                <ul className="files">{a.unresolvedThreads.map((t) => (
                  <li key={t.id}>{a.action === "request_changes" ? "" : "left "}{t.status}: <code>{t.where}</code> — {t.comment} ({t.author})</li>
                ))}</ul>
              ) : null}
            </li>
          ))}</ul>
        </section>
      )}
    </div>
  );
}

function Evals() {
  const [results, setResults] = useState<EvalResult[]>([]);
  const [costs, setCosts] = useState<Record<string, number>>({});
  const [err, setErr] = useState("");

  useEffect(() => {
    api.listEvals().then((rs) => {
      setResults(rs);
      rs.forEach((r) => api.getTaskSpend(r.taskId).then((s) => setCosts((c) => ({ ...c, [r.taskId]: s.task.spend.costUsd }))).catch(() => {}));
    }).catch((e) => setErr(String(e)));
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
      <p className="hint">
        How well the agents do real work: each case runs through Forge on a fresh copy of a small repository, then its result is
        checked separately from Forge's own checks and reviewers. Run it from the repository's folder with <code>make eval</code> (or <code>make eval CASE=0002</code>).
        It spends real money with a paid model, up to $0.50 a case and $2 a run, so run it when you change a prompt or a model.
      </p>
      {results.length === 0 && !err && <p className="hint">No results yet.</p>}
      {Array.from(byModel.entries()).map(([k, list]) => {
        const passed = list.filter((r) => r.passed).length;
        return (
          <section key={k}>
            <h3>{k} — {passed}/{list.length} passing</h3>
            <table>
              <thead><tr><th>Case</th><th>Status</th><th>Result</th><th>Cost</th><th>Duration</th><th>Started</th></tr></thead>
              <tbody>
                {list.map((r) => (
                  <tr key={r.id}>
                    <td>{r.caseId}</td>
                    <td>{r.status}</td>
                    <td className={r.passed ? "passed" : "failed"}>
                      {r.passed ? "PASS" : "FAIL"}
                      {r.reason && <div className="warning blocked-why">{r.reason}</div>}
                    </td>
                    <td>{costs[r.taskId] !== undefined ? `$${costs[r.taskId].toFixed(4)}` : "—"}</td>
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

/** Where the task's work lives and what approving it will do. */
function DeliveryHint({ preview: p }: { preview: DeliveryPreview }) {
  return (
    <section className={`delivery delivery-${p.mode}`}>
      <h3>Delivery</h3>
      {p.branch && (
        <p>
          Work branch <code>{p.branch}</code> → target <code>{p.targetBranch}</code>
          {" · "}
          {p.remote ? <>remote <code>{p.remote.name}</code> ({p.remote.host ? `${p.remote.host}/${p.remote.repo}` : p.remote.url})</> : "no remote"}
        </p>
      )}
      <p><strong>{p.action}</strong></p>
      {p.warnings.map((w, i) => <p key={i} className="warning">⚠ {w}</p>)}
    </section>
  );
}

/** What approval did: the merge commit or pull request, or why it stopped. */
function DeliveryRecord({ task, onRetry, onRollback }: { task: Task; onRetry?: () => void; onRollback?: (why: string) => void }) {
  const d = task.delivery!;
  const rb = d.rollback;
  const blocked = task.status === "BLOCKED" && !!d.error;
  const [why, setWhy] = useState("");
  return (
    <section className={`delivery delivery-${d.mode}`}>
      <h3>Delivery <StatusBadge status={task.status} /></h3>
      <p>
        <code>{d.branch}</code> → <code>{d.targetBranch}</code>
        {" · "}{d.mode === "pull_request" ? <>pull request via <code>{d.remote}</code></> : "local merge"}
        {d.targetMoved && <> · <code>{d.targetBranch}</code> had moved: merged in and verified again</>}
      </p>
      {task.status === "DELIVERING" && <p className="hint">Delivering…</p>}
      {d.prUrl && <p>Pull request <a href={d.prUrl} target="_blank" rel="noreferrer">#{d.prNumber}</a>
        {task.status === "PR_OPEN" && " — waiting for it to be merged on the forge"}</p>}
      {d.mergeCommit && <p>Merge commit <code>{d.mergeCommit.slice(0, 12)}</code></p>}
      {blocked && (
        <>
          <pre className="error">{d.error}</pre>
          {onRetry && <button className="neutral" onClick={onRetry}>Retry delivery</button>}
        </>
      )}
      {rb && (
        <div className="rollback">
          <h4>Rollback <span className="version">by {rb.reviewer}{rb.rationale ? `: ${rb.rationale}` : ""}</span></h4>
          {task.status === "ROLLING_BACK" && <p className="hint">Rolling back…</p>}
          {rb.prUrl && <p>Revert pull request <a href={rb.prUrl} target="_blank" rel="noreferrer">#{rb.prNumber}</a>
            {task.status === "REVERT_PR_OPEN" && " — waiting for it to be merged on the forge"}</p>}
          {rb.revertCommit && task.status === "ROLLED_BACK" && <p>Revert commit <code>{rb.revertCommit.slice(0, 12)}</code> on <code>{d.targetBranch}</code></p>}
          {rb.error && task.status === "MERGED" && <pre className="error">{rb.error}</pre>}
        </div>
      )}
      {task.status === "MERGED" && d.mergeCommit && onRollback && (
        <div className="rollback">
          <p className="hint">
            {d.mode === "pull_request"
              ? <>Roll back → push a branch reverting <code>{d.mergeCommit.slice(0, 12)}</code> and open a revert pull request into <code>{d.targetBranch}</code>.</>
              : <>Roll back → add a commit on <code>{d.targetBranch}</code> reverting <code>{d.mergeCommit.slice(0, 12)}</code>. History is not rewritten.</>}
          </p>
          <textarea value={why} onChange={(e) => setWhy(e.target.value)} placeholder="why (recorded with the rollback)" />
          <button className="reject" onClick={() => onRollback(why)}>Roll back</button>
        </div>
      )}
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

/** What model calls have cost so far against the caps (13.6). */
function SpendPanel({ title, view, onSave, children }: { title: string; view: SpendView; onSave?: (l: Limits) => Promise<void>; children?: ReactNode }) {
  const { spend: s, limits: l } = view;
  const tokens = s.inputTokens + s.outputTokens;
  const [maxTokens, setMaxTokens] = useState(l.maxTokens ? String(l.maxTokens) : "");
  const [maxCost, setMaxCost] = useState(l.maxCostUsd ? String(l.maxCostUsd) : "");
  const [err, setErr] = useState("");
  const save = async () => {
    setErr("");
    try { await onSave!({ maxTokens: Number(maxTokens) || 0, maxCostUsd: Number(maxCost) || 0 }); } catch (e) { setErr(String(e)); }
  };
  return (
    <section className="spend">
      <h3>{title}</h3>
      <p>
        {tokens.toLocaleString()} tokens{l.maxTokens ? ` of ${l.maxTokens.toLocaleString()}` : ""}
        {" · "}${s.costUsd.toFixed(4)}{l.maxCostUsd ? ` of $${l.maxCostUsd.toFixed(2)}` : ""}
        {!l.maxTokens && !l.maxCostUsd && <span className="version"> no cap</span>}
      </p>
      {s.unpricedTokens > 0 && <p className="hint">{s.unpricedTokens.toLocaleString()} tokens are from models with no price, so they are not in the cost.</p>}
      {children}
      {onSave && (
        <div className="inline-form">
          <input value={maxTokens} onChange={(e) => setMaxTokens(e.target.value)} inputMode="numeric" placeholder="token cap (none)" aria-label="Token cap" />
          <input value={maxCost} onChange={(e) => setMaxCost(e.target.value)} inputMode="decimal" placeholder="cost cap in $ (none)" aria-label="Cost cap in dollars" />
          <button onClick={save}>Save caps</button>
        </div>
      )}
      {err && <p className="error">{err}</p>}
    </section>
  );
}

function StatusBadge({ status }: { status: string }) {
  return <span className={`badge badge-${status.toLowerCase()}`}>{status}</span>;
}
/**
 * The current project's repositories and members (13.4). Project admins
 * add and remove them; instance admins also create projects.
 */
function ProjectPage() {
  const me = useContext(CurrentUser);
  const { current: p, list, select, reload } = useContext(Projects);
  const [err, setErr] = useState("");
  const [name, setName] = useState("");
  const [repo, setRepo] = useState("");
  const [target, setTarget] = useState("");
  const [member, setMember] = useState("");
  const [memberRole, setMemberRole] = useState<ProjectRole>("member");
  const run = async (f: () => Promise<unknown>) => { setErr(""); try { await f(); await reload(); } catch (e) { setErr(String(e)); } };
  const admin = me?.instanceAdmin || p?.myRole === "admin";
  const [spend, setSpend] = useState<SpendView | null>(null);
  const loadSpend = () => { if (p) api.getProjectSpend(p.id).then(setSpend).catch(() => setSpend(null)); };
  useEffect(loadSpend, [p?.id]);

  return (
    <div className="detail project-page">
      {err && <p className="error">{err}</p>}
      {me?.instanceAdmin && (
        <section>
          <h3>New project</h3>
          <div className="inline-form">
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="project name" />
            <button disabled={!name.trim()} onClick={() => run(async () => { const np = await api.createProject(name.trim()); setName(""); select(np.id); })}>Create</button>
          </div>
        </section>
      )}
      {!p ? <p className="hint">{list.length ? "Pick a project." : "No projects yet."}</p> : (
        <>
          <h2>{p.name} <span className="version">your role: {me?.instanceAdmin ? "admin (instance)" : p.myRole}</span></h2>
          {spend && (
            <SpendPanel key={p.id} title="Spend, all tasks" view={spend}
              onSave={admin ? async (l) => { await api.setProjectBudget(p.id, l); loadSpend(); } : undefined} />
          )}
          <section>
            <h3>Repositories</h3>
            <p className="hint">Tasks in this project can only work in these, on the control plane's machine.</p>
            <ul className="project-list">
              {p.repos.map((r) => (
                <li key={r.path}><code>{r.path}</code>{r.defaultTarget && <span className="version"> → {r.defaultTarget}</span>}
                  {admin && <button className="link" onClick={() => run(() => api.removeProjectRepo(p.id, r.path))}>remove</button>}</li>
              ))}
              {p.repos.length === 0 && <li className="hint">None yet.</li>}
            </ul>
            {admin && (
              <div className="inline-form">
                <input value={repo} onChange={(e) => setRepo(e.target.value)} placeholder="/absolute/path/to/repository" />
                <input value={target} onChange={(e) => setTarget(e.target.value)} placeholder="default target branch (optional)" />
                <button disabled={!repo.trim()} onClick={() => run(async () => { await api.addProjectRepo(p.id, repo.trim(), target.trim()); setRepo(""); setTarget(""); })}>Add repository</button>
              </div>
            )}
          </section>
          <section>
            <h3>Members</h3>
            <table>
              <thead><tr><th>Name</th><th>Role</th><th></th></tr></thead>
              <tbody>
                {p.members.map((m) => (
                  <tr key={m.userId}>
                    <td>{m.name}</td>
                    <td>{admin
                      ? <select value={m.role} onChange={(e) => run(() => api.setProjectMember(p.id, m.name, e.target.value as ProjectRole))}>
                          {(["viewer", "member", "reviewer", "admin"] as ProjectRole[]).map((r) => <option key={r} value={r}>{r}</option>)}
                        </select>
                      : m.role}</td>
                    <td>{admin && <button className="link" onClick={() => run(() => api.removeProjectMember(p.id, m.userId))}>remove</button>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {admin && (
              <div className="inline-form">
                <input value={member} onChange={(e) => setMember(e.target.value)} placeholder="user name" />
                <select value={memberRole} onChange={(e) => setMemberRole(e.target.value as ProjectRole)}>
                  {(["viewer", "member", "reviewer", "admin"] as ProjectRole[]).map((r) => <option key={r} value={r}>{r}</option>)}
                </select>
                <button disabled={!member.trim()} onClick={() => run(async () => { await api.setProjectMember(p.id, member.trim(), memberRole); setMember(""); })}>Add member</button>
              </div>
            )}
          </section>
        </>
      )}
    </div>
  );
}
