# Change Control

## Public contract changes

Changing any of the following requires a recorded ADR and contract/test updates:

- endpoint path or method;
- authentication behavior;
- code format;
- TTL semantics;
- stdout or stderr behavior;
- error code;
- manifest field meaning;
- receive archive behavior;
- local path semantics;
- supported filesystem entry types;
- lifecycle state invariant.

## Acceptable internal changes

The manager may freely improve:

- package layout;
- libraries;
- worker scheduling;
- SQL implementation;
- pack thresholds;
- buffering;
- metrics;
- test infrastructure;
- deployment hardening;

provided public behavior and invariants remain intact.

## Deviation test

A deviation is justified only when:

1. the existing decision causes a demonstrated failure or material penalty;
2. alternatives were evaluated;
3. the new design is simpler or materially better;
4. migrations and compatibility are addressed;
5. tests and documentation are updated.

“Agent preference,” “common industry pattern,” or “future scalability” alone is not sufficient.
