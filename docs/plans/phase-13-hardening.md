# Phase 13 — Hardening: auth, sandbox, projects

**Status:** 13.1 done (4 Nov 2026) · **Drafted:** 4 Nov 2026 · **Builds on:** Phase 12 (`docs/plans/phase-12-feedback.md`)

## Goal

Forge can run on a machine other people can reach without anyone being able to drive it who should not. Every human action is done by a signed-in person with the right role, and recorded as theirs. The services trust only each other. Work stays inside the repositories a project has registered. Code an agent writes cannot reach the network or the host, and a task cannot spend without limit.

## Where it starts

| Area | Today |
|---|---|
| Network | The control plane listens on `:8080` and the adapter on `:8081`, on every interface |
| Control plane API | No authentication. Anyone who can reach it can create tasks, approve and merge, roll back, reload agents. CORS allows any origin |
| Adapter | No authentication. `POST /runs` runs a model with the operator's API key and executes tools in whichever container it names, then posts results to whichever `callbackUrl` the request gives |
| Agent callbacks | `POST /runs/{id}/checkpoint` and `/complete` accept anything for any run ID, so a forged completion could push a diff into review |
| Human identity | Free text: `reviewer`, `author`. "Human" in threads means "not an agent's name" (Phase 12.2) |
| Repositories | A task's `repoPath` is any path on the control plane's machine; Forge branches, mounts and merges there |
| Tenancy | One shared space: every task, thread and approval is visible to everyone |
| Worker containers | `--network=none`, memory/CPU/PID limits; but root, all default capabilities, writable root filesystem |
| Dependency install | `npm install` with network, running the repository's install scripts. After the first subtask the repository includes agent-written code, so an agent can add a `postinstall` that runs with network on the next provision |
| Spend | Budgets cap steps, retries and wall time per run; nothing caps tokens or cost per task or project. Usage is recorded per checkpoint |
| Operations | A task in `DELIVERING` when the control plane restarts is not resumed (Phase 11 gap); one control plane per database |

## Decisions

1. **Local accounts first.** Users are kept in a `users` collection with argon2id password hashes and created with `forge users add` (the first user is an admin). The console signs in with a session cookie (`HttpOnly`, `SameSite=Strict`, server-side session, sliding expiry). Scripts and the harness use personal API tokens (`Authorization: Bearer`, stored hashed, shown once, revocable).
   - *Alternative:* sign in with GitHub (OIDC). It's a natural next step since delivery already uses `gh`, but it needs a reachable callback URL and an OAuth app. It is left for later behind the same session model.
2. **Projects are the tenancy unit.** A project is a set of registered repositories (path, default target branch) and its members. Every task belongs to one project, and its repository must be one of the project's. There's one deployment and one database; there are no separate organisations or databases per tenant.
3. **Roles per project:** `viewer` (read), `member` (create and start tasks, comment), `reviewer` (approve, reject, request changes, decide escalations, roll back, retry delivery, resolve threads), `admin` (manage the project, its repositories and members). Instance admins manage users, agents and skills (reload), and projects.
4. **Actions are attributed from the session, not the request.** `reviewer` and `author` in request bodies are ignored, and the signed-in user is recorded in approvals, threads, follow-ups and events. Agents never hold user credentials: their only way in is the callback token of a run (decision 5). The Phase 12 rule "agents cannot resolve" therefore becomes structural, and the name check goes.
5. **Services authenticate each other.** The control plane and adapter share a secret (`FORGE_ADAPTER_TOKEN`) that the adapter requires on `/runs` and `/runs/{id}/cancel`. Each run gets a callback token, an HMAC of its run ID under a control-plane key (`FORGE_CALLBACK_SECRET`, or a random key kept in Mongo), so retries, resumes and reviewers of the run present the same one and nothing per run is stored. Checkpoint and completion callbacks must present it, and once the run is `COMPLETE` they are refused. The adapter posts only to its configured control plane URL and ignores `callbackUrl` in requests.
6. **Listen on localhost by default.** Both services bind `127.0.0.1` unless `CONTROL_PLANE_ADDR` / `ADAPTER_ADDR` say otherwise. CORS allows only `FORGE_CONSOLE_ORIGINS`. Exposing Forge beyond the machine is done behind a TLS-terminating reverse proxy (documented, not built).
7. **Containers run unprivileged.** Worker, verification and setup containers run as a non-root user with `--cap-drop=ALL`, `--security-opt=no-new-privileges` and a read-only root filesystem (writable `/workspace`, `tmpfs` for `/tmp` and caches). Dependency install fetches packages with network but runs no scripts (`npm ci`/`npm install --ignore-scripts`). Install scripts then run in a second step with `--network=none`.
8. **Spend caps.** A task and a project can cap model tokens (input + output) and, where the provider's prices are known, estimated cost. The adapter already reports usage per step. When a cap is reached the run stops and the task is `BLOCKED` with the reason; a reviewer can raise the cap and retry.
9. **No "auth off" switch.** Local scripts, the harness and the slices use a bootstrap admin token from `FORGE_BOOTSTRAP_TOKEN`, which is accepted only when no user exists yet, or from a token created with `forge tokens create`. The suite runs with authentication on.

## Design

### Data

```
User      { id, name, email, passwordHash, instanceAdmin, createdAt, disabledAt? }
Session   { id (hashed), userId, createdAt, lastSeenAt, expiresAt }
APIToken  { id, userId, name, hash, createdAt, lastUsedAt, revokedAt? }
Project   { id, name, repos: [{path, defaultTarget}], members: [{userId, role}],
            budget: {maxRevisions, maxTokens, maxCostUSD}, createdAt }
Task      + projectId, createdBy
Approval / Thread comment / FollowUp / Event: actor = userId (+ name at the time)
```

### Request flow

```
console ──cookie──▶ ┐
script  ──Bearer──▶ ├─ authenticate ─▶ user ─▶ load task/project ─▶ role check ─▶ handler (actor = user)
                    ┘
adapter ──run token──▶ /runs/{id}/checkpoint|complete ─▶ token matches the run, run not finished
control plane ──FORGE_ADAPTER_TOKEN──▶ adapter /runs
```

Resources outside a user's projects answer 404, not 403, so their IDs are not confirmed.

### Console

A sign-in page. The header shows the signed-in user and a project switcher. The New task form picks a repository from the project's list instead of typing a path. Reviewer name fields go away. Project admins get a members page and a repositories page.

## Increments

| # | Scope | Exit check |
|---|---|---|
| 13.1 ✅ | Service trust and network: localhost binds, CORS allow-list, the adapter's shared secret, per-run callback tokens, adapter posts only to its configured control plane | Unit tests: run tokens differ per run and key; a callback without, with another run's, or with a wrongly keyed token is 401, an unknown run 404, a `COMPLETE` run 409; no key refuses everything; CORS answers only allowed origins (preflights from others 403); loopback detection; the adapter client sends the secret and the callback run's token, and a 401 is an error. Scripted: every scenario runs with the tokens; `verify-retry` checks forged callbacks (401, 409), the adapter's 401 without its secret, loopback listeners and CORS |
| 13.2 | Identity: users, sessions, API tokens, `forge users add` / `forge tokens create`, bootstrap token; every route authenticated except `/health` and run callbacks; console sign-in | A table-driven test over every registered route: unauthenticated is 401. Session expiry and token revocation. The harness and slices run with a token. Console: sign in, sign out, an expired session goes back to sign-in |
| 13.3 | Roles and attribution: per-route role rules, actor from the session in approvals, threads, follow-ups and events, the Phase 12 name check replaced | A role matrix test (viewer, member, reviewer, admin × each action). The harness approves with a reviewer token and is refused with a member token. Approvals and threads show the signed-in user |
| 13.4 | Projects: collection, registered repositories and members, tasks scoped to a project, listings filtered, migration of existing tasks into projects made from their repositories, console project switcher and repository picker | Tests: a user of project A gets 404 for project B's tasks, threads, diffs and approvals; a task on an unregistered path is refused; the migration is idempotent. Console: create a project, add a repository and a member, create a task from the picker |
| 13.5 | Sandbox: non-root, no capabilities, `no-new-privileges`, read-only root filesystem, install scripts run with no network | Scripted scenario `sandbox-postinstall`: a subtask adds a `postinstall` that tries to reach the network and write outside `/workspace`; the next subtask's provisioning runs it with no network and a read-only root, so the harness's marker files never appear. `docker inspect` shows the flags. All scripted scenarios still pass |
| 13.6 | Spend and operations: token and cost caps per task and project; resume `DELIVERING` after a restart; runbook (deployment behind a proxy, users, projects, tokens) | Scripted: `budget-tokens` (the cap stops a run → `BLOCKED` with the reason, raise the cap and retry → done), `restart-mid-delivery` (kill the control plane while `DELIVERING`; on restart the delivery resumes or blocks with a clear reason, never left hanging). Every Claude slice passes with authentication on |

Increments 13.1–13.5 are checked with unit tests and the scripted harness only (no model API cost). The real-model checks, every Claude slice and a console run, happen once, in 13.6.

## Risks

- **Locking out the harness and slices.** Every script calls the API. Mitigation: 13.1 and 13.2 update the scripts in the same change, and the bootstrap token keeps local runs one environment variable away.
- **Read-only containers breaking toolchains** that write to `$HOME` or global caches. Mitigation: `tmpfs` for `/tmp`, `HOME` and cache directories, and a per-project override for images that need more, recorded in the run.
- **Install scripts without network.** Packages that download binaries in `postinstall` (browsers, native builds) fail. Mitigation: the failure names the package. A project can allow network for its install step explicitly; the flag is visible in the console and recorded on the run.
- **Migrating free-text names.** Existing approvals and threads keep their names as text and are shown as "(before sign-in)". They are not mapped to users.

## Out of scope

Sign-in with GitHub or another OIDC provider, separate organisations or databases per tenant, TLS termination in Forge itself, several control planes per database (needs a distributed lock), encryption of repository contents at rest, per-user model API keys, audit-log export.
