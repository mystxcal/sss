# Subagent Work Order

## Identity

- Task ID:
- Owner:
- Branch or worktree:
- Due integration wave:

## Objective

State one independently verifiable outcome.

## Context

Link the exact handoff sections and existing code relevant to the task.

## Frozen contracts

List behavior the agent must not change.

## Owned scope

Packages and files the agent may change.

## Out of scope

Explicit exclusions and adjacent owners.

## Deliverables

- code;
- tests;
- docs;
- migrations;
- evidence.

## Acceptance commands

```bash
# exact commands
```

## Required evidence

- command output path;
- logs;
- hashes;
- screenshots only when they add information;
- failure cases exercised.

## Handoff format

- summary;
- changed files;
- tests and observed results;
- open risks;
- integration notes;
- deviations.

## Stop conditions

Escalate to manager only when:

- a frozen contract is impossible or demonstrably harmful;
- another active work order blocks progress;
- required external access or secret is missing;
- evidence shows the architecture must change.

Do not escalate ordinary implementation choices.
