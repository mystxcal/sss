# Glossary

**Advanced path**  
Resumable manifest-and-segment protocol used by the CLI for large or complex transfers.

**Claim**  
Authorization to receive a committed transfer. A claim may hold a bounded lease.

**Code**  
Eight-character human locator such as `K7M4-Q2PX`. It is not the public API password.

**Commit**  
The transition after verification and materialization that makes a transfer available and assigns its code.

**Handoff**  
One immutable transfer bundle: payload, manifest, optional note, and lifecycle metadata.

**Local claim**  
A claim through the Unix socket that returns the existing VPS path.

**Materialization**  
Constructing and verifying the final payload directory from raw segments and small-file packs.

**Pack**  
A bounded `tar.zst` segment containing many small files.

**Payload**  
The final directory tree exposed to receivers.

**Simple path**  
Zero-install curl-compatible endpoints such as `/s` and `/r/{code}`.

**Staging**  
Unpublished transfer storage.

**Transfer ID**  
Internal random identifier, not normally entered by users.

**Wire segment**  
Independently uploaded or downloaded object in the advanced protocol.
