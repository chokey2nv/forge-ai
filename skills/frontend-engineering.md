---
name: frontend-engineering
description: How production React front ends are structured - layering, state, services, styling, Ant Design and failure handling.
applies_to: [frontend-engineer]
---

## First: the codebase's own rules win

Read the project's README, one finished feature end to end, and its theme and shared components before writing anything. Where this skill and an established pattern in the repository differ, follow the repository and say so in your summary. Never add a second way of doing something the codebase already does. Do not add a dependency unless the task asks for one.

## Layering

Dependencies point one way. A layer may import from the ones below it, never above.

| Layer | Owns | Must not |
|---|---|---|
| Route and page | Which feature shows, URL parameters, page-level error boundary | Fetch, hold business rules |
| Container or feature hook | Wiring data and actions to views | Render markup of its own beyond composition |
| View component | Markup, styling, local UI state | Fetch, read stores it doesn't own, know endpoints |
| Domain logic (`*.logic.ts`) | Pure rules: calculations, filtering, validation, status transitions | Import React, the store or the network |
| Integration (query hooks, API modules) | Cache keys, requests, mapping responses to domain types | Import components, show messages |
| Transport (HTTP client) | Base URL, auth headers, timeouts, turning failures into one error type | Know any feature |

A feature keeps its parts together (`features/<name>/` with `components/`, `hooks/`, logic and types). It uses other features only through what they export, and code moves to a shared folder once a second feature needs it, not before.

## Logic and view split

- A view takes data and callbacks as props and can render in a test or story with no provider beyond the theme.
- If a branch or calculation is worth testing, it belongs in a pure function, not in JSX or an effect.
- A hook that returns data, state and handlers is the seam: `const vm = useCollection(); return <CollectionView {...vm} />`.
- Split a component when it has two reasons to change, not because of its line count. Don't pass a prop through more than two levels that don't use it; compose with `children` or read it where it's needed.

## Integration layer

- No component calls `fetch`, `axios` or an API module directly. Components use hooks; hooks use API modules; API modules use the one HTTP client.
- API modules map responses to domain types, so a backend rename changes one file. Validate or narrow untrusted data at this boundary.
- All errors reach the UI as one normalized type, with a message safe to show and a code to branch on.
- Query keys live in one place. A mutation invalidates or updates exactly the keys it affects. Requests are cancellable: pass the `AbortSignal` through.
- Mocks replace the transport or service boundary, so the code above them is the code that ships.

## Where state lives

Choose the first that fits, and never copy one into another:

1. Derivable from other state: compute it during render.
2. Server data: the query cache, not a store and not `useState`.
3. Should survive a reload or be shareable: the URL.
4. One component or its children: `useState` or `useReducer`.
5. Form input: the form library.
6. Client state used across distant components: a store.

### Zustand

- One store per concern, with its actions inside it. Components call actions; they don't `setState` from outside.
- Always select: `useStore((s) => s.count)`. Selecting the whole store re-renders on every change.
- A selector that builds a new object or array needs `useShallow`; without it, Zustand v5 can loop forever.
- Business rules go in logic functions the actions call. Stores don't import components, and don't hold server data.

## Hooks

- Follow the rules of hooks and keep the dependency arrays honest. If a dependency array is awkward, the code's shape is wrong; don't silence the lint rule.
- `useEffect` is for synchronizing with something outside React. Don't use it to derive state, respond to a click (use the handler), or reset state on a prop change (use `key`).
- Every effect that subscribes, times or requests cleans up after itself, and can't set state after it's stale.
- `useMemo` and `useCallback` are for a measured cost or a memoized child's stable prop, not by default.
- Name custom hooks for what they provide, and return a stable, small shape.

## styled-components

- Every color, space, radius, font and shadow comes from the theme. No hard-coded values when a token exists.
- Define styled components at module level, never inside a render.
- Use transient props (`$active`) for styling props, so they don't reach the DOM.
- Prefer a variant prop to a pile of booleans. Keep logic out of the template: compute the value first.
- Wrap third-party components with `styled(Button)`; don't reach into their internals.

## Ant Design

- Theme it through `ConfigProvider` tokens. Overriding `.ant-*` classes is a last resort, scoped, with a comment saying why.
- Get `message`, `notification` and `modal` from `App.useApp()`: the static functions can't see the theme or context.
- Use `Form` with `Form.useForm`, declared `rules` and `name`d fields; don't mirror its values in `useState`.
- Every `Table` has a stable `rowKey`. Keep `columns` outside the component or memoized, and paginate or virtualize long lists.
- Use the design system's components before building your own, and import what the repository already wraps from its wrapper.

## Failure and async states

- Every data view handles loading, empty, error and success, and a mutation handles pending and failure. Disable a submit while pending, so it can't fire twice.
- Error boundaries catch render errors only: not event handlers, async code or query errors. Those you handle where they happen and show inline.
- Put a boundary at the app root and around each route or independent panel, so one failure doesn't blank the page. Give it a way to retry, and reset it on navigation. Report what it catches.
- Never swallow an error. Show the user what happened and what they can do; log the detail.

## Quality you can't skip

- **Types:** model states so impossible ones can't be written (discriminated unions). No `any` or `as` to silence the compiler; narrow at the boundary. Exhaustive `switch` on status.
- **Accessibility:** semantic elements first. Everything works by keyboard, inputs have labels, icon buttons have an `aria-label`, focus moves sensibly in drawers and modals, and color is never the only signal.
- **Performance:** fix what's measured. Stable keys (never the index for a list that changes), narrow selectors, state held as low as it can be, and lazy-loaded routes.
- **Security:** no `dangerouslySetInnerHTML` on anything a user or server supplied, no secrets in client code, and client-side checks are never the only checks.
- **Tests:** test behaviour through what a user sees, with Testing Library roles and text rather than class names. Pure logic gets fast unit tests, and each screen gets a test through its real route and hooks with the network mocked. Wait with `findBy` rather than fixed delays.

## Working in this environment

- There's no browser: you can't see the result. Say so in your summary, and list what a person should look at.
- There's no network, so a new package can't be installed.
- The project may be in a subfolder: find its `package.json`, and use its package manager (`pnpm -C app test`). A lockfile tells you which.
- A full suite can take minutes and your steps are limited. Run the tests for what you touched (`pnpm -C app exec vitest run src/features/x`) and the type check (`pnpm -C app exec tsc --noEmit`); the platform runs the whole suite afterwards.
