# agent_romm

agent_romm Milestone 1 is a local engine demo for trusted collaborators. It is not yet the three-machine release. All agent work executes with the execution owner's full runtime authority; transcript attribution is best-effort and owner-tamperable. Default tests use a fake App Server and do not consume model quota.

The core owns one Git project, one room, one persistent Codex thread, and at most one active turn. Participant prompts queue in FIFO order. There are no parallel worktrees or isolation between participants and the execution owner's credentials, filesystem, or network access.

## Build and verification

Requires Go 1.24 or newer and Git. Dependencies are pinned in `go.mod` and `go.sum`: `modernc.org/sqlite v1.45.0` and `golang.org/x/sys v0.37.0`. The only accepted Codex CLI is `codex-cli 0.151.0-alpha.7.2`; App Server must identify as `agent_romm/0.151.0-alpha.7.2`. The pinned schema SHA-256 is `31ae67beb2c94cc9509f6a71968600062dc8c6d7fe45437ed3a9129838f4d2d9`.

```sh
go build -trimpath -o ./bin/agent_romm ./cmd/agent_romm
./bin/agent_romm help
go mod tidy
go vet ./...
go test ./...
go test -race ./...
```

Tests build local fixture executables and use Git, SQLite, pipes, and Unix sockets. They never resolve the installed Codex executable or make model requests. Run socket tests in an environment that permits local bind/connect. The process acceptance test uses three client processes and a daemon process, explicit test-only identity/process dependencies, and a fake SSH executable that executes the production `agent_romm bridge` binary.

Production process supervision and kernel peer identity are Linux-only. On macOS, production `serve` returns `unsupported-platform`; the local demo tests use injected process and identity implementations. These macOS results do not validate the Linux process manager, real sshd, real Unix-account separation, or paid Codex execution. Linux runtime execution remains unverified in this M1 macOS acceptance run.

## Host-local commands

The following production workflow requires a provisioned Linux host, the exact pinned Codex CLI, and an authenticated execution-owner account. Unlike default tests, `init` probes the real App Server, and `serve` can execute agent work with that account's authority. Three named accounts must exist and belong to the explicitly selected shared Unix group. Run administration as the non-root execution owner. The state directory must be absolute, outside the Git project, and on a canonical path without symlink ancestors.

```sh
agent_romm init --project /srv/project --state-dir /srv/agent-room \
  --room demo --execution-owner alice --shared-group collaborators \
  --members alice,bob,carol --full-owner-access
agent_romm serve --state-dir /srv/agent-room
agent_romm connect alice@execution-host
```

`connect` launches `ssh -T TARGET 'agent_romm bridge'`. Host-key validation and SSH account authentication remain SSH's responsibility. Each remote account needs a non-secret locator at `os.UserConfigDir()/agent_romm/bridge.json`: normally `$HOME/.config/agent_romm/bridge.json` on Linux, or the directory selected by ordinary `XDG_CONFIG_HOME`. Provision the file with this content, using the room's configured socket path:

```json
{"socketPath":"/srv/agent-room/room.sock"}
```

`bridge` accepts no arguments and relays opaque protocol bytes. It never accepts a socket path or participant identity from protocol input. There are no production `--as-user`, `--fake`, `--local`, or environment switches for alternate dependencies.

## Collaboration and recovery

Enter plain text to queue a prompt. `/note TEXT` records a room note without sending it to Codex. `/steer TEXT` and `/cancel` refer to the active turn ID observed by that client; stale IDs are rejected. `/queue`, `/status`, `/who`, `/diff`, and `/quit` provide the remaining line interface.

Durable messages, notes, control outcomes, completed items, and recovery decisions have one monotonically increasing SQLite sequence. Clients persist their last applied sequence and replay missed durable events after reconnecting. Live deltas are transient; reconnect replaces their projection from a snapshot rather than replaying every token. The encoded JSON mutation body limit is 64 KiB, including field names and IDs; the transport frame ceiling is 8 MiB. Slow clients are disconnected without blocking the room.

Completed output whose HTML-safe JSON encoding exceeds the room-event budget (8 MiB minus 128 KiB of metadata reserve) is stored as an explicit `output-omitted` record. It carries the original upstream JSON byte count and SHA-256 of its compact, HTML-safe JSON representation; equivalent history with different insignificant whitespace remains idempotent. Such output must be inspected in project/Codex artifacts. Live projection charges identifiers, entry overhead, and worst-case escaped text against a 1 MiB aggregate budget. It visibly reports truncation for the remainder of that turn, including after reconnect; completed output still arrives durably. Terminal turns release their partials and completion suppression keys.

A lost mutation response may mean the action already ran. The runtime restarts with bounded backoff and reconciles the same thread, but never automatically retries an ambiguous mutation. Review the room's `needs-review` request before selecting exactly one recovery action:

```text
/recover retry REQUEST_ID
/recover skip REQUEST_ID
/recover continue REQUEST_ID NEW_INSTRUCTION
```

Competing recovery decisions have one transactional winner. If initial thread creation is uncertain, stop the daemon and repair locally as the execution owner after reviewing the candidate thread or deciding to replace it:

```sh
agent_romm repair-thread --state-dir /srv/agent-room --use THREAD_ID
agent_romm repair-thread --state-dir /srv/agent-room --create
```

Shutdown closes client sessions, cancels the coordinator and runtime, reaps only the owned child, persists a stopped checkpoint, and then closes SQLite and releases the lock. A live or unprovable prior-generation process checkpoint refuses startup without signaling the recorded PID. Operational JSON logs contain metadata and diagnostic digests, not transcript bodies or raw App Server stderr.

M1 logs accepted-message connection/UID/message/sequence metadata and committed thread, turn, and completed-item transitions. Runtime failures have classifications and diagnostic digests. Complete normal and signal process-exit status logging and captured command lifecycle/exit details remain outside M1's reduced observability scope and will be added during Linux deployment. These logs do not constitute the full observability coverage described in design section 14.

Git status/diff disable external helpers and refuse configured Git filters. Submodule inspection is nonrecursive: nested repositories are not traversed or presented as part of the parent project diff.

## Package boundaries and next milestones

`internal/cli` wires entry points; `admin` and `config` validate owner-only setup and state; `identity` resolves kernel peers; `protocol`, `bridge`, and `client` implement framed SSH transport and the line client. `daemon` owns socket sessions and replay; `room` owns serialized coordination and transient projections; `store` owns SQLite transactions. `codex` implements the pinned JSON-RPC adapter, child supervisor, and stable restart runtime. `gitview` provides constrained Git reads; `observability` accepts metadata-only records.

The next Linux SSH milestone covers real sshd, keys, host checking, ProxyJump, three Unix accounts/hosts, service provisioning, and separately authorized real-Codex acceptance. macOS lifecycle support is a later milestone. Licensing, contributor packaging, CI hardening, secret scanning, GitHub publication, and releases are outside this M1 core demo.
