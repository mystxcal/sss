# AGENTS.md — Rules for All Coding Agents

## Objective

Ship working SSS software that satisfies the handoff contracts. Planning, scaffolding, and prose are supporting work, not completion.

## Before changing code

1. Read the assigned work order.
2. Read the relevant frozen decisions and contracts.
3. Inspect existing implementation and tests.
4. State the invariant your change must preserve.
5. Identify the command that will prove completion.

## While working

- Keep scope narrow.
- Prefer a working vertical outcome over speculative abstractions.
- Do not change public behavior casually.
- Never duplicate lifecycle logic in handlers, CLI code, and repositories.
- Keep transfer I/O streaming and bounded.
- Use injectable clocks and IDs for deterministic tests.
- Treat filesystem and archive paths as untrusted.
- Preserve Windows and Linux behavior.
- Do not log credentials or raw Authorization headers.
- Add tests with every behavior change.
- Remove temporary code and debug paths before handoff.

## Required handoff from a subagent

Provide:

- concise summary;
- changed files;
- behavior added or corrected;
- tests run and exact results;
- evidence paths;
- known risks;
- integration notes;
- any decision deviation.

Do not say “done” when tests were not run. Do not describe expected output as observed output.

## Prohibited completion patterns

- endpoint returns hard-coded success;
- TODO hidden behind a passing test;
- mocks replacing the actual filesystem/network path in acceptance evidence;
- claims of Windows compatibility based only on cross-compilation;
- claims of crash safety without process-kill tests;
- generated docs that disagree with executable behavior;
- broad refactoring unrelated to the work order.

## Code quality

- Run formatting.
- Keep errors wrapped with stable public codes and useful internal context.
- Use context cancellation.
- Close files and response bodies.
- Bound concurrency.
- Avoid whole-transfer RAM buffers.
- Make commit/delete/reconcile operations idempotent.
- Prefer explicit state over inferred filesystem accidents.
