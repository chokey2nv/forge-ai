# orchestration

- `scheduler.go`: a tick loop over dispatched parents. Starts ready subtasks up to `MAX_CONCURRENT_RUNS`, oldest parent first so one wide graph can't starve another; blocks a parent if a subtask is blocked or rejected; starts combined verification once all are integrated.
- `integrator.go`: commits a reviewed subtask on its branch and merges it into the parent branch, one at a time per parent.
- `hooks.go`: lets `api` give this package its parent-verification function, since importing `api` here would be a cycle.

Provisioning can take far longer than a tick, so a subtask is claimed first and its workspace is built in the background.
