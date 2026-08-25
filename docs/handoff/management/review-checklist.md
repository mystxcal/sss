# Review Checklist

## Behavior

- Does the code implement the assigned outcome?
- Is public behavior consistent with contracts?
- Are failure modes explicit?
- Does evidence use the real network and filesystem path?

## Lifecycle

- Are state transitions centralized?
- Is the operation idempotent?
- What happens on process death before and after every durable write?
- Can a code ever resolve early?
- Can cleanup race active use?

## I/O and resources

- Is I/O streaming?
- Is concurrency bounded?
- Are files closed?
- Are temporary paths cleaned?
- Is cancellation propagated?
- Is disk admission checked?

## Paths and archives

- Are absolute, parent, duplicate, and platform-invalid paths handled?
- Are symlinks and special files rejected?
- Can extraction escape the root?
- Does Windows behavior have native tests?

## Authentication

- Is public auth enforced?
- Is the Unix route inaccessible publicly?
- Are secrets redacted?
- Are errors stable?

## Tests

- Unit tests for rules?
- Real repository integration?
- Black-box route?
- Negative and fault case?
- Would the test fail if implementation were stubbed?
- Observed results attached?

## Code quality

- Small coherent change?
- No duplicate protocol or state logic?
- Useful errors?
- No debug bypass or production TODO?
- Docs updated?
- Formatting, lint, and race checks clean?

## Verdict

Use one:

- Accept
- Accept with follow-up
- Changes required
- Reject and rework

State the highest-risk issue first.
