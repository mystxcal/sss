# Risk Register

| Risk | Consequence | Control | Proof |
|---|---|---|---|
| SQLite/filesystem split state | Code points to missing or incomplete data | Explicit state machine, atomic renames, reconciliation | Fault tests at every commit boundary |
| Disk fills during materialization | Corrupt or incomplete publish | Reservation, high watermark, staging-only writes | Forced disk-full tests |
| Archive path traversal | Files escape payload root | Reject absolute, parent, duplicate, and symlink entries | Malicious corpus tests |
| Windows/Linux path mismatch | Receive fails or overwrites | Portable manifest rules and native tests | Cross-platform matrix |
| Simple curl response is lost after commit | Sender may not know code | Optional idempotency key; advanced CLI resume | Ambiguous-response test |
| Source changes while uploading | Inconsistent snapshot | Pre/post identity checks and digest | Mutation tests |
| Cleanup races active receive | Data disappears mid-transfer | Claim lease and pinning | Expiry and claim race tests |
| Local path outlives cleanup assumption | Agent loses files still in use | Read-only grace; copy for long-lived work | Local grace tests and docs |
| Too many tiny files | Request and filesystem overhead | Bounded packs and file limits | 10k and 100k file benchmarks |
| Large archive generation duplicates storage | VPS disk exhaustion | Stream ZIP and TAR | Disk observation test |
| Base password appears in logs or tooling | Credential exposure | Redaction, prompt, stdin, netrc guidance | Log audit |
| Dependency blocks static cross-build | Deployment friction | Prefer pure-Go dependencies; isolate replacements | Release matrix |
| Overengineering delays usable system | No delivered product | Vertical milestones and frozen non-goals | Manager gate reviews |
| Superficial tests reward-hack completion | Hidden failures | Black-box and subprocess fault evidence | Independent release audit |
