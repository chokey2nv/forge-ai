# workspace

Everything that touches Docker and worktrees on the host.

| File | Job |
|---|---|
| `provision.go` | Create a workspace, apply a saved diff, clean up |
| `integration.go` | A parent's worktree, and a subtask's off it |
| `sandbox.go` | The `docker run` flags every container gets |
| `deps.go` | Shared read-only package volumes |
| `projects.go` | Finds the projects in a repository, including in subfolders |
| `branches.go` | Commit a diff on a branch; merge into a worktree |
| `containers.go`, `clone.go` | List and remove managed containers; workspace directories |

Traps:

- `Instance` is set to the database name and labels everything Forge creates. The reaper only touches matching labels, so a test instance **must** use its own `MONGODB_DATABASE`.
- Containers have no network. Packages come from `deps.go`, which fetches with its project's own scripts removed. A repository where packages can't be shared (a monorepo, a local dependency) installs in each workspace instead.
- Resume needs a new container name each time, or a second resume deletes its own container.
