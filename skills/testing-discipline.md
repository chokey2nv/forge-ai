---
name: testing-discipline
description: Expectations for tests written or modified by an agent.
applies_to: [implementer, qa-engineer]
---

## Testing discipline

- Every code change must include a test that exercises it, or an explicit justification.
- Test behaviour, not implementation. Avoid asserting on internal function names.
- Cover the boundaries stated in acceptance criteria explicitly, even when the existing suite does not.
- When a test fails, fix the code — not the test. If the test is genuinely wrong, say so in the summary.
- Run the test suite before declaring the task complete.