import { useEffect, useMemo, useState } from "react";
import { api, type Config, type Task } from "./api";
import { ago, columnOf, COLUMNS, matches, moveTo, needsYou, onBoard, sortCards, STATUS_LABEL, type Column, type Move } from "./boardColumns";

const POLL_MS = 5000;

export function StatusBadge({ status }: { status: Task["status"] }) {
  return <span className={`badge badge-${status.toLowerCase()}`} title={status}>{STATUS_LABEL[status] ?? status}</span>;
}

const titleOf = (t: Task) => t.title ?? t.requirement.split("\n")[0];

/** Starting spends money, so it always says on what and asks first. */
export function StartDialog({ task, onClose, onStarted, onFailed }: { task: Task; onClose: () => void; onStarted: () => void; onFailed: (why: string) => void }) {
  const [config, setConfig] = useState<Config | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => { api.getConfig().then(setConfig).catch((e) => setErr(String(e))); }, []);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // The call returns once the workspace is up, which can take minutes, but the
  // task is under way long before that: no reason to hold the page.
  const start = () => {
    api.startTask(task.id, config!.defaultProvider, config!.defaultModel).catch((e) => onFailed(String(e)));
    setTimeout(onStarted, 800);
  };
  const cap = task.budget?.maxCostUsd;
  return (
    <div className="dialog-backdrop">
      <div className="dialog" role="dialog" aria-modal="true" aria-labelledby="start-title">
        <h3 id="start-title">Start this task?</h3>
        <p className="dialog-task">{titleOf(task)}</p>
        <p>
          The head engineer plans it, then agents do the work
          {config ? <> on <code>{config.defaultProvider}/{config.defaultModel}</code></> : ""}.
          {" "}This calls the model, which can cost money{cap ? `, up to this task's $${cap.toFixed(2)} cap` : ""}.
        </p>
        {err && <p className="error">{err}</p>}
        <div className="dialog-actions">
          <button onClick={onClose}>Cancel</button>
          <button className="approve" onClick={start} disabled={!config}>Start task</button>
        </div>
      </div>
    </div>
  );
}

function Card({ task, kids, parent, canMove, dragging, onOpen, onMove, onDrag }: {
  task: Task; kids: Task[]; parent?: Task; canMove: boolean; dragging: boolean;
  onOpen: () => void; onMove: (m: Move) => void; onDrag: (id: string | null) => void;
}) {
  const column = columnOf(task);
  const merged = kids.filter((k) => k.status === "INTEGRATED").length;
  const stuck = kids.filter(needsYou).length;
  const actions: { move: Move; label: string }[] = !canMove ? [] : [
    { move: "start" as Move, label: "Start" },
    { move: "todo" as Move, label: "Move to Todo" },
    { move: "backlog" as Move, label: "Back to Backlog" },
    { move: "closed" as Move, label: "Close" },
    { move: "done" as Move, label: "Reopen" },
  ].filter((a) => a.move === moveTo(task, a.move === "start" ? "progress" : a.move));
  const draggable = canMove && COLUMNS.some((c) => moveTo(task, c.id));

  return (
    <article
      className={`card${needsYou(task) ? " card-attention" : ""}${dragging ? " card-dragging" : ""}${column === "closed" ? " card-closed" : ""}`}
      draggable={draggable}
      onDragStart={(e) => { e.dataTransfer.setData("text/plain", task.id); e.dataTransfer.effectAllowed = "move"; onDrag(task.id); }}
      onDragEnd={() => onDrag(null)}
      onClick={onOpen}
    >
      {parent && <div className="card-parent" title={titleOf(parent)}>↳ {titleOf(parent)}</div>}
      <button className="card-title" onClick={(e) => { e.stopPropagation(); onOpen(); }}>{titleOf(task)}</button>
      <div className="card-meta">
        <StatusBadge status={task.status} />
        {kids.length > 0 && <span className={stuck ? "card-count stuck" : "card-count"}>{merged}/{kids.length} subtasks{stuck ? ` · ${stuck} need${stuck === 1 ? "s" : ""} you` : ""}</span>}
      </div>
      {kids.length > 0 && (
        <div className="card-progress" aria-hidden="true"><span style={{ width: `${(merged / kids.length) * 100}%` }} /></div>
      )}
      <div className="card-foot">
        <span className="card-who">{task.parentId ? task.assignedAgent : task.createdBy}</span>
        <span title={new Date(task.updatedAt).toLocaleString()}>{ago(task.updatedAt)}</span>
      </div>
      {actions.length > 0 && (
        <div className="card-actions">
          {actions.map((a) => (
            <button key={a.move} className={a.move === "start" ? "small approve" : "small"} onClick={(e) => { e.stopPropagation(); onMove(a.move); }}>{a.label}</button>
          ))}
        </div>
      )}
    </article>
  );
}

/** The project's tasks by stage. The pipeline moves most cards; people move the rest. */
export function Board({ projectId, canMove, onOpen, onNew }: { projectId: string; canMove: boolean; onOpen: (id: string) => void; onNew: () => void }) {
  const [tasks, setTasks] = useState<Task[] | null>(null);
  const [err, setErr] = useState("");
  const [query, setQuery] = useState("");
  const [showSubtasks, setShowSubtasks] = useState(false);
  const [needsYouOnly, setNeedsYouOnly] = useState(false);
  const [dragId, setDragId] = useState<string | null>(null);
  const [overColumn, setOverColumn] = useState<Column | null>(null);
  const [starting, setStarting] = useState<Task | null>(null);

  const load = () => api.listTasks(undefined, projectId, 500).then((t) => { setTasks(t); setErr(""); }).catch((e) => setErr(String(e)));
  useEffect(() => {
    setTasks(null);
    load();
    const t = setInterval(() => { if (!document.hidden) load(); }, POLL_MS);
    return () => clearInterval(t);
  }, [projectId]);

  const all = tasks ?? [];
  const byId = useMemo(() => new Map(all.map((t) => [t.id, t])), [tasks]);
  const kidsOf = useMemo(() => {
    const m = new Map<string, Task[]>();
    for (const t of all) if (t.parentId) m.set(t.parentId, [...(m.get(t.parentId) ?? []), t]);
    return m;
  }, [tasks]);
  const waiting = all.filter((t) => onBoard(t, false) && needsYou(t)).length;
  const shown = all.filter((t) => onBoard(t, showSubtasks) && matches(t, query) && (!needsYouOnly || needsYou(t)));
  const dragged = dragId ? byId.get(dragId) : undefined;

  const move = async (task: Task, m: Move) => {
    if (m === "start") { setStarting(task); return; }
    try {
      const updated = await api.moveOnBoard(task.id, m);
      setTasks((ts) => (ts ?? []).map((t) => (t.id === updated.id ? updated : t)));
      setErr("");
    } catch (e) { setErr(String(e)); }
  };

  const drop = (column: Column) => {
    const m = dragged && moveTo(dragged, column);
    if (dragged && m) move(dragged, m);
    setDragId(null); setOverColumn(null);
  };

  if (tasks === null && !err) return <p className="hint">Loading the board…</p>;

  return (
    <div className="board-page">
      <div className="board-toolbar">
        <input type="search" className="board-search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search tasks" aria-label="Search tasks" />
        <button className={needsYouOnly ? "filter on" : "filter"} aria-pressed={needsYouOnly} onClick={() => setNeedsYouOnly(!needsYouOnly)}>
          Needs you{waiting > 0 && <span className="filter-count">{waiting}</span>}
        </button>
        <label className="board-check"><input type="checkbox" checked={showSubtasks} onChange={(e) => setShowSubtasks(e.target.checked)} /> Show subtasks</label>
      </div>
      {err && <p className="error">{err}</p>}
      {all.length === 0 && !err ? (
        <div className="board-empty">
          <h2>No tasks yet</h2>
          <p className="hint">Add one to the backlog, or give it to the head engineer straight away.</p>
          {canMove && <button className="approve" onClick={onNew}>New task</button>}
        </div>
      ) : (
        <div className="board">
          {COLUMNS.map((c) => {
            const cards = sortCards(shown.filter((t) => columnOf(t) === c.id));
            const accepts = !!dragged && !!moveTo(dragged, c.id);
            return (
              <section key={c.id}
                className={`column column-${c.id}${dragged ? (accepts ? " column-accepts" : " column-refuses") : ""}${overColumn === c.id && accepts ? " column-over" : ""}`}
                aria-label={c.title}
                onDragOver={(e) => { if (accepts) { e.preventDefault(); e.dataTransfer.dropEffect = "move"; setOverColumn(c.id); } }}
                onDragLeave={(e) => { if (!e.currentTarget.contains(e.relatedTarget as Node)) setOverColumn(null); }}
                onDrop={(e) => { e.preventDefault(); drop(c.id); }}
              >
                <header className="column-head" title={c.hint}>
                  <h2>{c.title}</h2>
                  <span className="column-count">{cards.length}</span>
                </header>
                <div className="column-cards">
                  {cards.map((t) => (
                    <Card key={t.id} task={t} kids={kidsOf.get(t.id) ?? []} parent={t.parentId ? byId.get(t.parentId) : undefined}
                      canMove={canMove} dragging={dragId === t.id} onOpen={() => onOpen(t.id)} onMove={(m) => move(t, m)} onDrag={setDragId} />
                  ))}
                  {cards.length === 0 && <p className="column-empty">{dragged && accepts ? "Drop here" : c.hint}</p>}
                </div>
              </section>
            );
          })}
        </div>
      )}
      {starting && <StartDialog task={starting} onClose={() => setStarting(null)} onStarted={() => { setStarting(null); load(); }} onFailed={setErr} />}
    </div>
  );
}
