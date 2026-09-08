# Session admission implementation plan

**Goal:** Deliver the user-authorized IP/session approval workflow and quick installation on devbox and Mac.

**Architecture:** Keep the room coordinator, durable store and line client. Add transactional admission methods, TLS transport, a private local administration endpoint and simple CLI entry points.

**Spec:** `docs/superpowers/specs/2026-09-08-session-admission-design.md`

- [x] Store: write behavioral tests for pending/approved/denied/revoked requests, then implement `RequestJoin`, `DecideJoin`, `AuthenticateJoin`, `ListJoins` in `internal/store/admission.go`. Allocate virtual member IDs transactionally; never authorize based on a display name.
- [x] Transport: implement certificate creation/pinning and bounded authentication framing in `internal/network`; test approval gating, incorrect certificates, session mismatch and revocation. Generalize daemon session streams to `net.Conn`, while retaining the existing peer-credential path.
- [x] CLI: add `host`, `join`, `requests`, `approve`, `deny`, `revoke`, and `session`. Derive owner, group, and default state automatically; persist client credentials privately and reuse the existing client launcher interface. Run integration and regression tests.
- [x] Distribution: add `scripts/install.sh` and release packaging with macOS/Linux binaries and SHA-256 manifests. Update quickstart with actual commands. Exercise installation into a temporary prefix.
- [x] Delivery: review code, run race/vet/tests/build, update devbox, start host, submit a local join request, approve it and verify shared-session interaction. Record exact session/installation/start/stop details in the quickstart.

Changes are executed in this session; no implementation delegation is needed. Preserve the current public commands, pinned Codex protocol, existing transcripts and recovery behavior.

## Completion evidence

- Full `go test -race ./...` and `go vet ./...` passed.
- Independent Go/security and CLI/installer reviews passed after fixing historical admission exhaustion and wildcard advertisement.
- Four platform binaries built; bundled installer succeeded and tamper rejection preserved the installed executable.
- Real devbox host (Linux amd64) accepted a Mac client, host approved its request, status/who succeeded and a real Codex turn completed with `agent_room 连接成功`.
- Restart retained session/certificate/membership; client reconnected automatically. Host proxy environment was restored from the active SSH terminal.
