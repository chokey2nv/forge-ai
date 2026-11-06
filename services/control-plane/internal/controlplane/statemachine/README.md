# statemachine

`task.go` is the table of allowed status changes: the most useful single page for seeing how a task can move. `service.go` applies a change, records a `state.transition` event, and uses compare-and-set on the stored status.

`ErrInvalidTransition` means the table doesn't allow the move. `ErrStaleTransition` means another writer moved the task first, which is normal when a person and a background step act at once.
