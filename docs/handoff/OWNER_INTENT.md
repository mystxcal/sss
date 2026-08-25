# Owner Intent

## The problem

AI agents on different devices need to hand files to one another.

Possible routes include:

- VPS to device A;
- device A to device B;
- device B to VPS;
- any later combination of trusted Windows and Linux machines.

The sender and receiver may not be online simultaneously. The owner already has a Debian 11 VPS and wants that VPS to act as the temporary rendezvous point.

## Desired experience

The system should feel almost primitive in its simplicity:

```bash
sssend <files-or-directories>
```

The command uploads the handoff, assigns a short code, and prints that code. The sender gives the code to another agent. The recipient runs:

```bash
ssrecv <code>
```

The default expiry is 30 minutes. The sender may extend it up to 3,000 minutes.

The system must also work with raw HTTP tools. Installing the CLI cannot be mandatory. A basic `curl` upload or download must be a first-class, documented path.

The owner wants one configurable base password. The same password authenticates raw HTTP and the CLI. It is acceptable for all trusted devices to share it in v1.

When the recipient runs on the VPS itself, the files are already there. The system must return the existing path immediately rather than downloading or needlessly copying them.

## Product priorities

### Highest priority

- almost no setup after the server is installed;
- obvious commands;
- dependable transfer completion;
- no sender/receiver coordination;
- fast transfer of both large files and large file trees;
- clean automation behavior for agents;
- easy installation on Windows and Linux;
- low operational burden on the VPS.

### Required capability

- files and directories;
- one or many transfer roots;
- optional handoff note;
- configurable TTL from 1 to 3,000 minutes;
- multiple receivers until expiry;
- remote and VPS-local receipt;
- clean restart behavior;
- resumable advanced transfers;
- integrity verification;
- deterministic errors;
- real `curl` recipes.

### Deliberate restraint

Do not overfocus on security features. Implement the minimum that makes a public HTTPS service sane:

- TLS;
- one base password;
- password hashing at rest;
- basic rate limiting;
- path safety;
- resource limits;
- no secret logging.

Do not add:

- accounts;
- organizations;
- RBAC;
- a web dashboard;
- P2P transport;
- end-to-end encryption;
- permanent storage;
- folder synchronization;
- user-managed buckets;
- Redis, Kafka, Kubernetes, or a database server;
- a plugin system;
- previews, transformations, or content indexing.

## Design test

A feature belongs only when omitting it would materially damage one of these:

- simplicity;
- reliability;
- transfer speed;
- agent automation;
- cross-platform behavior;
- VPS-local efficiency.

The final system should feel complete because the unnecessary parts are absent.
