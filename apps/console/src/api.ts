const BASE = import.meta.env.VITE_API_URL ?? "http://localhost:8080";

export type TaskStatus =
  | "CREATED" | "PLANNED" | "DECOMPOSING" | "DISPATCHED" | "RUNNING" | "VERIFYING"
  | "AGENT_REVIEW" | "ESCALATED" | "REVIEW_PENDING" | "INTEGRATING" | "INTEGRATED"
  | "APPROVED" | "DELIVERING" | "PR_OPEN" | "MERGED" | "ROLLING_BACK" | "REVERT_PR_OPEN" | "ROLLED_BACK" | "BLOCKED" | "RESUMABLE" | "REJECTED";

export const TASK_STATUSES: TaskStatus[] = [
  "CREATED", "DECOMPOSING", "DISPATCHED", "RUNNING", "VERIFYING", "AGENT_REVIEW", "ESCALATED",
  "INTEGRATING", "INTEGRATED", "REVIEW_PENDING", "APPROVED", "DELIVERING", "PR_OPEN", "MERGED", "ROLLING_BACK", "REVERT_PR_OPEN", "ROLLED_BACK", "BLOCKED", "RESUMABLE", "REJECTED",
];


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
}

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

/** The run whose reviews decide the task: a parent's final review, otherwise its latest run. */
export const reviewRunId = (t: Task) => (isParent(t) ? `run-${t.id}-final-review` : t.latestRunId);

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
  requirement: string;
  acceptance: string[];
  repoPath: string;
  targetBranch?: string;
  budget: { maxSteps: number; maxRetries: number; wallClockMs: number };
}

async function j<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(BASE + path, init);
  if (!res.ok) throw new Error(`${path}: HTTP ${res.status} — ${await res.text().catch(() => "")}`);
  return res.json();
}

export const api = {
    getConfig: () => j<Config>("/config"),
    inspectRepo: (path: string, targetBranch: string, requirement: string) =>
        j<RepoInspection>(`/repos/inspect?${new URLSearchParams({ path, targetBranch, requirement })}`),
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


    listTasks: (status?: string) =>
        j<Task[]>(`/tasks${status ? `?status=${status}` : ""}`),
    getTask: (id: string) => j<Task>(`/tasks/${id}`),
    listRuns: (taskId: string) => j<Run[]>(`/tasks/${taskId}/runs`),
    getRun: (id: string) => j<Run>(`/runs/${id}`),
    getEvents: (runId: string) => j<Event[]>(`/runs/${runId}/events`),
    getVerification: (runId: string) =>
        fetch(`${BASE}/runs/${runId}/verification`).then((r) => (r.ok ? (r.json() as Promise<VerificationRun>) : null)),
    listApprovals: (taskId: string) => j<Approval[]>(`/tasks/${taskId}/approvals`),
    approve: (taskId: string, reviewer: string, rationale: string) =>
        j<{ status: string }>(`/tasks/${taskId}/approve`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ reviewer, rationale }),
        }),
    reject: (taskId: string, reviewer: string, rationale: string) =>
        j<{ status: string }>(`/tasks/${taskId}/reject`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ reviewer, rationale }),
        }),
    listReviews: (runId: string) => j<Review[]>(`/runs/${runId}/reviews`),
    getTaskEvents: (taskId: string) => j<Event[]>(`/tasks/${taskId}/events`),
    getDelivery: (taskId: string) => j<DeliveryPreview>(`/tasks/${taskId}/delivery`),
    rollback: (taskId: string, reviewer: string, rationale: string) =>
        j<{ status: string }>(`/tasks/${taskId}/rollback`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ reviewer, rationale }),
        }),
    retryDelivery: (taskId: string) => j<{ status: string }>(`/tasks/${taskId}/deliver`, { method: "POST" }),
    decideEscalation: (taskId: string, decision: EscalationDecision, reviewer: string, rationale: string, guidance: string) =>
        j<{ status: string }>(`/tasks/${taskId}/escalation`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ decision, reviewer, rationale, guidance }),
        }),
    listEvals: (model?: string) =>
        j<EvalResult[]>(`/evals/results${model ? `?model=${encodeURIComponent(model)}` : ""}`),
};