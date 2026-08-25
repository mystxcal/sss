# Merge and Integration Policy

## Branching

- One manager-controlled integration branch.
- One worktree or branch per active work order.
- Rebase or merge frequently enough to expose conflicts early.
- Do not let agents independently alter frozen contracts.

## Merge requirements

- work-order acceptance commands pass;
- tests added;
- evidence attached;
- reviewer checklist completed;
- no unrelated changes;
- public contract changes approved by manager;
- integration branch remains green.

## Ownership conflicts

When two tasks need the same package:

1. manager narrows ownership;
2. land shared interface first;
3. downstream agent rebases;
4. avoid parallel edits to the same state machine.

## Rework

The manager may rewrite subagent code. Optimize for one coherent codebase.

## Commit quality

Commits should explain behavior and invariants, not merely list files. Avoid giant “implementation” commits when work can be reviewed in vertical steps.
