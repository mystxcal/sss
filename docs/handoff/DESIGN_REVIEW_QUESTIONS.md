# Design Review Questions

The manager should answer these from the implementation before release.

## User experience

- Can a new trusted device operate entirely with curl and one password?
- Does the normal CLI require only a URL and that same password?
- Are code and receive-path outputs safe for direct shell assignment?
- Is directory sending automatic in the CLI?
- Does the VPS path flow avoid both network transfer and full duplication?

## Lifecycle

- What exact durable event makes a transfer committed?
- What exact invariant lets reconciliation distinguish complete from incomplete live directories?
- Can a response be lost after commit, and how does idempotency recover the code?
- When can cleanup delete a transfer?
- How are active downloads and local path users pinned?

## Storage

- How is disk capacity reserved?
- How is materialization overhead estimated?
- Which files are duplicated, and why?
- Can streaming ZIP or TAR accidentally buffer or create a full temporary archive?
- Are database and filesystem orphans visible to admin tooling?

## Cross-platform

- What path subset is truly portable?
- How are Windows reserved names handled?
- How are executable bits represented?
- What happens with Unicode normalization collisions?
- Are native Windows tests proving behavior rather than only compilation?

## Failure

- What happens at every write, fsync, rename, and database commit boundary?
- What happens if the daemon dies while deleting?
- How does a resumed send prove source identity?
- How does a resumed receive detect corrupt cached data?
- What happens when disk fills after a reservation was accepted?

## Simplicity

- Which dependencies are essential?
- Which settings could be deleted?
- Is there any behavior implemented twice?
- Is any background service separable only because of an abstraction preference?
- Is a feature present mainly for hypothetical future scale?
