# Tests

- `failure-paths/`: canned model replies for `scripts/phase8-failure-paths.sh`. Each JSON maps an agent's name to its turns, and the scripted provider replays them. To add a scenario, add a JSON here, then its steps and checks in the script, and its name to the script's header and default list.
- `evaluations/`: scored cases for `make eval`, which tests how well the agents do real work. Each JSON names a fixture, a requirement, acceptance criteria and independent `checks`. **It spends real money with a paid model.**
- `evaluations/fixtures/`: small repositories those cases and the harness work on.

Unit tests live beside the code they test.
