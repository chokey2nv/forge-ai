package orchestration

import "github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"

// ParentVerificationHook is set by api.Handlers at startup. Runs the parent's
// combined verification and agent review. Defined as a package var to avoid
// the import cycle orchestration → api.
var ParentVerificationHook func(parent *domain.Task) error
