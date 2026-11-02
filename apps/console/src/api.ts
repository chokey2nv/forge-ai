const BASE = import.meta.env.VITE_API_URL ?? "http://localhost:8080";

export type TaskStatus =
  | "CREATED" | "PLANNED" | "DECOMPOSING" | "DISPATCHED" | "RUNNING" | "VERIFYING"
  | "AGENT_REVIEW" | "ESCALATED" | "REVIEW_PENDING" | "INTEGRATING" | "INTEGRATED"
  | "APPROVED" | "MERGED" | "BLOCKED" | "RESUMABLE" | "REJECTED";

export const TASK_STATUSES: TaskStatus[] = [
  "CREATED", "DECOMPOSING", "DISPATCHED", "RUNNING", "VERIFYING", "AGENT_REVIEW", "ESCALATED",
  "INTEGRATING", "INTEGRATED", "REVIEW_PENDING", "APPROVED", "BLOCKED", "RESUMABLE", "REJECTED",
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

async function j<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(BASE + path, init);
  if (!res.ok) throw new Error(`${path}: HTTP ${res.status} — ${await res.text().catch(() => "")}`);
  return res.json();
}

export const api = {
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
    decideEscalation: (taskId: string, decision: EscalationDecision, reviewer: string, rationale: string, guidance: string) =>
        j<{ status: string }>(`/tasks/${taskId}/escalation`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ decision, reviewer, rationale, guidance }),
        }),
    listEvals: (model?: string) =>
        j<EvalResult[]>(`/evals/results${model ? `?model=${encodeURIComponent(model)}` : ""}`),
};