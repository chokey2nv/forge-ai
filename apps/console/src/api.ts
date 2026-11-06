const BASE = import.meta.env.VITE_API_URL ?? "http://localhost:8080";

export type TaskStatus =
  | "CREATED" | "PLANNED" | "DECOMPOSING" | "DISPATCHED" | "RUNNING" | "VERIFYING"
  | "AGENT_REVIEW" | "ESCALATED" | "REVIEW_PENDING" | "INTEGRATING" | "INTEGRATED"
  | "APPROVED" | "DELIVERING" | "PR_OPEN" | "MERGED" | "ROLLING_BACK" | "REVERT_PR_OPEN" | "ROLLED_BACK" | "BLOCKED" | "RESUMABLE" | "REJECTED";


export interface AgentDefinition {
  name: string;
  description: string;
  version: number;
  modelPreference?: string;
  skills: string[];
  tools: string[];
  reviewers: string[];
  systemPrompt: string;
  contentHash: string;
  loadedAt: string;
}

export interface Skill {
  name: string;
  description: string;
  appliesTo: string[];
  content: string;
  contentHash: string;
  loadedAt: string;
}

export interface Task {
  budget?: { maxSteps: number; maxCostUsd?: number };
  id: string;
  requirement: string;
  acceptance: string[];
  status: TaskStatus;
  latestRunId?: string;
  createdAt: string;
  updatedAt: string;
  // Phase 8 task graph
  parentId?: string;
  title?: string;
  assignedAgent?: string;
  dependsOn?: string[];
  /** Added by the platform: overlapping or undeclared diffScope, so these never run in parallel. */
  implicitDependsOn?: string[];
  integrationBranch?: string;
  targetBranch?: string;
  /** Phase 11.3: what approval did (set once delivery starts). */
  delivery?: Delivery;
  /** Phase 12: each version of a parent's combined change put up for human review. */
  revisions?: Revision[];
  /** Phase 13.4: the project the task belongs to; who created it (13.3). */
  projectId?: string;
  createdBy?: string;
  /** Phase 12.3: each time a human sent the combined change back to the head engineer. */
  followUps?: FollowUp[];
  /** Follow-up subtask: the review threads it takes on. */
  addresses?: string[];
  /** A person's choice of board column, where the status leaves one. */
  boardLane?: "todo" | "closed";
}

export interface FollowUp {
  n: number;
  kind: "request_changes" | "escalation_retry";
  revision: number;
  reviewer: string;
  summary?: string;
  runId: string;
  /** "planned", or "refused: …" when the head engineer's graph was not accepted. */
  outcome?: string;
  at: string;
}

export interface Revision {
  n: number;
  commit: string;
  baseCommit: string;
  finalReviewRunId: string;
  at: string;
}

/** GET /tasks/{id}/diff: revision 0 is the work in progress. */
export interface TaskDiff {
  taskId: string;
  revisions: Revision[];
  revision: number;
  since?: number;
  from: string;
  to: string;
  diff: string;
}

/** Phase 12.2: a human's comment thread on a revision of a combined change. */
export type ThreadStatus = "open" | "addressed" | "resolved" | "outdated";

export interface Anchor {
  path: string;
  side: "old" | "new";
  line: number;
  lineText: string;
  before?: string[];
  after?: string[];
}

export interface ThreadComment {
  author: string;
  kind: "human" | "agent" | "reviewer";
  body: string;
  revision: number;
  at: string;
}

export interface Thread {
  id: string;
  taskId: string;
  /** The revision it was opened on. */
  revision: number;
  /** The revision it is placed on now: later revisions that keep its line carry it along (12.4). */
  shownOn?: number;
  commit: string;
  anchor?: Anchor;
  /** The anchor as opened, before any move. */
  origin?: Anchor;
  /** A later revision (outdatedOn) changed or removed its line; anchor keeps its last place. */
  outdated?: boolean;
  outdatedOn?: number;
  status: ThreadStatus;
  comments: ThreadComment[];
  addressedBy?: string;
  resolvedBy?: string;
  createdAt: string;
  updatedAt: string;
}

/** A final reviewer's finding, read-only; path and line when its location names them. */
export interface ReviewerNote {
  revision: number;
  reviewer: string;
  verdict: string;
  severity: string;
  description: string;
  location?: string;
  path?: string;
  line?: number;
}

export interface ThreadList {
  threads: Thread[];
  notes: ReviewerNote[];
}

/** Threads and replies are accepted only while a human is looking at the change. */
export const FEEDBACK_OPEN = new Set<TaskStatus>(["REVIEW_PENDING", "ESCALATED"]);

/** The revision a thread is placed on (threads from before 12.4 have no shownOn). */
export const shownOn = (t: Thread) => t.shownOn || t.revision;

export const threadWhere = (t: Thread) =>
  t.anchor ? `${t.anchor.path}:${t.anchor.line}${t.anchor.side === "old" ? " (old)" : ""}` : "general";

export interface Delivery {
  mode: "pull_request" | "local_merge" | "unavailable";
  targetBranch: string;
  branch: string;
  remote?: string;
  targetMoved?: boolean;
  mergeCommit?: string;
  prUrl?: string;
  prNumber?: number;
  error?: string;
  startedAt: string;
  finishedAt?: string;
  rollback?: Rollback;
}

/** Phase 11.4: undoing a delivery by revert (a commit, or a revert PR). */
export interface Rollback {
  reviewer: string;
  rationale?: string;
  revertCommit?: string;
  branch?: string;
  prUrl?: string;
  prNumber?: number;
  error?: string;
  startedAt: string;
  finishedAt?: string;
}

/** A decomposed parent: its own review covers the combined change. */
export const isParent = (t: Task) => !t.parentId && !!t.integrationBranch;

// A parent being worked on again after a revision was recorded: its next
// final review is the one in progress.
const REWORKING = new Set<TaskStatus>(["DECOMPOSING", "DISPATCHED", "RUNNING", "INTEGRATING", "VERIFYING", "AGENT_REVIEW", "ESCALATED"]);

/**
 * The run whose reviews decide the task: a parent's final review (of the
 * latest revision, or of the one being prepared), otherwise its latest run.
 * Revision 1 keeps the ID used before revisions existed.
 */
export const reviewRunId = (t: Task) => {
  if (!isParent(t)) return t.latestRunId;
  const revs = t.revisions ?? [];
  if (revs.length && !REWORKING.has(t.status)) return revs[revs.length - 1].finalReviewRunId;
  const n = revs.length + 1;
  // An escalated final review sent back is tried again: -a2, -a3, …
  const retries = (t.followUps ?? []).filter((f) => f.kind === "escalation_retry" && f.revision === revs.length).length;
  return (n === 1 ? `run-${t.id}-final-review` : `run-${t.id}-final-review-r${n}`) + (retries ? `-a${retries + 1}` : "");
};

export interface Run {
  id: string;
  taskId: string;
  status: string;
  stepCount: number;
  plan?: { steps: { description: string; files?: string[] }[]; risks?: string[] };
  reviewerVerdict?: string;
  reviewRound?: number;
  blockedReason?: string;
  provenance?: { agentDefinition: string; model: string; provider: string };
  lastCheckpoint?: { workspaceDiff: string; status: string };
}

export type Verdict = "pass" | "fail" | "error";

export interface Review {
  id: string;
  round: number;
  index: number;
  reviewer: string;
  agentDefinition?: string;
  verdict: Verdict;
  summary?: string;
  defects?: { severity: string; description: string; location?: string }[];
  startedAt: string;
  finishedAt: string;
}

export type EscalationDecision = "accept" | "retry" | "reject";

/** What approving a task will do (GET /tasks/{id}/delivery). */
export interface DeliveryPreview {
  mode: "pull_request" | "local_merge" | "unavailable";
  remote: { name: string; url: string; host?: string; repo?: string } | null;
  targetBranch?: string;
  branch?: string;
  ghReady: boolean;
  ghAccount?: string;
  action: string;
  warnings: string[];
  deliversOnApproval: boolean;
}

export interface VerificationRun {
  id: string;
  passed: boolean;
  attempt: number;
  gates: { name: string; passed: boolean; skipped: boolean; skipReason?: string; exitCode: number; durationMs: number; output?: string }[];
}

export interface Event {
  id: string;
  type: string;
  from?: string;
  to?: string;
  reason?: string;
  at: string;
}

export interface EvalResult {
  id: string;
  caseId: string;
  taskId: string;
  model: string;
  provider: string;
  passed: boolean;
  status: string;
  startedAt: string;
  durationMs: number;
  reason?: string;
}

export interface Approval {
  id: string;
  action: string;
  reviewer: string;
  rationale?: string;
  unresolvedThreads?: { id: string; where: string; status: ThreadStatus; author: string; comment: string }[];
  decidedAt: string;
}

/** GET /repos/inspect: what creating a task in a repository would do. */
export interface RepoInspection {
  repo?: { path: string; branches: string[]; currentBranch?: string };
  delivery?: DeliveryPreview;
  error?: string;
}

export interface Config {
  providers: string[];
  defaultProvider: string;
  defaultModel: string;
}

export interface NewTask {
  projectId: string;
  requirement: string;
  acceptance: string[];
  repoPath: string;
  targetBranch?: string;
  budget: { maxSteps: number; maxRetries: number; wallClockMs: number; maxTokens?: number; maxCostUsd?: number };
}

/** Phase 13.2: the signed-in person. */
export interface User {
  id: string;
  name: string;
  email?: string;
  instanceAdmin: boolean;
  /** Phase 13.3: viewer < member < reviewer; instance admins can do everything. */
  role: Role;
}

export type Role = "viewer" | "member" | "reviewer";
/** A role in a project (13.4): the instance roles plus the project's admin. */
export type ProjectRole = Role | "admin";
const RANK: Record<ProjectRole, number> = { viewer: 1, member: 2, reviewer: 3, admin: 4 };

/** Whether the user may do what needs role min across the instance (the API enforces it; the console only hides what would be refused). */
export const can = (u: User | null | undefined, min: Role) => !!u && (u.instanceAdmin || RANK[u.role] >= RANK[min]);
/** Whether a project role includes min. */
export const allows = (r: ProjectRole | "" | undefined, min: ProjectRole) => !!r && RANK[r] >= RANK[min];

/** Phase 13.4: a project, its repositories and members, with your role in it. */
export interface Project {
  id: string;
  name: string;
  repos: { path: string; defaultTarget?: string; addedAt: string }[];
  members: { userId: string; name: string; role: ProjectRole }[];
  myRole: ProjectRole | "";
}

/** Fired when the API answers 401: the session ended, so the console shows sign-in. */
// Spend caps (Phase 13.6). A cap of 0 or none means no cap.
export interface Limits { maxTokens?: number; maxCostUsd?: number }
export interface Spend { inputTokens: number; outputTokens: number; costUsd: number; unpricedTokens: number }
export interface SpendView { spend: Spend; limits: Limits }
export interface TaskSpend { rootTaskId: string; task: SpendView; subtask?: Spend; project?: SpendView }

export const SIGNED_OUT = "forge:signed-out";

async function j<T>(path: string, init?: RequestInit): Promise<T> {
  // The session cookie goes with every request (the API is on another port).
  const res = await fetch(BASE + path, { ...init, credentials: "include" });
  if (res.status === 401 && path !== "/auth/login") window.dispatchEvent(new Event(SIGNED_OUT));
  if (!res.ok) throw new Error(`${path}: HTTP ${res.status} — ${await res.text().catch(() => "")}`);
  return res.json();
}

export const api = {
    me: () => j<{ user: User; via: string }>("/auth/me"),
    login: (name: string, password: string) =>
        j<{ user: User }>("/auth/login", {
            method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name, password }),
        }),
    logout: () => fetch(BASE + "/auth/logout", { method: "POST", credentials: "include" }),
    getConfig: () => j<Config>("/config"),
    inspectRepo: (projectId: string, path: string, targetBranch: string, requirement: string) =>
        j<RepoInspection>(`/repos/inspect?${new URLSearchParams({ projectId, path, targetBranch, requirement })}`),
    createTask: (t: NewTask) =>
        j<Task>("/tasks", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(t) }),
    startTask: (taskId: string, provider: string, model: string) =>
        j<{ runId: string }>(`/tasks/${taskId}/runs`, {
            method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ provider, model }),
        }),
    listAgents: () => j<AgentDefinition[]>("/agents"),
    getAgent: (name: string) => j<AgentDefinition>(`/agents/${name}`),
    reloadAgents: () => j<Record<string, unknown>>("/agents/reload", { method: "POST" }),
    listSkills: () => j<Skill[]>("/skills"),
    getSkill: (name: string) => j<Skill>(`/skills/${name}`),
    listChildren: (parentId: string) => j<Task[]>(`/tasks/${parentId}/children`),


    listTasks: (status?: string, projectId?: string, limit?: number) => {
        const q = new URLSearchParams();
        if (status) q.set("status", status);
        if (projectId) q.set("projectId", projectId);
        if (limit) q.set("limit", String(limit));
        return j<Task[]>(`/tasks${q.size ? `?${q}` : ""}`);
    },
    listProjects: () => j<Project[]>("/projects"),
    createProject: (name: string) =>
        j<Project>("/projects", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name }) }),
    addProjectRepo: (pid: string, path: string, defaultTarget: string) =>
        j<Project>(`/projects/${pid}/repos`, {
            method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path, defaultTarget: defaultTarget || undefined }),
        }),
    setTaskMaxSteps: (taskId: string, maxSteps: number) =>
        j<{ maxSteps: number }>(`/tasks/${taskId}/max-steps`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ maxSteps }) }),
    removeProjectRepo: (pid: string, path: string) =>
        fetch(`${BASE}/projects/${pid}/repos?${new URLSearchParams({ path })}`, { method: "DELETE", credentials: "include" })
            .then(async (r) => { if (!r.ok) throw new Error(`HTTP ${r.status} — ${await r.text()}`); }),
    setProjectMember: (pid: string, name: string, role: ProjectRole) =>
        j<unknown>(`/projects/${pid}/members`, {
            method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name, role }),
        }),
    removeProjectMember: (pid: string, uid: string) =>
        fetch(`${BASE}/projects/${pid}/members/${uid}`, { method: "DELETE", credentials: "include" })
            .then(async (r) => { if (!r.ok) throw new Error(`HTTP ${r.status} — ${await r.text()}`); }),
    getTask: (id: string) => j<Task>(`/tasks/${id}`),
    listRuns: (taskId: string) => j<Run[]>(`/tasks/${taskId}/runs`),
    getRun: (id: string) => j<Run>(`/runs/${id}`),
    getEvents: (runId: string) => j<Event[]>(`/runs/${runId}/events`),
    getVerification: (runId: string) =>
        fetch(`${BASE}/runs/${runId}/verification`, { credentials: "include" }).then((r) => (r.ok ? (r.json() as Promise<VerificationRun>) : null)),
    listApprovals: (taskId: string) => j<Approval[]>(`/tasks/${taskId}/approvals`),
    // Decisions and comments are recorded as the signed-in user (13.3): no name is sent.
    approve: (taskId: string, rationale: string) =>
        j<{ status: string }>(`/tasks/${taskId}/approve`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ rationale }),
        }),
    reject: (taskId: string, rationale: string) =>
        j<{ status: string }>(`/tasks/${taskId}/reject`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ rationale }),
        }),
    listReviews: (runId: string) => j<Review[]>(`/runs/${runId}/reviews`),
    getTaskEvents: (taskId: string) => j<Event[]>(`/tasks/${taskId}/events`),
    getDelivery: (taskId: string) => j<DeliveryPreview>(`/tasks/${taskId}/delivery`),
    getTaskSpend: (taskId: string) => j<TaskSpend>(`/tasks/${taskId}/spend`),
    setTaskBudget: (taskId: string, l: Limits) =>
      j<Limits>(`/tasks/${taskId}/budget`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(l) }),
    getProjectSpend: (pid: string) => j<SpendView>(`/projects/${pid}/spend`),
    setProjectBudget: (pid: string, l: Limits) =>
      j<Limits>(`/projects/${pid}/budget`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(l) }),
    resumeRun: (runId: string) => j<{ runId: string; status: string }>(`/runs/${runId}/resume`, { method: "POST" }),
    getTaskDiff: (taskId: string, revision?: number, since?: number) => {
      const q = new URLSearchParams();
      if (revision !== undefined) q.set("revision", String(revision));
      if (since) q.set("since", String(since));
      return j<TaskDiff>(`/tasks/${taskId}/diff${q.size ? `?${q}` : ""}`);
    },
    requestChanges: (taskId: string, summary: string) =>
        j<{ status: string; followUp: number }>(`/tasks/${taskId}/request-changes`, {
            method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ summary }),
        }),
    listThreads: (taskId: string) => j<ThreadList>(`/tasks/${taskId}/threads`),
    createThread: (taskId: string, t: { body: string; revision?: number; anchor?: { path: string; side: "old" | "new"; line: number } }) =>
        j<Thread>(`/tasks/${taskId}/threads`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(t) }),
    replyThread: (taskId: string, threadId: string, body: string) =>
        j<Thread>(`/tasks/${taskId}/threads/${threadId}/comments`, {
            method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ body }),
        }),
    setThreadStatus: (taskId: string, threadId: string, action: "resolve" | "reopen") =>
        j<Thread>(`/tasks/${taskId}/threads/${threadId}/${action}`, {
            method: "POST",
        }),
    rollback: (taskId: string, rationale: string) =>
        j<{ status: string }>(`/tasks/${taskId}/rollback`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ rationale }),
        }),
    retryDelivery: (taskId: string) => j<{ status: string }>(`/tasks/${taskId}/deliver`, { method: "POST" }),
    moveOnBoard: (taskId: string, column: string) =>
        j<Task>(`/tasks/${taskId}/board`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ column }) }),
    escalate: (taskId: string) =>
        j<{ status: string }>(`/tasks/${taskId}/escalate`, { method: "POST" }),
    decideEscalation: (taskId: string, decision: EscalationDecision, rationale: string, guidance: string) =>
        j<{ status: string }>(`/tasks/${taskId}/escalation`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ decision, rationale, guidance }),
        }),
    listEvals: (model?: string) =>
        j<EvalResult[]>(`/evals/results${model ? `?model=${encodeURIComponent(model)}` : ""}`),
};