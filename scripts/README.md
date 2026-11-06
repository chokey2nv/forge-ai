# Scripts

- `forge.sh`: start, stop and inspect a whole local Forge. This is the normal way in. `Start Forge.command` and `Stop Forge.command` in the root call it.
- `phase8-failure-paths.sh`: the end-to-end regression suite. Real pipeline, Docker and git, with the scripted model, so it costs nothing. Its header lists every scenario; pass names to run a few.
- `phase10-parallel-slice.sh`, `phase11-delivery-slice.sh`, `phase12-feedback-slice.sh`, `phase8-vertical-slice-demo.sh`, `phase8-two-child-slice.sh`: **real model, real money.** They use `lib/` for API tokens and a project.
- `phase3-*.sh`, `phase4-*.sh`, `phase5-demo.sh`: early demos, no longer re-checked and written for port 8080.
- `check-docs.sh`: `make docs-check`.

## A test instance beside your running Forge

Use its own database (the reaper only touches its own), its own ports, and `127.0.0.1` so its login doesn't replace yours. The harness does this with ports 8090 and 8091 and the database `forge_ai_failpaths`. By hand, set `MONGODB_DATABASE`, `CONTROL_PLANE_ADDR`, `CONTROL_PLANE_SELF_URL`, `ADAPTER_URL`, `FORGE_CONSOLE_ORIGINS`, `MODEL_PROVIDER=scripted`, `FORGE_ALLOWED_PROVIDERS=scripted` and a `FORGE_BOOTSTRAP_TOKEN` to create the first user.

macOS has bash 3.2, so these scripts avoid newer bash features. Harness scenarios can flake when the machine is short of memory; rerun one alone before suspecting the code.
