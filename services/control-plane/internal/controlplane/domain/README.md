# domain

The data types everything else shares, with their BSON and JSON shapes. It imports nothing from the project.

`task.go` holds `Task` and every `TaskStatus`; `run.go` holds `WorkflowRun`, `Checkpoint` and `Event`. Those two explain most of the rest.

When you add a task status, also add it to `statemachine/task.go` and to the console's `api.ts` and `boardColumns.ts`. The console's type check catches a missing column or label.
