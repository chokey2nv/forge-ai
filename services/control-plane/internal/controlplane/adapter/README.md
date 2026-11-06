# adapter

The control plane's HTTP client for the runtime adapter: start a run, cancel a run. `client.go` asks the budget gate before starting, so an over-cap task never makes a model call.
