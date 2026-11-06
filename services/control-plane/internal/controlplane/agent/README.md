# agent

Turns the markdown in `agents/` and `skills/` into what the model sees. `loader.go` parses and validates, `registry.go` holds the current set, `compose.go` builds the final prompt and tools, and `tools.go` is the catalog of every tool an agent may use.

An agent file:

```
---
name: implementer            # must match the filename
description: One line, shown to the head engineer
version: 1
model_preference: strong
submission_tool: submit_review   # optional; see below
skills: [generic-coding]
tools: [read_file, write_file]
reviewers: [reviewer]        # who must pass its work
---
The system prompt.
```

`submission_tool` decides what finishing means: `submit_task_graph` (decompose), `submit_plan` (plan), `submit_review` (review), or none (it edits code). Agents with none are the ones a head engineer can assign subtasks to.

A skill file has `name`, `description`, `applies_to` and a body that is added to the prompt of agents listing it.

Traps:

- **Every `.md` in `agents/` and `skills/` is loaded,** so don't put a README there. `repo_agents_test.go` checks the real files load.
- **A new tool needs two edits:** the catalog in `tools.go`, and an executor in the adapter's `executor.ts`.
- Reload a changed agent with `POST /agents/reload` or a restart.
