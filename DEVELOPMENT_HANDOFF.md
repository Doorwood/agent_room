# agent_room development handoff

## User intent

The project enables multiple users on different machines to collaborate through
their local CLI with one persistent Codex session working on one host Git project.
No extra GUI is wanted. All accepted agent work uses the execution owner's
authority; participants are trusted collaborators, not isolated tenants.

The user wants subsequent iteration discussions to happen through agent_room
itself, using this checkout as the working project. This is a new Codex thread:
the previous desktop conversation is not automatically imported.

## Current implementation

- Public repository: https://github.com/Doorwood/agent_room (MIT).
- CLI: agent_room. Internal Go module and entry point still use agent_romm.
- Linux host; macOS/Linux clients. TLS-pinned IP + session ID admission with
  host approval. Remote participants need no SSH account or local Codex.
- One persistent Codex thread, FIFO queue, steering/cancellation, durable SQLite
  history, reconnect replay, bounded live projection and explicit recovery.
- Host approval/revocation; execution uses the host owner's full authority.
- Installation and multi-platform packaging scripts are included.
- Optional local browser clients show shared conversations, member filters and
  task progress. Host state is isolated per project. The npm package is
  `menmu-agent-room`, with platform-bundled binaries and checksum verification.
- Exact reviewed Codex runtime pin: 0.153.4. Every host startup resolves Codex
  from the current PATH; model and reasoning effort come from its environment.
  Do not silently relax protocol
  compatibility checks or substitute another version.

## Read these first

1. README.md and docs/QUICKSTART.zh-CN.md for the current workflow.
2. docs/superpowers/specs/2026-09-08-session-admission-design.md.
3. docs/superpowers/plans/2026-09-08-session-admission.md.
4. docs/implementation-decisions.md for recovery and protocol tradeoffs.
5. docs/legacy-core-mvp.md and the September 3 spec/plan for the original core;
   their SSH-only topology is historical, superseded by the admission layer.
6. SECURITY.md and CONTRIBUTING.md.

## Verification and publication provenance

The public initial snapshot includes the completed core and network admission
implementation. Its publication verification passed `go test ./...` and
`go vet ./...` on macOS. Default tests use a fake App Server, not paid calls.
Earlier development notes recorded real Linux host/client smoke testing, but
those deployment-specific details were intentionally removed before publishing.

The public repository starts with a clean initial commit: original local history
contained private deployment details and a company email. Preserve the sanitized
public history; do not merge or push the old private development history.
The repository rename was committed as 52f40d4.

The runtime upgrade replaces the old 0.151.0-alpha.7.2 pin. Existing room
state needs a stopped-host backup and reviewed runtime metadata migration;
see docs/runtime-upgrade.md. Do not reset the database to upgrade a runtime.

## Working expectations

Inspect Git status before edits; preserve others' work. Discuss new scope through
the shared session and implement only requested changes. Keep state, credentials,
logs and session identifiers outside Git. Never commit environment secrets or
private deployment addresses. Re-run relevant tests after changes; do not describe
fake-runtime tests as live Codex acceptance. GitHub write credentials have not
been transferred with the project; check authorization before future pushes.

Potential follow-ups are CI, stable releases, broader three-machine acceptance,
and lifecycle observability, but these are not authorization to start them now.
