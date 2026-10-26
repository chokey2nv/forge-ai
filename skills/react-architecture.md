---
name: react-architecture
description: Enforces presentation/integration separation in React codebases.
applies_to: [implementer, qa-engineer]
---

## React architecture rules

- Presentational components own UI rendering only. They receive data and callbacks as props.
- Hooks or container components own integration — data fetching, side effects, orchestration.
- API clients own transport-level concerns (URLs, HTTP, error mapping). No component calls `fetch` directly.
- Domain logic must not depend on React.
- Do not pass data fetching down through presentational components.

Violations are architectural defects, not style nits.