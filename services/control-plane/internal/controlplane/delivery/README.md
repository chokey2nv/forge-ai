# delivery

Git and `gh` operations for getting work to its target. It knows nothing about tasks' statuses; `api/deliver.go` and `api/rollback.go` drive it.

- `branch.go`: branch names and creation. `preview.go`: what approving will do, shown before a task exists. `inspect.go`: a repository's branches.
- `deliver.go`: merge locally, push, open and read pull requests. `remote.go`: fetch the target. `rollback.go`: reverts.

Every step can be repeated, because a restart resumes deliveries left in `DELIVERING`. A local merge refuses a dirty target checkout rather than touching someone's work. `FORGE_GH` swaps out `gh`; the harness uses a stand-in so tests never act as your account.
