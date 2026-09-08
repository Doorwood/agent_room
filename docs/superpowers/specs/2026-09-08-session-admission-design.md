# IP + session admission

The requested flow replaces the Unix-account prerequisite for the default workflow: the owner runs `agent_room host PROJECT`; participants run `agent_room join IP SESSION_ID --name NAME`; the owner lists, approves, denies, or revokes requests locally. Approved participants share the existing persistent Codex thread and FIFO work queue.

Use a native TLS stream (default port 7443), with a SHA-256 certificate fingerprint embedded in the shareable session identifier. The client verifies this fingerprint before sending a random credential. Knowing the session identifier permits requesting admission, never reading history or submitting work. Credentials are stored privately on the client; only their hashes are stored on the host. Approvals and identities are transactional SQLite records. Participant IDs occupy a virtual ID range and never create or resolve Unix accounts. Existing SSH commands remain available for compatibility.

The host uses the current non-root OS account for execution and initializes owner-only membership automatically. Session state is outside the Git root and persists across restarts. A private owner-only Unix administration socket handles request listing and decisions. Revocation closes existing connections. Pending admission expires after 24 hours and is bounded; TLS handshakes and application reads have deadlines and connection caps.

The first implementation serves one project/session per host process. Separate state directories and ports allow more sessions. Distribution includes macOS/Linux amd64/arm64 binaries, checksum manifests, an installer that supports bundled binaries or source builds, and an optional pinned Codex runtime install on Linux. No public publishing is required for the local/devbox delivery.

Verification covers pending denial, correct approval identity, token theft resistance via certificate pinning, session mismatch, approval persistence, revoke/reconnect rejection, bounded input, concurrent decisions, existing queue regressions, and a real devbox-to-Mac request/approve/query/task flow.
