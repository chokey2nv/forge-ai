---
name: decomposition-discipline
description: Rules for splitting a requirement into independently verifiable subtasks.
applies_to: [head-engineer, planner]
---

## Decomposition discipline

- A subtask is "independently verifiable" if its acceptance criteria can be checked without the other subtasks being complete.
- Independent tasks (no `dependsOn`) must not modify the same files. If they would, they need a dependency edge instead.
- Dependency edges mean "cannot start until the dependency integrates" — not "related to."
- Prefer 2–4 subtasks. More than 6 is a red flag.
- Each subtask is one agent's job. If you cannot name a single agent, the subtask is too broad.
- Rationale is not optional in spirit: for every subtask, write one sentence explaining why it is separate.