# Console

The React app people use. It has no router or UI library: pages are chosen from the address hash (`#/task/<id>`), and styles are one file.

| File | Job |
|---|---|
| `src/App.tsx` | Top bar, routing, the task page, New task, Project, Evals |
| `src/Board.tsx` | The Kanban board and the Start dialog |
| `src/boardColumns.ts` | Pure rules: which column a task is in, what a drop means. Tested. |
| `src/Team.tsx`, `src/agentRoles.ts` | The Team page, and the pure rules for an agent's role and who reviews what. Tested. |
| `src/DiffView.tsx`, `src/diff.ts` | The change viewer and line comments |
| `src/api.ts` | Every API call and the shared types |
| `src/styles.css` | All styles; colours are variables on `:root` |

Traps:

- `Board.tsx` and `boardColumns.ts` have different names on purpose: macOS ignores case, so a lowercase file named after the component collides with it.
- Run `tsc` with `--noEmit`. A plain `tsc -b` writes `.js` files into `src/`, and Vite then loads those instead of the sources.
- The Changes tab is hidden, not removed, when you switch tabs, so a half-written comment survives.
- `VITE_API_URL` picks the control plane; `forge.sh` sets it.
- Sessions are cookies per host, so a test login on `localhost` signs you out of your real console, whatever the port. Use `127.0.0.1` for test instances.

Test: `npm test`, and `npx tsc --noEmit -p .` for types.
