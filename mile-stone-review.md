# Review of Forge AI — state of the build

This is a status check, not a summary. Three things: what exists, what actually works, and where the risks are.

---

## 1. The full stack, as it stands

**Planes:**

```
Console (React)  ←→  Control Plane (Go)  ←→  Agent Runtime (TS)  ←→  Ollama
                            ↓
                     Mongo (7 collections)
                            ↓
                     Execution Plane (Docker workers)
```

**Collections:** `tasks`, `workflowRuns`, `events`, `verificationRuns`, `approvals`, `evalResults`, `agentRegistry` (snapshots).

**Agents (config-as-data):** `head-engineer`, `planner`, `implementer`, `reviewer`, `qa-engineer` — all in `agents/*.md`.

**Skills:** `generic-coding`, `testing-discipline`, `security-review`, `decomposition-discipline`, `react-architecture`, `go-service-patterns` — all in `skills/*.md`.

**State machine** on `Task`: `CREATED → DECOMPOSING → DISPATCHED → RUNNING → VERIFYING → AGENT_REVIEW → {INTEGRATED | REVIEW_PENDING} → APPROVED`.

**The end-to-end flow that now exists in code:**

```
requirement
  → head-engineer decomposes → task graph (children with assignedAgent, dependsOn, diffScope)
  → scheduler dispatches ready children into isolated worktrees off the integration branch
  → child agent runs (composed prompt + filtered tools)
  → verification gates (secrets, diff-scope, build, test)
  → agent review (reviewer from child's reviewers[0])
  → diff applied to parent's integration branch
  → when all children integrated: combined verification on parent
  → head-engineer's reviewer reviews combined diff
  → REVIEW_PENDING → human approves in console
```

That is the product you described. It's not aspirational anymore — it's written.

---

## 2. What's actually verified vs. what's written

This is the honest part. Let me be precise about it.

| Phase | Written | Verified |
|---|---|---|
| 1 — Skeleton, adapter, smoke test | ✅ | **✅ Confirmed passing** (`qwen3-coder:30b`) |
| 2 — Single-agent loop, containerized | ✅ | ⚠️ Implied working (you proceeded to Phase 3) |
| 3 — Control plane, Mongo, state machine, resume | ✅ | ⚠️ Mongo connection error fixed; demo never confirmed |
| 4 — Verification gates | ✅ | ❌ Not run |
| 5 — Planner/Reviewer (as TS roles) | ✅ | ❌ Not run |
| 6 — Console, evals, approvals | ✅ | ❌ Not run |
| 7 — Agent registry, skills, composer | ✅ | ❌ Not run |
| 8 — Head engineer, task graph, integrator | ✅ (just compiled fixes) | ❌ Not run |

**Only Phase 1 has confirmed end-to-end evidence.** Everything from Phase 3 onwards is architecturally sound code that no one has watched succeed. This matters because:

- Phase 8 had significant compile errors (undefined types, missing methods) — signals the code drifted past what one-shot generation can produce without feedback
- The Phase 8 integration flow has complex failure modes (diff apply conflict, branch mismatch, verification pass/fail propagation across parent and child) that only surface at runtime
- Some of the "concrete" code in Phases 4–8 came out of my sketches with placeholders that needed replacing — you've been finding those one at a time as you compile

**State of confidence:** the architecture is real. The implementation is a *design with a working skeleton*, not a *production system with a working integration*. There's a meaningful distance between those.

---

## 3. The vision vs. what's built

Your stated vision:
> "Agents with job descriptions, skills, etc. Organize work, supervise and review each other. Head engineer splits into specialized task. Within SDLC. Console for approval/feedback."

| Requirement | Status |
|---|---|
| Define agents via config | ✅ Phase 7 — markdown files, hot reload |
| Define skills via config | ✅ Phase 7 — markdown files, composed into prompts |
| Head engineer decomposes | ✅ Phase 8 — `submit_task_graph`, task tree with deps |
| Specialized tasks → specialized agents | ✅ Phase 8 — `assignedAgent` per child |
| Organize work (dependency-aware) | ✅ Phase 8 — scheduler + `dependsOn` |
| **Supervise / review each other** | ⚠️ **Partial** — single reviewer per agent (Phase 8). Cross-review of multiple reviewers is Phase 9. |
| Console for approval | ✅ Phase 6 — Approve/Reject with rationale |
| Console for **feedback** (inline comments on diff) | ❌ Phase 12 — not built |
| Delivery (PR creation, merge) | ❌ Phase 11 — not built |

**Three of your stated requirements are architecturally realized.** Two aren't yet. That's not a failure — you've been building in a sensible order — but it's worth naming so you don't think Phase 8 "delivers supervision" when it delivers *single-reviewer supervision*. Cross-review is what makes it a team; single-reviewer is a pipeline.

---

## 4. The architecture — what's held up, what's strained

### What's held up genuinely well

1. **"Control plane owns truth; agents propose."** Every layer respects this. The composer composes; the adapter executes; verification runs on the platform; approval is a separate actor. This is the invariant that makes the system trustworthy.

2. **Composer as single point of "what model sees."** Phase 7's decision to make agent composition a *single function in Go* (not spread across adapter + roles + tools) is the reason Phase 8 was possible at all. Adding head-engineer didn't touch the adapter — it added a markdown file and an integration handler. That's the payoff.

3. **One workspace per task, git branches as merge mechanism.** Phase 8 could have tried to have agents share a workspace. It didn't. This is why the design is tractable — real merges, real conflicts, real isolation.

4. **Verification always runs and always records evidence.** Not trusting agent claims is what makes the whole thing auditable. The `verificationRuns` collection is a real artifact.

5. **State machine enforces transitions.** Cheap to add, impossible to retrofit. Every phase has benefited from having it early.

### What's strained

1. **The adapter is now genuinely dumb — but the completion protocol is still string-routed.** `handleAgentDone` dispatches by `body.Role` (a string). That's a coupling the registry was meant to remove. It should be routing by *which agent the run's provenance says ran* and calling a handler *registered per agent name*. Doesn't break anything today, but the string-matching is a latent source of "why did agent X get routed as implementer?"

2. **`reviewers: [...]` is an array, but only `[0]` is used.** This is honest Phase 8 scope, but it's a smell — the data model promises more than the runtime delivers. Phase 9 has to resolve this or the registry starts lying about its own capabilities.

3. **The parent verification path is genuinely complex.** Parent has its own worktree, its own container, its own gates, its own reviewer, its own transition to REVIEW_PENDING — but it shares state machine verbs with children. This works but will need careful testing. It's where bugs will hide.

4. **`orchestration.ParentVerificationHook` as a package var** is a compromise for the import cycle. It works, but it's a seam that future contributors will forget exists. Document it, or refactor to an interface.

5. **Phase 7's submission_tool is per-agent in frontmatter, but Phase 8's `handleHeadEngineerDone` still calls `json.Unmarshal` directly.** Any agent that submits a graph gets routed by `body.Role == "head-engineer"` — not by "does the submission contain tasks[]". Fragile to any second agent that wants decomposition (e.g., a `tech-lead`).

---

## 5. What I'd flag before Phase 9

Not blockers to moving forward — flags. Things to know you're carrying.

### A. The testing debt is now the biggest risk

You said early on "I want to build this completely, understand the architecture, then take my time to start testing phase by phase." That was a reasonable plan. But at Phase 8, the untested code is ~3,000 lines across two languages. When you do start testing, you'll be testing *eight phases at once*, and a failure in Phase 8 might be caused by a Phase 4 bug that only manifests when children hit verification.

**Recommendation:** don't test everything phase-by-phase starting from Phase 1. Test **vertical slices**. The single most valuable test right now:

```
Give the system a requirement that needs 2 agents.
Watch: DECOMPOSING → DISPATCHED → children dispatched → INTEGRATED
       → parent INTEGRATING → combined VERIFYING → REVIEW_PENDING
```

That path exercises:
- Registry (Phase 7)
- Composer (Phase 7)
- Head engineer decomposition (Phase 8)
- Scheduler (Phase 8)
- Child dispatch + execution (Phase 2)
- Verification gates (Phase 4)
- Reviewer (Phase 5)
- Integrator (Phase 8)
- Parent verification (Phase 8)
- Console (Phase 6)

**One test** proves eight phases. If it fails at any step, you find the phase to debug. If you test phases 4, 5, 6, 7, 8 independently you'll test twenty things and still not know if the whole pipeline works.

### B. The string-routing in `handleAgentDone` needs to be turned into data

Before Phase 9 (which adds cross-review), consider one small refactor:

```go
// Register per-agent completion handlers in an init() or main():
h.AgentCompletions["head-engineer"] = h.handleHeadEngineerDone
h.AgentCompletions["planner"]       = h.handlePlannerDone
h.AgentCompletions["reviewer"]      = h.handleReviewerDone
// default: h.handleImplementerDone

// In handleAgentDone:
if fn, ok := h.AgentCompletions[agentNameOf(run)]; ok {
	fn(run, task, body)
} else {
	h.handleImplementerDone(run, task, body)
}
```

The registry tells you *who* ran. The map tells you *what to do* when they finish. This closes a coupling that Phase 9 will otherwise inherit.

### C. `reviewers: [...]` — either honor it fully in Phase 9, or note it as single-reviewer-only in the docs

The data model says "array." Phase 8 uses `[0]`. That's fine for now, but be explicit. Consider (in Phase 9):

```go
// All reviewers must pass; any rejection triggers retry with consolidated defects.
for _, rName := range def.Reviewers {
    // dispatch reviewer, collect verdict
}
```

If you don't get to Phase 9 quickly, at least add a comment on `reviewers` in the loader: `// Phase 8: only [0] is used; Phase 9 iterates.` Prevent future confusion.

### D. The "add an agent" test needs to happen before you build more

The defining Phase 7 exit criterion was: **"Adding a new agent is a markdown file and a reload. No Go edits, no TypeScript edits, no rebuild."**

That has never been run. It's the single best proof that the architecture matches your vision. Before Phase 9, take 15 minutes:

```
1. touch agents/test-agent.md
2. fill in frontmatter + a prompt
3. POST /agents/reload
4. GET /agents/test-agent   → should return the definition
5. Delete file, reload, GET → 404
```

If that works, Phase 7 is real. If it doesn't, everything built on it is suspect.

### E. What "production-grade" honestly means here

You said "production grade." The architecture is production-shaped. The implementation is:
- **Correct** in the invariants (isolation, truth ownership, audit, provenance)
- **Incomplete** in operations (no auth, no rate limits, no multi-tenant concerns)
- **Untested** in the composite flows

Two of these are fine to defer (operations belong in Phase 13). The third — testing — is the one that makes the other two matter.

---

## 6. Where this leaves the vision

You described a harness where you define a team of agents, and they organize, execute, supervise, and deliver software, with you approving at decision points.

**What exists:** the harness defines agents as data. They decompose work through a head engineer. They execute in isolation. They verify their own and each other's work (single reviewer per agent). They integrate through git. They surface decisions to you.

**What remains for the vision to be complete:**

| Feature | Phase |
|---|---|
| Multiple reviewers, cross-review, escalation | 9 |
| Truly parallel child execution | 10 |
| Delivery (PR creation, merge, rollback) | 11 |
| Human feedback loop — inline comments, threaded responses | 12 |
| Hardening — auth, sandbox, multi-tenant | 13 |

That's the honest roadmap. Five phases of substantial work remaining, plus testing across all thirteen.

---

## 7. What I'd do next (concrete recommendation)

You said you want to understand the architecture fully, then test.

**Step 1 — Do the "add an agent" test** (15 min). It proves the architecture's central promise.

**Step 2 — Do one vertical slice test** (~1 hour). Give the system a 2-agent requirement and watch the full pipeline. Everything from Phase 2 through Phase 8 gets exercised. Whatever breaks, fix, repeat.

**Step 3 — Write down what the slice test looked like** in `docs/operations/RUNBOOK.md` under a new section: "End-to-end slice test." It becomes the regression test for Phase 9 onwards.

**Step 4 — Then Phase 9.** With the pipeline working end-to-end, cross-review is a natural extension. Without it working, cross-review is adding complexity to an unverified base.

---

## The one-paragraph honest verdict

You have a genuinely well-architected system whose core invariants — control plane owns truth, agents are data, verification is authoritative, isolation is real, provenance is recorded — are correct and rarely thought through this carefully. You have *written* eight phases' worth of implementation against those invariants. You have *verified* one phase. The distance between "written" and "verified" is the distance between a design document and a product, and it's the distance you now have to close. The best path through that distance is not "test phase by phase" — it's "test one vertical slice, then another, then another." Each slice tests multiple phases simultaneously and finds real bugs instead of theoretical ones. Phase 8 is the point where the architecture is complete enough that a vertical slice *can* run through it. That's a real milestone. Use it.