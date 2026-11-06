# verification

`gates.go` defines the checks in order, in `DefaultPolicy`: `secrets`, `diff-scope`, `build`, `test`. `runner.go` runs them in the workspace's container and writes the summary an agent gets back.

`RunWithoutTests` is for combined verification when a subtask already passed the tests on the same code.

Traps:

- `gates.go` is not gofmt-clean. Don't run `gofmt -w` on it.
- The gate scripts are shell, so watch quoting. An unquoted `dir/**` scope pattern was once expanded by the shell, and this failed valid files; `TestDiffScope` guards it.
- The test gate has a timeout. On a machine short of memory, a timeout can look like failing tests, so check the exit code in the output.
