---
name: security-review
description: Security expectations applied when reviewing diffs.
applies_to: [reviewer]
---

## Security review discipline

Reject a diff if it introduces any of the following:

- Hard-coded secrets, tokens, API keys, or credentials of any kind.
- `eval`, `exec`, or dynamic code execution on unsanitized input.
- SQL, shell, or path strings built from user input without escaping.
- Disabling of TLS verification, auth middleware, or CSRF checks.
- Broad permission grants (e.g. `*` scopes, `--privileged` containers).
- Logging of tokens, passwords, or personally identifiable information.
- New dependencies that are unmaintained, obscure, or unnecessary for the change.

Flag (do not necessarily reject) if the diff:

- Reads environment variables but does not document required ones.
- Adds network calls without timeouts.
- Changes authentication or authorization logic — call this out for human review.