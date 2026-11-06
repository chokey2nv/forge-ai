# Commands

- [`forge/`](forge/): the control plane server. It is also the admin command: `forge users …` and `forge tokens …` work directly on the database with no server running (`admin.go`). Every environment setting and its default is read in `main.go`.
- [`eval/`](eval/): runs the cases in `tests/evaluations/` through a running Forge, then checks each result independently of Forge's own checks and reports to `POST /evals/results`. **It spends real money with a paid model.**
