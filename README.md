# Forge AI

An AI software-engineering team. Give it a requirement and acceptance criteria. A head engineer splits the work into subtasks, specialist agents do them in isolated Docker containers, automated checks and reviewer agents test each change, and a person approves the result before it's merged.

**Stack:** Go control plane · TypeScript runtime adapter · React console · MongoDB · Claude, OpenAI or Ollama.

**Status:** Phases 1–13 are built. The [runbook](docs/operations/RUNBOOK.md#9-phase-status) lists the evidence for each.

## Run it

```bash
scripts/forge.sh up
```

This starts everything and opens the console. First-time setup is in the [runbook](docs/operations/RUNBOOK.md#2-one-time-setup).

## Read more

- [`CLAUDE.md`](CLAUDE.md): layout, rules, where to change things. Start here.
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md): how it works.
- [`docs/README.md`](docs/README.md): everything else.
