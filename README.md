# agent_room

Source repository: [Doorwood/agent_room](https://github.com/Doorwood/agent_room).
The CLI and repository are named `agent_room`.

Early-stage software for trusted collaborators. Approved members can request
work with the host execution owner's full authority. See [SECURITY.md](SECURITY.md).

Create one project session on a Linux host. Participants join from macOS or Linux using the host IP and session ID, request admission, then share a persistent Codex conversation and FIFO work queue after host approval. Participants need no SSH credentials, OS accounts, or local Codex installation.

## Install

Unpack the binary bundle and run:

```sh
sh scripts/install.sh
```

From a source checkout, the same command builds with Go 1.24+. The binary bundle contains macOS/Linux amd64/arm64 executables and checksums; it needs no Go toolchain. Default installation directory: `~/.local/bin`.

On the Linux execution host, optionally install the dedicated compatible Codex runtime (requires Node.js/npm):

```sh
sh scripts/install.sh --with-codex
export PATH="$HOME/.local/share/agent_room/codex/node_modules/.bin:$PATH"
~/.local/share/agent_room/codex/node_modules/.bin/codex login
```

Every host startup resolves `codex` from the current `PATH` and checks its version.
Missing Codex causes an explicit startup error; private runtime directories are
never selected automatically. If Codex is already on PATH, no separate install
is needed. The currently validated CLI version is `0.153.4`; other versions are
rejected until protocol compatibility is reviewed. Model and reasoning effort
are not hard-coded by agent_room: the selected Codex loads its own configuration
and existing login from the current environment (including `CODEX_HOME`).

## Start, join, approve

Host, from a Git project:

```sh
agent_room host .
```

Copy the printed Join command to the participant's machine, choosing a display name:

```sh
agent_room join HOST_IP SESSION_ID --name alice
```

The participant waits for approval. In a second host terminal:

```sh
agent_room requests
agent_room approve REQUEST_ID
```

Approval connects the participant automatically. Enter a question or task as ordinary text. `/status`, `/queue`, `/who`, `/diff`, `/note TEXT`, `/steer TEXT`, `/cancel`, and `/quit` are available. All approved participants can schedule work and control the active turn. Work runs as the execution owner on the host project.

## Manage

```sh
agent_room deny REQUEST_ID
agent_room revoke REQUEST_ID
agent_room session
```

Revocation disconnects that member's current clients and rejects further connections. It does not cancel work already accepted into the shared queue; use `/cancel` for the active turn. A revoked nickname remains reserved for transcript consistency; a fresh application can use a new name.

Ctrl+C stops the host cleanly. Run it in tmux to keep it alive after disconnecting. Start it with the same project and state to reuse the session and membership approvals. Client credentials and replay positions persist locally.

Default listener: port 7443. Default state: `~/.local/share/agent_room/host`, outside the project. For another session use a different state directory and port:

```sh
agent_room host /path/to/other-project --state /absolute/other-state --listen 0.0.0.0:7444
agent_room requests --state /absolute/other-state
agent_room approve REQUEST_ID --state /absolute/other-state
```

Use `--advertise IP:PORT` if the automatically selected interface is not reachable by participants. The chosen TCP port must be reachable; no SSH forwarding is used. `session_id` includes the host certificate fingerprint, so copy it completely. The ID permits requesting admission, not accessing the room. Connections use TLS 1.3; only credential hashes are stored on the host. Pending requests expire in 24 hours; at most 128 may be pending and 64 connections may be open. The host can deny unwanted requests to release capacity. Members and revocation records are retained, with a 4096-record admission cap and pruning of rejected/expired history at capacity.

The default host flow creates only the current owner's local room record. Approved remote members have application identities, never Unix accounts. A host/session serves one Git root and at most one active Codex turn. Transcript attribution is not tamper-proof against the execution owner.

## Build and test

```sh
go test ./...
go test -race ./...
go vet ./...
sh scripts/package.sh
```

Packaging writes `dist/agent-room-bundle.tar.gz`. No publication occurs automatically. Legacy `agent_romm` SSH commands remain supported; see [legacy core notes](docs/legacy-core-mvp.md). The Go module and source entry point retain their original `agent_romm` spelling.

[中文快速使用文档](docs/QUICKSTART.zh-CN.md)

## License and contributions

Released under the [MIT License](LICENSE). See [CONTRIBUTING.md](CONTRIBUTING.md)
for development checks and contribution guidelines. Codex and third-party
dependencies retain their own licenses and terms.
