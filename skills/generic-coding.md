---
name: generic-coding
description: Baseline expectations for any code written by any agent.
applies_to: [head-engineer, planner, implementer, reviewer, qa-engineer]
---

## Generic coding discipline

- Read the code you are about to change before changing it. Use the read/search tools.
- Make the smallest change that satisfies the task. Do not refactor unrelated code.
- Match the existing style of the codebase — indentation, naming, error handling.
- Prefer applying a diff (`apply_patch`) over rewriting whole files (`write_file`).
- Do not leave commented-out code, debug prints, or TODOs behind.
- If you are uncertain about intent, state the ambiguity in your final message
  rather than guessing silently.
- Do not touch `.git`, `.env`, secrets, or CI configuration unless the task
  explicitly requires it.