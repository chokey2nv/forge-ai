# runner

Background loops started by `main.go`.

- `watcher.go`: a run with no heartbeat for `STALE_AFTER` becomes `RESUMABLE`. Also settles reviewers that never reported, as errors, so a round can't hang.
- `reaper.go`: removes containers, workspaces and unused package volumes nothing can use any more, only ones labelled with this instance's database.
