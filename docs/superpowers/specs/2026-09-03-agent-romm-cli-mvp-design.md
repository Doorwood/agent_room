# agent_romm Multi-User CLI MVP Design

**Date:** 2026-09-03
**Status:** Approved for implementation on 2026-09-03
**Repository and binary name:** `agent_romm`

## 1. Purpose

`agent_romm` lets three trusted people, each using a CLI on a different computer, join one shared room and communicate with the same persistent Codex thread working in one project directory. Three users are the MVP acceptance target; the protocol and membership model do not intentionally hard-code that count.

The MVP is deliberately a shared-control tool rather than a multi-tenant collaboration platform. One designated room owner supplies the project host, operating-system identity, Codex authentication, configuration, tools, network access, and filesystem permissions. Every room member's prompt is executed with that owner's full authority.

The product has no web page, desktop application, hosted relay, or public application server. OpenSSH carries each local CLI connection to the project host.

## 2. Approved Decisions

1. The repository and executable are named exactly `agent_romm`.
2. The implementation is a Go single binary with multiple subcommands.
3. Exactly three users on three different machines are the minimum acceptance deployment. The execution owner's machine also serves as the project host; a separate fourth project host is compatible but not required for MVP acceptance.
4. All clients, including the owner when exercising the multi-machine path, connect to the project host through their existing OpenSSH setup.
5. The project host contains the canonical project directory and runs one persistent `agent_romm` daemon and one Codex App Server child process.
6. The room maps to one active Codex `thread.id`; it does not use a browser session or Codex `thread.sessionId` as the product identity.
7. The room owner is the `execution_owner`. All members' agent requests inherit the execution owner's full permissions and Codex configuration.
8. `agent_romm` explicitly requests Codex's unrestricted execution mode and does not add per-user sandbox, network, tool, or per-action approval restrictions. Operating-system, provider, and infrastructure limits may still apply. The trust and credential exposure this creates are explicit product assumptions.
9. All members see the same captured transcript, App Server events, and Git status/diff projection. Private messages are not supported. Background processes and project-external side effects may not be observable.
10. The daemon serializes its own Codex turn dispatch. It does not claim exclusive control of workspace mutations made by the owner, background processes, or external programs.
11. The project will be released publicly under Apache-2.0 after implementation, verification, documentation, and a secret scan.
12. Each `agent_romm` release supports an explicit Codex CLI/App Server version allowlist backed by a reviewed protocol-schema fixture. The first implementation target is `codex-cli 0.151.0-alpha.7.2`; unknown versions fail closed rather than guessing field shapes.

## 3. Terminology and Identity

The design keeps four identities separate:

- **Client connection:** one temporary local CLI process and its SSH connection.
- **Room:** the durable `agent_romm` collaboration object, transcript, membership, queue, and event sequence.
- **Codex thread:** the durable Codex conversation containing turns and items. Exact continuation uses `thread.id`.
- **Project workspace:** the canonical directory and current filesystem/Git state on the project host.

The Codex protocol defines `Thread -> Turn -> Item`. App Server supports starting, reading, resuming, and forking threads; starting, steering, and interrupting turns; and streaming item and turn events. These primitives provide the agent runtime but not multi-user ordering, user attribution, authorization, or reconnect replay. Those are `agent_romm` responsibilities. See the official [Codex App Server documentation](https://learn.chatgpt.com/docs/app-server) and [Codex SDK documentation](https://learn.chatgpt.com/docs/codex-sdk).

The stable room identifier is generated and owned by `agent_romm`. The room stores the tuple `(host_id, canonical_project_path, codex_thread_id)`. A Codex thread identifier alone is insufficient because its persisted rollout and its working directory belong to the project host.

## 4. Trust Model

The MVP supports one trust domain: a small team whose members are all trusted with the execution owner's machine authority and project data.

Each participant uses a distinct SSH account and key on the project host. A remote `bridge` process runs under that SSH account. The daemon obtains the bridge's operating-system UID from Unix peer credentials and maps it to a configured room member. It never trusts a username, role, UID, or author field supplied inside the client protocol.

The room owner runs both `agent_romm init` and `agent_romm serve`. Both commands verify that the process effective UID equals the stored execution-owner UID. App Server is a direct child of that daemon. It inherits the daemon service process's UID, `HOME`, Codex files, explicit environment, and descriptors intentionally passed by the daemon. A launchd/systemd service does not necessarily inherit the owner's interactive shell environment, so readiness is checked again whenever the daemon starts.

The owner is the execution identity and pays for model usage. "Full owner access" means the access actually available to the daemon and App Server processes at runtime, with Codex configured for `approvalPolicy: "never"`, thread sandbox mode `danger-full-access`, and turn sandbox policy `dangerFullAccess`. It does not override operating-system permissions, provider policy, host firewalling, upstream rate limits, or missing credentials.

Every accepted member may cause the agent to exercise those capabilities. Message authorship is recorded as best-effort attribution, not for reducing execution authority and not as tamper-proof audit evidence. This means a malicious or compromised member can instruct the agent to read, modify, delete, transmit, or otherwise act on anything the execution owner can access. The agent may also be able to alter `agent_romm` state, invoke host-local administrative commands, consume unbounded model quota, exhaust host resources, launch persistent background processes, or make the host unavailable. Only operating-system and provider limits remain. `agent_romm` cannot offer secrets isolation, message-level confidentiality, non-repudiation, availability protection, or protection between room members in this mode.

Direct client protocol calls still enforce membership and distinguish normal from host-local administrative commands. Because the agent has the owner's authority, these checks do not prevent an indirect request from asking the agent to invoke or modify owner-accessible administration and state. Protecting the control plane from the execution owner would require a separate security identity and is outside this approved MVP.

The project host administrator is also trusted. Root can impersonate users, inspect state, and alter the executable. The owner may likewise alter all owner-readable state. The stored transcript is an operational convenience, not a security-grade audit log.

## 5. System Architecture

```text
User A computer       User B computer       User C computer
agent_romm connect    agent_romm connect    agent_romm connect
        |                    |                    |
        +--------------------+--------------------+
                             |
                   OpenSSH sessions (`ssh -T`)
                             |
                project host: agent_romm bridge
                (one short-lived process per user)
                             |
                     Unix domain socket
                             |
                   agent_romm serve daemon
                - membership and peer UID checks
                - room coordinator and FIFO queue
                - SQLite state and event sequence
                - Codex protocol adapter
                             |
                  stdin/stdout JSON-RPC (JSONL)
                             |
                    codex app-server child
                             |
             canonical project directory and shell
```

In the minimum three-machine deployment, User A is the execution owner and User A's computer is also the project host shown below the SSH boundary. User A exercises the same network path through an SSH host alias for that machine. Deploying the project on a separate fourth host uses the same protocol but is not part of the minimum acceptance setup.

The Go release is one binary with three runtime roles:

- `agent_romm connect`: local interactive client. It invokes the system `ssh` command and presents the shared transcript.
- `agent_romm bridge`: fixed remote command. It connects to the local Unix socket and copies framed protocol messages between SSH stdio and the daemon. It holds no room state.
- `agent_romm serve`: long-running daemon. It owns room state, client fan-out, message ordering, recovery, Codex App Server lifecycle, and project binding.

The browser and App Server WebSocket transports are intentionally absent. OpenAI currently documents App Server's direct WebSocket listener as experimental and unsupported for production, and advises against exposing App Server on shared or public networks. The SSH-native topology keeps App Server private on stdio. See [Remote connections](https://learn.chatgpt.com/docs/remote-connections).

## 6. Installation and Host Configuration

The project host must have:

- Linux or macOS;
- OpenSSH server access for each member;
- a distinct Unix account and SSH key for each member;
- a pre-created shared Unix group containing the execution owner and all three member accounts;
- `agent_romm` installed in the non-interactive SSH command path;
- a Codex CLI/App Server version explicitly supported by that `agent_romm` release, installed and authenticated as the execution owner;
- one canonical Git worktree root accessible to the execution owner;
- one owner-controlled state directory outside the project directory;
- Go is needed only for source builds, not for a released binary.

The state layout separates the shared connection endpoint from private state. A top-level run directory is owned by the execution owner and the pre-created member group, with mode `0710`; its control socket is owner/group with mode `0660`. A nested private directory has mode `0700` and contains SQLite, configuration, logs, PID metadata, and the daemon lock with owner-only permissions. Group members can traverse to and connect to the known socket but cannot list or directly modify private state. `init` verifies group membership and fails if any configured member is absent.

These filesystem modes prevent a member's ordinary SSH process from directly editing private state. They do not protect state from Codex commands running with the execution owner's full authority, which is an accepted limitation of this MVP.

`agent_romm init` performs one-time initialization:

```bash
agent_romm init \
  --project /absolute/path/to/repository \
  --room team \
  --member alice \
  --member bob \
  --member carol \
  --execution-owner alice \
  --full-owner-access
```

Initialization canonicalizes the project path, resolves the execution owner and member usernames to UIDs, records the explicit full-access mode, creates state, and checks that Codex App Server starts in the `init` process environment. Because a service environment can differ, `serve` repeats the readiness check in its actual daemon-service environment. The daemon refuses to start if the stored project path no longer resolves to the initialized directory.

Initialization also requires the project path to be the root of a valid Git worktree. `git rev-parse --show-toplevel` must resolve to that same canonical directory. Non-Git directories and project subdirectories are outside the MVP because recovery review and `/diff` depend on a complete worktree projection.

`init` and `serve` must be run by the named execution owner. Each verifies `geteuid()` against the stored UID and fails rather than trying to switch Unix identities. App Server must remain a direct child of `serve`.

The owner then runs `agent_romm serve` in the foreground or configures it as a launchd/systemd user service. The MVP documentation supplies examples but does not install privileged system services automatically.

## 7. SSH Transport and Framing

A user joins from their own computer with:

```bash
agent_romm connect alice@project-host
```

The client invokes the system OpenSSH executable as an argv array. It preserves the user's SSH config, agent, `ProxyJump`, hardware-key integration, and known-host verification. The remote command is the fixed literal `agent_romm bridge`; user messages, project paths, room names, and protocol data are never interpolated into a shell command.

`ssh -T` disables PTY allocation. SSH stdout contains protocol frames only, while diagnostics and authentication prompts use stderr. Disconnecting SSH terminates only that bridge and client connection; it does not terminate the daemon, Codex App Server, or an active turn.

The same framed protocol is used over SSH stdio and the Unix socket. Each frame consists of a four-byte unsigned big-endian length prefix followed by UTF-8 JSON, with a maximum frame size of 8 MiB. The protocol defines:

- a maximum frame size;
- a version handshake;
- explicit request, response, event, and error frame kinds;
- opaque server-generated connection and session identifiers;
- client-generated message idempotency keys;
- server-generated monotonically increasing room sequence numbers;
- heartbeat and graceful-close messages;
- strict rejection of unknown required fields, invalid lengths, malformed JSON, and unsupported versions.

Frame-size limits, client-buffer limits, heartbeats, and reconnect backoff protect the coordination protocol only. They are not limits on commands, model usage, child processes, filesystem effects, or network effects produced by unrestricted Codex execution.

The remote non-interactive shell must not print banners or startup text to stdout. Installation includes a probe that performs the version handshake through real `ssh -T`; any bytes before the first valid frame cause a clear setup failure. Stderr remains available for SSH and bridge diagnostics.

The bridge is a byte relay after connecting to the daemon. All validation, identity mapping, authorization, ordering, and state changes occur in the daemon.

## 8. CLI Experience

After connection, the CLI prints the current room identity, project path, execution owner, full-access warning, active turn state, online members, and recent or missing transcript events. It then reads user input while asynchronously rendering room events.

Example:

```text
room=team project=/srv/project executor=alice mode=owner-full-access
[alice] Fix the refresh-token race.
[codex] Inspecting the authentication package.
[codex:command] go test ./...
[bob] /note I suspect the transaction boundary.
[carol] /steer Also inspect refresh token rotation.
```

Supported MVP commands are:

- plain text: start a turn when idle, otherwise enqueue the next turn;
- `/steer <text>`: explicitly append input to the active Codex turn;
- `/note <text>`: add a human-only room message that is not sent to Codex;
- `/queue`: display pending turn requests in deterministic order;
- `/status`: display daemon, Codex thread, active turn, queue, and project status;
- `/who`: display connected participants;
- `/cancel`: interrupt the current turn only when the client's last displayed turn ID still matches the daemon's active turn;
- `/diff`: request the current Git working-tree diff from the execution host;
- `/recover retry <message-id>`: explicitly redispatch an ambiguous request after warning that side effects may be duplicated;
- `/recover skip <message-id>`: mark an ambiguous request skipped and unfreeze the queue;
- `/recover continue <message-id> <instruction>`: mark the ambiguous request reviewed and enqueue a new recovery instruction instead of replaying it;
- `/quit`: disconnect the local client without stopping active work.

All configured members may use agent commands, including `/steer`, `/cancel`, and `/recover`, because all accepted agent work runs with the execution owner's full authority. The direct client protocol exposes configuration and membership commands only to a host-local execution-owner invocation. This is a UX distinction, not a security boundary against the unrestricted agent: a member may still ask the agent to invoke or alter anything available to the execution owner.

Thread repair is deliberately host-local rather than available through a remote room connection. The execution owner uses `agent_romm repair-thread --use <thread-id>` after inspecting an existing candidate, or `agent_romm repair-thread --create` to authorize a replacement. Both commands require the daemon to be stopped, verify the execution-owner EUID and fixed project binding, update state transactionally, and preserve the prior thread ID and repair reason in room history.

The MVP is a line-oriented asynchronous CLI, not a full-screen TUI. ANSI color is optional and automatically disabled when output is not a terminal.

## 9. Message and Turn Semantics

The daemon is the only component that dispatches turns to the Codex thread. It serializes that dispatch stream but does not control external writers to the project workspace.

For ordinary messages:

1. The client generates a random `client_message_id` and sends the text.
2. The daemon authenticates the Unix peer, validates size and schema, and inserts the message and command in one SQLite transaction.
3. A uniqueness constraint on `(room_id, client_message_id)` makes reconnect retries idempotent.
4. The daemon assigns the durable message a `room_seq` and broadcasts acceptance.
5. If no turn is active, the coordinator dispatches it with `turn/start`.
6. If a turn is active, the request remains in FIFO order until that turn reaches a terminal state.
7. The triggering Codex input includes a daemon-generated participant label such as `[participant: bob]`. The label improves model context but has no authorization meaning.

For `/steer`, the client must identify the active turn last displayed to it. The daemon verifies it is still active and calls `turn/steer` with the exact `expectedTurnId`. A stale steer fails visibly and is not silently converted into a new turn.

`/cancel` follows the same optimistic-concurrency rule. It carries the client's expected active turn ID, and the daemon rejects a stale cancel rather than interrupting a newer turn.

For `/note`, the daemon writes and broadcasts a room event without adding anything to Codex history.

The daemon dispatches at most one Codex turn at a time. Parallel prompts are serialized; parallel task threads and Git worktrees are future work. This is a scheduler property, not a filesystem exclusivity guarantee: the owner, an external program, or a previously launched background process can still mutate the workspace concurrently.

The persisted request state machine is:

```text
queued -> dispatching -> running -> completed | failed | interrupted
                    \-> needs-review
```

Entering `needs-review` freezes later agent requests but continues to allow room notes and status inspection. A member must select one of the explicit `/recover` actions before FIFO dispatch resumes. Only the normal, uninterrupted path automatically dispatches an accepted message, and it does so once. Shell, filesystem, network, and external-service side effects do not have exactly-once semantics.

## 10. Codex App Server Lifecycle

The daemon owns one long-running `codex app-server --listen stdio://` child process.

The first implementation adapter is pinned to `codex-cli 0.151.0-alpha.7.2` and the protocol schema generated from that binary on 2026-09-03 with `codex app-server generate-json-schema --experimental`. That schema is the authority for the first adapter: its thread-level `SandboxMode` value is `danger-full-access`, while its turn-level `SandboxPolicy.type` value is `dangerFullAccess`. The repository keeps a reviewed schema fixture or fingerprint plus compatibility tests. `init` records the detected `codex --version`; every `serve` startup checks both the executable version and the App Server `initialize` response `userAgent` against the release allowlist. An unrecognized or changed version stops before thread resume or turn dispatch. Updating the supported version requires an explicit schema diff, adapter review, and compatibility-test update; the target may be refreshed this way before the first tagged public release.

At startup it:

1. starts App Server with stdin/stdout pipes;
2. performs one `initialize` / `initialized` handshake;
3. reads the stored room mapping;
4. calls `thread/read(includeTurns: true)` when a thread ID exists;
5. calls `thread/resume` to load and subscribe to that thread, or `thread/start` only when the room has never obtained a thread;
6. injects the initialized canonical project path as `cwd` on every `thread/start`, `thread/resume`, and `turn/start`, and rejects client attempts to override it;
7. inherits the execution owner's daemon-service Codex authentication, selected model/provider, tools, MCP configuration, skills, and explicit environment;
8. explicitly sends `approvalPolicy: "never"` and `sandbox: "danger-full-access"` on thread creation/resume, and sends `sandboxPolicy: {"type":"dangerFullAccess"}` plus `approvalPolicy: "never"` on every turn; the kebab-case thread enum and camel-case turn policy are intentionally different App Server protocol values;
9. verifies that App Server accepts those fields and that the account/provider is ready; otherwise startup or the pending turn fails visibly instead of silently falling back to a restricted or different mode.

The fixed values above take precedence over an execution owner's conflicting Codex sandbox or approval defaults. Other owner configuration remains inherited. "Unrestricted" means no additional Codex sandbox or `agent_romm` approval gate; it cannot bypass OS permissions, provider policy, network infrastructure, or missing credentials.

On resume, the daemon compares the stored host/project binding with the fixed canonical `cwd`. A missing or corrupt rollout, an unreadable thread, a mismatched project binding, or an uncertain initial `thread/start` result moves the room to `thread-needs-repair`. It never silently creates a replacement thread. FIFO agent dispatch remains frozen until the execution owner uses a host-local repair command to bind a reviewed existing thread or explicitly create a replacement.

The adapter implements only the documented methods required by the MVP: initialization, account readiness, thread start/read/resume, turn start/steer/interrupt, and turn/item notifications. Experimental pagination and direct remote transports are excluded.

The daemon records the Codex thread and turn IDs as soon as responses arrive. Streaming deltas are marked transient and broadcast for responsiveness without a durable `room_seq`. Completed items and terminal turn state receive durable room events, replace accumulated transient projections, and become the replayable transcript representation.

App Server runs in a dedicated child process group. The daemon records a generation nonce, PID, and platform process-start identity; closes App Server stdin and may terminate the process group during a clean shutdown only while it still owns the original, unreaped child handle; and uses a parent-death mechanism where the platform supports one. After a daemon restart, the MVP never signals a PID or process group from persisted metadata because identity verification followed by a group signal has a reuse race. The new daemon proceeds only when it proves the recorded process is absent. A still-live, mismatched, or unprovable prior process stops startup with an owner-repair error instead of risking an unrelated process or launching a second App Server.

## 11. Persistence Model

SQLite in WAL mode is the only database. A single daemon process owns writes, so no distributed database or leader election is needed.

The logical entities are:

- `room`: room ID, display name, host ID, project path, execution-owner UID, Codex thread ID, status, schema version;
- `member`: room ID, Unix UID, canonical username, added timestamp;
- `client_connection`: ephemeral connection ID, member UID, connect/disconnect timestamps, last acknowledged sequence;
- `message`: server ID, client idempotency ID, author UID, kind, body, creation time, queue state;
- `turn_binding`: triggering message ID, Codex turn ID, state, start and completion timestamps;
- `room_event`: room sequence, actor UID, event kind, durable payload, timestamp;
- `runtime_checkpoint`: App Server generation, Codex CLI/App Server version and schema fingerprint, latest observed item/turn state, recovery status;
- `schema_migration`: applied database schema versions.

The database is the source of truth for product identity, human authorship, queue state, and client replay. Codex rollout history is the source of truth for model-visible conversation. The Git working tree is the source of truth for project files. Recovery reconciles these three sources rather than pretending any one contains the others.

## 12. Event Delivery and Reconnect

The daemon gives every durable room event a strictly increasing `room_seq`. Transient streaming deltas have no durable sequence and no replay guarantee. A connected client acknowledges the highest applied durable sequence. The local client persists that cursor in its user state directory. Reconnect supplies the cursor when available; the daemon replays later durable events before switching the connection to live delivery.

During a live turn, clients assemble transient deltas under `(turn_id, item_id)`. A durable `item/completed` event contains the complete final projection and replaces any partial assembly. A reconnect while the daemon remains alive receives the daemon's current in-memory item snapshot before new deltas. After a daemon/App Server failure, only completed history is reconstructed; an unproven in-flight item is handled through `needs-review` rather than represented as complete.

Durable-event delivery to clients is at least once. Applying `room_seq` in order makes duplicate durable-event delivery harmless. A message idempotency key prevents duplicate acceptance; it does not guarantee exactly-once Codex, shell, filesystem, network, or external-service effects.

Slow clients have bounded outgoing buffers. When a client exceeds the buffer, the daemon closes that connection with a resumable error instead of blocking Codex event handling or other clients.

## 13. Failure Recovery

### Client or SSH failure

The daemon marks the connection offline. Active Codex work continues. On reconnect the client receives missed durable events and the current runtime snapshot.

### Bridge failure

The bridge contains no durable state. It exits when either side closes. The local client may reconnect with the same message idempotency ID when it did not receive acceptance.

### Codex App Server failure

The daemon marks the runtime recovering, restarts App Server with bounded exponential backoff, initializes it, reads the stored thread, and resumes its completed history. New messages remain queued during recovery. An in-flight turn is not assumed to continue across an App Server crash. If its terminal state cannot be proven from restored history, its binding enters `needs-review` and freezes agent dispatch.

### Daemon failure or host reboot

SQLite transactions preserve accepted messages and command states. At restart, the daemon acquires its single-instance lock and proves the recorded App Server process is absent before starting a new child; it does not automatically kill a prior-generation process from persisted PID metadata. It then reads/resumes the existing thread's completed history, compares recorded and actual completed turns, and reconstructs the queue. A live or unprovable old process requires owner repair. Resuming the thread does not promise continuation of the prior in-flight turn.

If a mutating `turn/start` may have reached App Server but no turn ID was durably recorded, the command becomes `needs-review`. The daemon does not automatically resend it because shell, filesystem, network, or external-service side effects may already have occurred. Git status and diff are only partial evidence: they cannot reveal every project-external action, ignored-file change, background process, commit, reset, clean operation, or transmitted secret. The CLI presents the available evidence and requires an explicit `/recover` decision.

If initial `thread/start` may have succeeded without a durably recorded thread ID, or if the stored rollout is missing, corrupt, or bound to a different project, the room becomes `thread-needs-repair`. No replacement thread is created automatically. The execution owner must inspect candidate Codex threads and use `agent_romm repair-thread --use <thread-id>` to bind one or `agent_romm repair-thread --create` to authorize a replacement.

### Approval or input request

`agent_romm` adds no approval gate and requests `approvalPolicy: "never"`; it does not promise that every OS, provider, MCP server, or future App Server build can avoid blocking input. If App Server nevertheless sends an approval request, `tool/requestUserInput`, or another unsupported server request, the adapter writes a durable error, returns the safest documented decline/cancel response when available (otherwise a JSON-RPC error), interrupts the turn if it remains active, and marks it failed/unsupported. It never auto-approves an unexpected request.

### Project host offline

Clients report the SSH failure. No central queue exists outside the project host, so messages cannot be accepted while it is offline. The MVP does not move a thread or workspace to another host.

## 14. Observability and Best-Effort Attribution

The daemon writes structured local logs with connection IDs, member UIDs, message IDs, room sequence numbers, Codex thread/turn/item IDs, captured command lifecycle, exit status, and recovery transitions. It does not intentionally log Codex credentials.

The shared transcript records the SSH UID that initiated each accepted protocol message. This best-effort attribution does not imply that the action used that member's OS permissions; all agent actions use the execution owner. Because an unrestricted owner-authority agent can alter owner-accessible state and logs, these records are not tamper-resistant, cannot prove non-repudiation, and must not be used as a security or compliance audit trail.

Because full-owner-access can expose secrets through command output or model responses, the MVP does not promise complete event capture, log redaction, secret containment, cost control, or host availability. The README and `SECURITY.md` must state this prominently before showing installation instructions.

## 15. Testing Strategy

### Unit tests

- frame encoding/decoding, maximum sizes, truncation, and malformed input;
- protocol handshake and version rejection;
- Unix UID-to-member mapping;
- message idempotency and room sequence monotonicity;
- FIFO queue and single-active-turn state machine;
- steer validation against an expected active turn ID;
- durable-event replay and duplicate suppression;
- slow-client buffer eviction;
- daemon restart state reconstruction;
- `needs-review` queue freezing and all explicit recovery decisions;
- initial thread creation uncertainty and `thread-needs-repair` behavior;
- stale `/cancel` rejection;
- Codex/App Server version allowlist and schema-compatibility rejection;
- App Server child-generation and orphan handling.

### Integration tests

A deterministic fake App Server implements the narrow JSON-RPC surface. Tests start a real daemon plus three bridge/client processes and verify:

- all three distinct Unix identities join the same room;
- all clients receive the same ordered history;
- simultaneous messages are accepted once and automatically dispatched once in FIFO order during uninterrupted operation;
- only one turn is active;
- explicit steer reaches the active turn and stale steer is rejected;
- disconnecting one client does not stop the turn;
- reconnect replays missed events;
- App Server crash/restart resumes the same thread;
- daemon crash/restart preserves accepted messages and detects ambiguous work;
- large or malformed frames cannot corrupt other connections.

A Linux OpenSSH integration fixture starts an isolated `sshd` with three test accounts and real keys. It verifies the fixed remote command, no-PTY mode, peer UID mapping, clean stdout framing, host-key checking, three simultaneous SSH sessions, and disconnect/reconnect behavior. A separate documented three-machine manual acceptance script validates the same path on a real project host. macOS covers peer credentials and daemon behavior in CI, while the first release milestone treats the Linux three-machine path as the primary deployment.

OS peer-credential tests run on supported Linux and macOS CI variants. Tests that cannot create multiple operating-system users use an injectable credential provider only in test builds; production code always reads kernel peer credentials.

### Real Codex end-to-end test

A gated, opt-in test uses a temporary Git repository and the locally authenticated Codex installation. It is excluded from default CI so contributors do not unexpectedly consume model quota. It verifies thread creation, a harmless file edit, client reconnect, daemon restart, and resume of the same thread.

### MVP acceptance criteria

1. Three users on three machines can connect through independent SSH identities.
2. They see the same room history and captured live Codex events.
3. Concurrent submissions have deterministic acceptance order; each is automatically dispatched once during uninterrupted operation.
4. Ordinary messages queue behind an active turn; `/steer` is explicit.
5. SSH disconnect does not stop active work.
6. Client reconnect recovers missed durable state.
7. Daemon and App Server restarts restore the same Codex thread ID and completed history; they do not promise continuation of an in-flight turn.
8. Ambiguous dispatch is never retried automatically, and the queue remains frozen until an explicit recovery decision.
9. On an allowlisted Codex/App Server version, every agent request uses `approvalPolicy: "never"`, thread sandbox `danger-full-access`, and turn sandbox policy `dangerFullAccess` under the execution owner's actual daemon-service authority; an unknown version or a failure to establish that mode stops visibly before dispatch.
10. Human authorship remains visible as best-effort, owner-tamperable attribution.
11. The default test suite performs no paid model calls.

## 16. Open-Source Deliverables

The repository will include:

- Go source for all three subcommands and shared protocol packages;
- versioned protocol documentation;
- database migration files;
- fake App Server and automated tests;
- `README.md` with build, host initialization, three-user SSH setup, operation, and recovery;
- `SECURITY.md` describing the full-owner-access trust model;
- `CONTRIBUTING.md` and code-of-conduct information;
- Apache-2.0 `LICENSE`;
- GitHub Actions for formatting, vet/static analysis, unit tests, integration tests, and supported-OS builds;
- release checks for accidental credentials and sensitive local paths.

After implementation and local verification, the repository will be created as a public GitHub repository named `agent_romm`. Its Go module path will use the actual GitHub repository URL returned by that creation step. No remote repository is created during the design phase.

Delivery is split into four milestones so the first implementation plan remains executable:

1. **Core local MVP:** protocol, SQLite state, fake App Server, one daemon, local bridge/client process tests, queue, streaming projection, and recovery state machines.
2. **Linux three-machine acceptance:** real OpenSSH fixture, three Unix identities, owner-host deployment, Codex integration, service example, and manual three-machine script.
3. **macOS compatibility:** peer credentials, process lifecycle, launchd example, and cross-platform release builds.
4. **Open-source release:** README, security/trust documentation, contributor files, Apache-2.0 licensing, CI hardening, secret scan, public GitHub repository creation, and initial push.

## 17. Explicit Non-Goals

The MVP does not include:

- a web UI, desktop UI, or full-screen terminal UI;
- a hosted relay, public HTTP API, or public WebSocket listener;
- more than one project or room per daemon;
- multiple active Codex turns;
- parallel agents, thread merging, or Git worktree orchestration;
- private messages or per-member transcript visibility;
- per-user Codex authentication, billing, sandbox, secrets, or tool policy;
- a permission intersection between the initiating member and execution owner;
- per-action approval prompts in the intended full-access mode;
- container or virtual-machine isolation from the execution owner;
- automatic Git commit, push, deploy, or destructive recovery;
- cross-host room handoff or high availability.

## 18. Implementation Boundaries

The first implementation plan should preserve these package boundaries:

- `cmd`: CLI subcommand wiring only;
- `protocol`: framed transport types and compatibility rules;
- `client`: terminal input, rendering, SSH subprocess, reconnect;
- `bridge`: stdio-to-Unix-socket relay;
- `daemon`: connection lifecycle and room coordinator;
- `identity`: Unix peer credentials and UID membership mapping;
- `store`: SQLite schema, migrations, transactions, replay;
- `codex`: App Server child lifecycle and narrow JSON-RPC adapter;
- `room`: message, queue, turn, and recovery state machines;
- `gitview`: read-only diff/status projection for the CLI.

Each package exposes interfaces that can be exercised with in-memory or fake dependencies. App Server events enter the room state machine through one adapter boundary; terminal rendering never consumes raw Codex JSON-RPC directly.

## 19. Deferred Evolution

If the shared-room MVP proves useful, later versions may add:

1. multiple rooms and projects on one host;
2. a persistent remote runner protocol with outbound connections;
3. TLS application authentication for hosts without shared SSH accounts;
4. task forks and independent Git worktrees for safe parallelism;
5. structured result summaries returned to a coordinator thread;
6. restricted execution modes and per-member capabilities;
7. execution-owner handoff with explicit credential and workspace migration;
8. optional hosted discovery or relay without exposing App Server.

These extensions must preserve the scheduler invariant that `agent_romm` dispatches at most one turn per room at a time. Strong filesystem exclusivity would require additional process or workspace isolation and is not promised by the full-owner-access MVP.
