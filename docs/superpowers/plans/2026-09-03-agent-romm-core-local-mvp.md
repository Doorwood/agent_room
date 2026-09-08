# agent_romm Core Local MVP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a deterministic, local, no-paid-call vertical slice of `agent_romm` that exercises the real room, persistence, protocol, daemon, bridge, client, and recovery semantics against a fake Codex App Server.

**Architecture:** One daemon owns a single room coordinator and serializes all Codex mutations. SQLite commits accepted work and durable events atomically; framed clients connect through byte-only bridge processes; a narrow Codex adapter talks JSONL JSON-RPC to a supervised App Server child. Milestone 1 uses dependency injection only inside tests for identities and external processes, while production constructors remain fail-closed.

**Tech Stack:** Go 1.24.0, Go standard library, `modernc.org/sqlite` v1.45.0, `golang.org/x/sys` v0.37.0, SQLite WAL, OpenSSH subprocess boundary, Codex App Server JSONL JSON-RPC v2 fixture.

**Spec:** [`docs/superpowers/specs/2026-09-03-agent-romm-cli-mvp-design.md`](../specs/2026-09-03-agent-romm-cli-mvp-design.md)

## Global Constraints

- The repository and executable name is exactly `agent_romm`; the temporary local Go module path is `agent_romm` until the public GitHub namespace exists.
- Go is pinned to `1.24.0`. `modernc.org/sqlite` is pinned to `v1.45.0`, the newest checked version whose module declares Go 1.24 compatibility. `golang.org/x/sys` is pinned to `v0.37.0`.
- One daemon owns exactly one room, one persistent Codex thread, and at most one dispatched turn.
- Every Codex thread request uses `approvalPolicy: "never"` and `sandbox: "danger-full-access"`; every turn uses `approvalPolicy: "never"` and `sandboxPolicy: {"type":"dangerFullAccess"}`.
- The first compatibility allowlist contains exactly `codex-cli 0.151.0-alpha.7.2`. Unknown CLI versions or incompatible App Server `initialize.userAgent` values stop before thread resume or turn dispatch.
- The canonical project path is an absolute, symlink-resolved Git worktree root and is injected by the daemon; no client payload can override identity, author, project path, `cwd`, sandbox, approval policy, model, or provider.
- All default tests use the fake App Server and make no paid model call. A real Codex invocation, real `sshd`, three Unix accounts, three machines, service installation, and GitHub publication belong to later plans.
- Test identity and process substitution enter only through Go interfaces supplied by `_test.go` code. There is no public `--as-user`, `--fake`, or `--local` bypass.
- Production process supervision and peer credentials in M1 are Linux-only. Every `!linux` production constructor fails closed; macOS runs the local demo only through test-injected process and peer fakes until Milestone 3.
- Mutating-call uncertainty never causes an automatic retry. `DeliveryUnknown` enters `needs-review`; initial thread uncertainty enters `thread-needs-repair`.
- Client message IDs, framed request IDs, and server-generated connection IDs are exactly 32 lowercase hexadecimal characters (128 bits). Invalid IDs are rejected before persistence, dispatch, or logging.
- A stable Codex runtime proxy owns replaceable App Server sessions. Its event channel survives child crashes, restart backoff is bounded, and it never retries a mutating JSON-RPC call.
- Durable events alone receive `room_seq`; streaming deltas remain transient and are never replayed as completed output.
- Each task follows red-green-refactor, runs focused tests plus affected package tests, and ends with one reviewable commit.

---

## Scope and Exit Boundary

This plan produces the **Core local engine demo**, not the three-machine user release. Its end-to-end test starts a real `agent_romm` daemon server, Unix socket, SQLite database, fake App Server child, and three local client/bridge processes. A test-only peer resolver assigns deterministic synthetic members because all processes normally share the developer's UID.

The next plan replaces those injected boundaries with an isolated Linux `sshd`, three real accounts and keys, the real Codex binary, system service configuration, and a three-machine acceptance script. Milestone 1 must not add alternate public transports or weaken production identity checks merely to make its demo convenient.

## File Map

```text
go.mod
go.sum
.gitignore
cmd/agent_romm/main.go

internal/room/types.go
internal/room/ports.go
internal/room/coordinator.go
internal/room/commands.go
internal/room/projection.go
internal/room/recovery.go
internal/room/coordinator_test.go
internal/room/recovery_test.go

internal/protocol/envelope.go
internal/protocol/messages.go
internal/protocol/framing.go
internal/protocol/validation.go
internal/protocol/framing_test.go
internal/protocol/protocol_test.go
internal/protocol/fuzz_test.go

internal/store/sqlite.go
internal/store/migrations.go
internal/store/schema/001_initial.sql
internal/store/repository.go
internal/store/recovery.go
internal/store/store_test.go
internal/store/recovery_test.go

internal/codex/compat.go
internal/codex/jsonrpc.go
internal/codex/protocol.go
internal/codex/adapter.go
internal/codex/requests.go
internal/codex/runtime.go
internal/codex/supervisor.go
internal/codex/process.go
internal/codex/process_linux.go
internal/codex/process_unsupported.go
internal/codex/adapter_test.go
internal/codex/jsonrpc_test.go
internal/codex/runtime_test.go
internal/codex/supervisor_test.go
internal/codex/testdata/schema-0.151.0-alpha.7.2.sha256
internal/codex/testdata/golden/*.json
internal/codex/testdata/fakeappserver/main.go

internal/identity/peer.go
internal/identity/peer_linux.go
internal/identity/peer_unsupported.go
internal/identity/peer_test.go

internal/daemon/server.go
internal/daemon/session.go
internal/daemon/hub.go
internal/daemon/replay.go
internal/daemon/server_test.go
internal/daemon/replay_test.go
internal/observability/logger.go
internal/observability/logger_test.go

internal/config/layout.go
internal/config/config.go
internal/config/config_test.go
internal/admin/init.go
internal/admin/lock.go
internal/admin/repair.go
internal/admin/init_test.go
internal/admin/repair_test.go
internal/gitview/git.go
internal/gitview/git_test.go

internal/bridge/relay.go
internal/bridge/relay_test.go
internal/client/client.go
internal/client/commands.go
internal/client/render.go
internal/client/cursor.go
internal/client/ssh.go
internal/client/client_test.go
internal/client/commands_test.go
internal/client/ssh_test.go

internal/cli/run.go
internal/cli/run_test.go
internal/integration/local_process_test.go
internal/integration/testdata/fakessh/main.go
README.md
```

The `room` package owns domain types and interfaces. `store` and `codex` implement those ports. `daemon` authenticates connections, replays durable events, and applies backpressure; it does not duplicate room state transitions. `client` parses user commands and projects protocol events; it never consumes raw Codex JSON-RPC.

## Shared Interface Contract

Task 1 creates the following named types and ports. Later tasks must use these names exactly rather than introducing parallel representations.

```go
package room

type UID uint32
type Seq uint64
type RoomID string
type ClientMessageID string
type ThreadID string
type TurnID string
type ItemID string
type ConnectionID string

type RequestState string
const (
    RequestQueued      RequestState = "queued"
    RequestDispatching RequestState = "dispatching"
    RequestRunning     RequestState = "running"
    RequestCompleted   RequestState = "completed"
    RequestFailed      RequestState = "failed"
    RequestInterrupted RequestState = "interrupted"
    RequestNeedsReview RequestState = "needs-review"
)

type RoomStatus string
const (
    RoomReady             RoomStatus = "ready"
    RoomRecovering        RoomStatus = "recovering"
    RoomThreadNeedsRepair RoomStatus = "thread-needs-repair"
)

type Actor struct { UID UID; Name string }
type SubmitInput struct { ClientMessageID ClientMessageID; Text string }
type SteerInput struct { ClientMessageID ClientMessageID; ExpectedTurnID TurnID; Text string }
type CancelInput struct { ClientMessageID ClientMessageID; ExpectedTurnID TurnID }
type ControlKind string
const (
    ControlSteer ControlKind = "steer"
    ControlCancel ControlKind = "cancel"
)
type ControlInput struct { ClientMessageID ClientMessageID; Kind ControlKind; ExpectedTurnID TurnID; Text string }
type Acceptance struct {
    MessageID int64
    ClientMessageID ClientMessageID
    Seq Seq
    State RequestState
    ErrorCode string
    Duplicate bool
    Event DurableEvent
}

type RecoveryAction string
const (
    RecoveryRetry    RecoveryAction = "retry"
    RecoverySkip     RecoveryAction = "skip"
    RecoveryContinue RecoveryAction = "continue"
)

type RecoverInput struct {
    ClientMessageID ClientMessageID
    TargetMessageID ClientMessageID
    Action RecoveryAction
    ReplacementMessageID ClientMessageID
    Instruction string
}

type RetryKind string
const (
    RetryPrompt RetryKind = "prompt"
    RetrySteer  RetryKind = "steer"
    RetryCancel RetryKind = "cancel"
)
type RetryCommand struct { Kind RetryKind; ClientMessageID ClientMessageID; Actor Actor; ExpectedTurnID TurnID; Text string }
type RecoveryResult struct { Duplicate bool; Events []DurableEvent; Retry *RetryCommand }

type DeliveryCertainty string
const (
    DeliveryNotSent DeliveryCertainty = "not-sent"
    DeliveryUnknown DeliveryCertainty = "unknown"
)

type MutationError struct { Operation string; Certainty DeliveryCertainty; Err error }
func (e *MutationError) Error() string { return e.Operation + ": " + e.Err.Error() }
func (e *MutationError) Unwrap() error { return e.Err }
```

```go
package room

type Repository interface {
    LoadRecoveryImage(context.Context, RoomID) (RecoveryImage, error)
    FindMember(context.Context, RoomID, UID) (Member, error)
    AcceptMessage(context.Context, RoomID, Actor, SubmitInput) (Acceptance, error)
    AppendNote(context.Context, RoomID, Actor, SubmitInput) (Acceptance, error)
    AcceptControl(context.Context, RoomID, Actor, ControlInput) (Acceptance, error)
    FinishControl(context.Context, RoomID, ClientMessageID, ControlOutcome) (DurableEvent, error)
    BeginDispatch(context.Context, RoomID, ClientMessageID) error
    FailDispatch(context.Context, RoomID, ClientMessageID, FailureOutcome) (DurableEvent, error)
    BindRunningTurn(context.Context, RoomID, ClientMessageID, TurnID) (DurableEvent, error)
    RecordCompletedItem(context.Context, RoomID, CompletedItem) (DurableEvent, error)
    FinishTurn(context.Context, RoomID, FinishTurnInput) ([]DurableEvent, error)
    MarkNeedsReview(context.Context, RoomID, ClientMessageID, ReviewReason) (DurableEvent, error)
    ResolveReview(context.Context, RoomID, Actor, RecoverInput) (RecoveryResult, error)
    BindThread(context.Context, RoomID, ThreadSnapshot) (DurableEvent, error)
    MarkThreadNeedsRepair(context.Context, RoomID, RepairReason) (DurableEvent, error)
    SetRoomStatus(context.Context, RoomID, RoomStatus) (*DurableEvent, error)
    SaveRuntimeCheckpoint(context.Context, RoomID, RuntimeCheckpoint) error
    LatestSeq(context.Context, RoomID) (Seq, error)
    Events(context.Context, RoomID, Seq, Seq, int) ([]DurableEvent, error)
}

type Agent interface {
    StartThread(context.Context) (ThreadSnapshot, error)
    ReadThread(context.Context, ThreadID) (ThreadSnapshot, error)
    ResumeThread(context.Context, ThreadID) (ThreadSnapshot, error)
    StartTurn(context.Context, ThreadID, ClientMessageID, string) (TurnID, error)
    SteerTurn(context.Context, ThreadID, TurnID, string) error
    InterruptTurn(context.Context, ThreadID, TurnID) error
    Events() <-chan AgentEvent
}

type EventSink interface {
    PublishDurable(DurableEvent)
    PublishTransient(TransientEvent)
}

func NewCoordinator(roomID RoomID, projectRoot string, repository Repository, agent Agent, sink EventSink, clock Clock) (*Coordinator, error)
func (c *Coordinator) Recover(context.Context) error
func (c *Coordinator) Run(context.Context) error
func (c *Coordinator) Submit(context.Context, Actor, SubmitInput) (Acceptance, error)
func (c *Coordinator) Note(context.Context, Actor, SubmitInput) (Acceptance, error)
func (c *Coordinator) Steer(context.Context, Actor, SteerInput) (Acceptance, error)
func (c *Coordinator) Cancel(context.Context, Actor, CancelInput) (Acceptance, error)
func (c *Coordinator) Resolve(context.Context, Actor, RecoverInput) error
func (c *Coordinator) Snapshot(context.Context) (Snapshot, error)
```

### Task 1: Bootstrap the module and room domain contract

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `internal/room/types.go`
- Create: `internal/room/ports.go`
- Test: `internal/room/types_test.go`

**Interfaces:**
- Consumes: only Go standard-library `context`, `encoding/json`, `errors`, and `time`.
- Produces: all named domain types and the `Repository`, `Agent`, `EventSink`, `Clock`, and `IDSource` interfaces used by Tasks 3–11.

- [ ] **Step 1: Write the failing stable-value and validation tests**

```go
func TestPersistedEnumValuesAreStable(t *testing.T) {
    got, err := json.Marshal([]RequestState{
        RequestQueued, RequestDispatching, RequestRunning,
        RequestCompleted, RequestFailed, RequestInterrupted, RequestNeedsReview,
    })
    if err != nil { t.Fatal(err) }
    want := `["queued","dispatching","running","completed","failed","interrupted","needs-review"]`
    if string(got) != want { t.Fatalf("got %s want %s", got, want) }
}

func TestSubmitInputRejectsBlankText(t *testing.T) {
    err := (SubmitInput{ClientMessageID: "00000000000000000000000000000001", Text: " \n"}).Validate()
    if !errors.Is(err, ErrBlankMessage) { t.Fatalf("got %v", err) }
}
```

- [ ] **Step 2: Run the focused test and verify the package does not yet compile**

Run: `go test ./internal/room -run 'TestPersistedEnumValuesAreStable|TestSubmitInputRejectsBlankText'`

Expected: FAIL because `RequestState` and `SubmitInput` do not exist.

- [ ] **Step 3: Add the module and exact domain types**

```go
module agent_romm

go 1.24.0
```

```gitignore
/bin/
/.agent_romm/
*.test
```

Create the types in the Shared Interface Contract, plus these concrete records in `types.go`:

```go
var ErrBlankMessage = errors.New("message text is blank")
var ErrInvalidClientMessageID = errors.New("client message id must be 32 lowercase hexadecimal characters")

func (in SubmitInput) Validate() error {
    if !ValidClientMessageID(in.ClientMessageID) { return ErrInvalidClientMessageID }
    if strings.TrimSpace(in.Text) == "" { return ErrBlankMessage }
    return nil
}

func ValidClientMessageID(id ClientMessageID) bool {
    if len(id) != 32 { return false }
    for _, b := range []byte(id) { if !('0' <= b && b <= '9') && !('a' <= b && b <= 'f') { return false } }
    return true
}

type Member struct { UID UID; Name string }
type DurableEvent struct { Seq Seq; Kind string; ActorUID UID; Payload json.RawMessage; CreatedAt time.Time }
type TransientEvent struct { Revision uint64; Kind string; ThreadID ThreadID; TurnID TurnID; ItemID ItemID; Delta string }
type CompletedItem struct { ThreadID ThreadID; TurnID TurnID; ItemID ItemID; Payload json.RawMessage }
type TurnSnapshot struct { ID TurnID; State RequestState; Items []CompletedItem }
type ThreadSnapshot struct { ID ThreadID; CWD string; Turns []TurnSnapshot }
type TurnBinding struct { MessageID ClientMessageID; TurnID TurnID; State RequestState }
type QueuedMessage struct { Input SubmitInput; Actor Actor; AcceptedSeq Seq }
type RuntimeProcessState string
const (
    RuntimeProcessRunning RuntimeProcessState = "running"
    RuntimeProcessStopped RuntimeProcessState = "stopped"
)
type RuntimeCheckpoint struct { State RuntimeProcessState; Generation string; PID int; PGID int; ProcessStart string; CodexVersion string; SchemaSHA256 string }
type RecoveryImage struct { Status RoomStatus; ThreadID ThreadID; Active *TurnBinding; Queue []QueuedMessage; Checkpoint RuntimeCheckpoint }
type FailureOutcome struct { State RequestState; ErrorCode string; ErrorDigest string }
type ControlOutcome struct { State RequestState; ErrorCode string; ErrorDigest string }
type FinishTurnInput struct { TurnID TurnID; State RequestState; ErrorCode string; ErrorDigest string }
type ReviewReason struct { Code string; DetailDigest string }
type RepairReason struct { Code string; DetailDigest string }
type AgentEvent struct { Kind string; ThreadID ThreadID; TurnID TurnID; ItemID ItemID; Delta string; Completed *CompletedItem; Error error }
type LiveItemSnapshot struct { ThreadID ThreadID; TurnID TurnID; ItemID ItemID; Partial string }
type Snapshot struct { Status RoomStatus; ThreadID ThreadID; Active *TurnBinding; Queue []QueuedMessage; ProjectionRevision uint64; LiveItems []LiveItemSnapshot; LatestSeq Seq }
type ConnectionRecord struct { ID ConnectionID; RoomID RoomID; UID UID; ConnectedAt time.Time; DisconnectedAt *time.Time; LastAck Seq }
```

Create `ports.go` with the two port interfaces shown above and:

```go
type Clock interface { Now() time.Time }
type IDSource interface {
    NewClientMessageID() (ClientMessageID, error)
    NewConnectionID() (ConnectionID, error)
}
```

- [ ] **Step 4: Format and run the whole room package test**

Run: `gofmt -w internal/room && go test ./internal/room`

Expected: PASS.

- [ ] **Step 5: Commit the domain contract**

```bash
git add .gitignore go.mod internal/room
git commit -m "feat: define room domain contract"
```

### Task 2: Implement strict framed protocol and handshake

**Files:**
- Create: `internal/protocol/envelope.go`
- Create: `internal/protocol/messages.go`
- Create: `internal/protocol/framing.go`
- Create: `internal/protocol/validation.go`
- Test: `internal/protocol/framing_test.go`
- Test: `internal/protocol/protocol_test.go`
- Test: `internal/protocol/fuzz_test.go`

**Interfaces:**
- Consumes: `io.Reader`, `io.Writer`, and JSON payload types.
- Produces: `Reader.Read() (Envelope, error)`, `Writer.Write(Envelope) error`, strict `DecodeBody`, and version-1 hello/welcome types for daemon and client.

- [ ] **Step 1: Write framing boundary tests**

```go
func TestReaderHandlesFragmentedAndAdjacentFrames(t *testing.T) {
    first := mustFrame(t, Envelope{Version: 1, Kind: KindRequest, ID: "00000000000000000000000000000001", Method: "hello", Body: json.RawMessage(`{"minVersion":1,"maxVersion":1}`)})
    second := mustFrame(t, Envelope{Version: 1, Kind: KindRequest, ID: "00000000000000000000000000000002", Method: "status", Body: json.RawMessage(`{}`)})
    r := NewReader(&oneByteReader{r: bytes.NewReader(append(first, second...))}, MaxFrameBytes)
    if got, err := r.Read(); err != nil || got.ID != "00000000000000000000000000000001" { t.Fatalf("first: %#v %v", got, err) }
    if got, err := r.Read(); err != nil || got.ID != "00000000000000000000000000000002" { t.Fatalf("second: %#v %v", got, err) }
}

func TestReaderRejectsOversizeBeforeBodyAllocation(t *testing.T) {
    var prefix [4]byte
    binary.BigEndian.PutUint32(prefix[:], MaxFrameBytes+1)
    _, err := NewReader(bytes.NewReader(prefix[:]), MaxFrameBytes).Read()
    if !errors.Is(err, ErrFrameTooLarge) { t.Fatalf("got %v", err) }
}
```

Add `Validate` methods for `SteerInput`, `CancelInput`, `ControlInput`, and `RecoverInput`. Every client and replacement message ID must pass `ValidClientMessageID`; steer/cancel require `ExpectedTurnID`; steer requires nonblank text. Recover requires a valid target ID plus one known action, forbids replacement fields for retry/skip, and requires a distinct valid replacement ID plus nonblank instruction for continue.

Also table-test zero length, exactly 8 MiB, `0xffffffff`, EOF inside prefix/body, invalid UTF-8, malformed JSON, duplicate object keys, trailing JSON, invalid frame kind, and request IDs that are empty, too long, uppercase, non-hex, or contain control characters. Request/response IDs and every client message ID must pass the exact `^[0-9a-f]{32}$` predicate; the validator is a byte loop rather than a regexp on an unbounded string.

- [ ] **Step 2: Run the framing tests and verify failure**

Run: `go test ./internal/protocol -run 'TestReader'`

Expected: FAIL because the protocol reader is undefined.

- [ ] **Step 3: Implement allocation-safe framing and strict envelope decoding**

```go
const Version uint16 = 1
const MaxFrameBytes uint32 = 8 << 20

type Kind string
const (
    KindRequest Kind = "request"
    KindResponse Kind = "response"
    KindEvent Kind = "event"
    KindError Kind = "error"
)

type Envelope struct {
    Version uint16 `json:"version"`
    Kind Kind `json:"kind"`
    ID string `json:"id,omitempty"`
    Method string `json:"method"`
    Requires []string `json:"requires,omitempty"`
    Seq *uint64 `json:"seq,omitempty"`
    Body json.RawMessage `json:"body"`
}

func (r *Reader) Read() (Envelope, error) {
    var prefix [4]byte
    if _, err := io.ReadFull(r.source, prefix[:]); err != nil { return Envelope{}, err }
    n := binary.BigEndian.Uint32(prefix[:])
    if n == 0 { return Envelope{}, ErrEmptyFrame }
    if n > r.maximum { return Envelope{}, ErrFrameTooLarge }
    body := make([]byte, int(n))
    if _, err := io.ReadFull(r.source, body); err != nil { return Envelope{}, err }
    if !utf8.Valid(body) { return Envelope{}, ErrInvalidUTF8 }
    return decodeEnvelope(body)
}
```

`decodeEnvelope` rejects duplicate object keys before using `json.Decoder.DisallowUnknownFields()`, requires the next decode to return `io.EOF`, and calls `Envelope.Validate()` before returning. `Writer` owns one mutex; `Writer.Write` holds it continuously while validating, marshaling, checking the encoded length, and calling `writeFull` for both the four-byte prefix and the body. No response, event, or heartbeat can interleave bytes with another frame on the same connection.

Add `shortWriter` and a concurrency-gated writer test: force partial writes in both prefix and body, then launch response/event writes together and decode the resulting stream into exactly two intact envelopes under `go test -race`.

- [ ] **Step 4: Add handshake and payload validation tests**

```go
func TestSessionRequiresHelloFirst(t *testing.T) {
    state := NewHandshakeState([]string{"resume-v1"})
    err := state.Accept(Envelope{Version: Version, Kind: KindRequest, ID: "00000000000000000000000000000001", Method: "submit", Body: json.RawMessage(`{}`)})
    if !errors.Is(err, ErrHandshakeRequired) { t.Fatalf("got %v", err) }
}

func TestHandshakeRejectsUnknownRequiredCapability(t *testing.T) {
    state := NewHandshakeState([]string{"resume-v1"})
    err := state.Accept(Envelope{Version: Version, Kind: KindRequest, ID: "00000000000000000000000000000001", Method: "hello", Requires: []string{"root-shell-v9"}, Body: json.RawMessage(`{"minVersion":1,"maxVersion":1}`)})
    if !errors.Is(err, ErrUnsupportedCapability) { t.Fatalf("got %v", err) }
}
```

Define strict payloads for `hello`, `welcome`, `submit`, `note`, `steer`, `cancel`, `recover`, `queue`, `status`, `who`, `diff`, `ack`, `heartbeat`, and `close`. Identity and execution fields do not appear in any client request type.

```go
type Hello struct { MinVersion uint16 `json:"minVersion"`; MaxVersion uint16 `json:"maxVersion"`; LastAppliedSeq uint64 `json:"lastAppliedSeq"` }
type Welcome struct { RoomID string `json:"roomId"`; RoomName string `json:"roomName"`; ProjectRoot string `json:"projectRoot"`; ExecutionOwner string `json:"executionOwner"`; FullOwnerAccess bool `json:"fullOwnerAccess"`; ActiveTurnID string `json:"activeTurnId,omitempty"`; LatestSeq uint64 `json:"latestSeq"` }
type SubmitRequest struct { ClientMessageID string `json:"clientMessageId"`; Text string `json:"text"` }
type SteerRequest struct { ClientMessageID string `json:"clientMessageId"`; ExpectedTurnID string `json:"expectedTurnId"`; Text string `json:"text"` }
type CancelRequest struct { ClientMessageID string `json:"clientMessageId"`; ExpectedTurnID string `json:"expectedTurnId"` }
type RecoverRequest struct { ClientMessageID string `json:"clientMessageId"`; TargetMessageID string `json:"targetMessageId"`; Action string `json:"action"`; ReplacementMessageID string `json:"replacementMessageId,omitempty"`; Instruction string `json:"instruction,omitempty"` }
type LiveItem struct { ThreadID string `json:"threadId"`; TurnID string `json:"turnId"`; ItemID string `json:"itemId"`; Partial string `json:"partial"` }
type RuntimeSnapshot struct { ProjectionRevision uint64 `json:"projectionRevision"`; LiveItems []LiveItem `json:"liveItems"` }
type Ack struct { Seq uint64 `json:"seq"` }
type Heartbeat struct { UnixMilli int64 `json:"unixMilli"` }
type Empty struct{}
```

`DecodeBody[T]` performs the same duplicate-key, unknown-field, trailing-value, and UTF-8 checks as the envelope decoder. `note` uses `SubmitRequest`; query and close methods use `Empty`. Validate the exact 32-lowercase-hex ID format, nonblank text, and require `replacementMessageId` plus instruction only for recovery action `continue`. Server connection IDs use the same crypto-random generator and format.

- [ ] **Step 5: Add a non-panicking fuzz target**

```go
func FuzzReader(f *testing.F) {
    f.Add([]byte{0, 0, 0, 2, '{', '}'})
    f.Fuzz(func(t *testing.T, data []byte) {
        _, _ = NewReader(bytes.NewReader(data), MaxFrameBytes).Read()
    })
}
```

- [ ] **Step 6: Run protocol tests, race detector, and the seed fuzz corpus**

Run: `gofmt -w internal/protocol && go test -race ./internal/protocol && go test ./internal/protocol -run=^$ -fuzz=FuzzReader -fuzztime=2s`

Expected: PASS with no panic or allocation proportional to an unvalidated prefix.

- [ ] **Step 7: Commit the protocol**

```bash
git add internal/protocol
git commit -m "feat: add strict framed room protocol"
```

### Task 3: Add atomic SQLite persistence and replay

**Files:**
- Modify: `go.mod`
- Create: `go.sum`
- Create: `internal/store/sqlite.go`
- Create: `internal/store/migrations.go`
- Create: `internal/store/schema/001_initial.sql`
- Create: `internal/store/repository.go`
- Create: `internal/store/recovery.go`
- Test: `internal/store/store_test.go`
- Test: `internal/store/recovery_test.go`

**Interfaces:**
- Consumes: all methods of `room.Repository` from Task 1.
- Produces: `store.Open(context.Context, string) (*Store, error)`, `Store.InitializeRoom`, `Store.Close() error`, and a complete `room.Repository` implementation.

- [ ] **Step 1: Pin the SQLite driver and write atomic acceptance tests**

Run: `go get modernc.org/sqlite@v1.45.0`

```go
func TestAcceptMessageIsIdempotentAndAtomic(t *testing.T) {
    s := openTestStore(t)
    seedRoomAndMember(t, s, "team", 1001, "alice")
    input := room.SubmitInput{ClientMessageID: "00000000000000000000000000000001", Text: "inspect auth"}
    first, err := s.AcceptMessage(context.Background(), "team", room.Actor{UID: 1001, Name: "alice"}, input)
    if err != nil { t.Fatal(err) }
    second, err := s.AcceptMessage(context.Background(), "team", room.Actor{UID: 1001, Name: "alice"}, input)
    if err != nil { t.Fatal(err) }
    if !second.Duplicate || second.MessageID != first.MessageID || second.Seq != first.Seq { t.Fatalf("first=%#v second=%#v", first, second) }
    assertCounts(t, s, 1, 1)
}

func TestAcceptMessageConflictsWhenKeyIsReused(t *testing.T) {
    s := openTestStore(t)
    seedRoomAndMember(t, s, "team", 1001, "alice")
    _, _ = s.AcceptMessage(context.Background(), "team", room.Actor{UID: 1001, Name: "alice"}, room.SubmitInput{ClientMessageID: "00000000000000000000000000000001", Text: "first"})
    _, err := s.AcceptMessage(context.Background(), "team", room.Actor{UID: 1001, Name: "alice"}, room.SubmitInput{ClientMessageID: "00000000000000000000000000000001", Text: "changed"})
    if !errors.Is(err, store.ErrIdempotencyConflict) { t.Fatalf("got %v", err) }
}
```

- [ ] **Step 2: Run the store test and verify failure**

Run: `go test ./internal/store -run 'TestAcceptMessage'`

Expected: FAIL because `store.Open` and the schema do not exist.

- [ ] **Step 3: Create the initial schema with explicit constraints**

```sql
PRAGMA foreign_keys = ON;

CREATE TABLE schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);
CREATE TABLE rooms (
  id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL,
  host_id TEXT NOT NULL,
  project_path TEXT NOT NULL,
  execution_owner_uid INTEGER NOT NULL,
  codex_thread_id TEXT,
  status TEXT NOT NULL CHECK (status IN ('ready','recovering','thread-needs-repair')),
  next_seq INTEGER NOT NULL DEFAULT 1,
  schema_version INTEGER NOT NULL
);
CREATE TABLE members (
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  uid INTEGER NOT NULL,
  username TEXT NOT NULL,
  added_at TEXT NOT NULL,
  PRIMARY KEY (room_id, uid),
  UNIQUE (room_id, username)
);
CREATE TABLE messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  client_message_id TEXT NOT NULL,
  actor_uid INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('prompt','note','steer','cancel','recover','recovery-prompt')),
  body TEXT NOT NULL,
  payload_hash BLOB NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('queued','dispatching','running','completed','failed','interrupted','needs-review')),
  accepted_seq INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE (room_id, client_message_id)
);
CREATE TABLE turn_bindings (
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  message_id INTEGER NOT NULL UNIQUE REFERENCES messages(id) ON DELETE CASCADE,
  codex_turn_id TEXT,
  state TEXT NOT NULL CHECK (state IN ('queued','dispatching','running','completed','failed','interrupted','needs-review')),
  started_at TEXT,
  completed_at TEXT,
  error_code TEXT,
  error_digest TEXT,
  PRIMARY KEY (room_id, message_id)
);
CREATE TABLE client_connections (
  id TEXT PRIMARY KEY,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  member_uid INTEGER NOT NULL,
  connected_at TEXT NOT NULL,
  disconnected_at TEXT,
  last_ack_seq INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE room_events (
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  actor_uid INTEGER NOT NULL,
  kind TEXT NOT NULL,
  payload BLOB NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (room_id, seq)
);
CREATE TABLE runtime_checkpoints (
  room_id TEXT PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
  generation TEXT NOT NULL,
  pid INTEGER NOT NULL,
  pgid INTEGER NOT NULL,
  process_start TEXT NOT NULL,
  codex_version TEXT NOT NULL,
  schema_sha256 TEXT NOT NULL,
  process_state TEXT NOT NULL CHECK (process_state IN ('running','stopped'))
);
```

Embed migrations with `//go:embed schema/*.sql`. `Open` uses driver name `sqlite`, enables WAL, foreign keys, a 5-second busy timeout, and calls `db.SetMaxOpenConns(1)`.

Initialization uses this one-shot API and fails if the database already contains a room:

```go
type RoomSeed struct { ID room.RoomID; DisplayName string; HostID string; ProjectRoot string; ExecutionOwnerUID room.UID; Members []room.Member }
func (s *Store) InitializeRoom(context.Context, RoomSeed) error
func (s *Store) Close() error
```

- [ ] **Step 4: Implement transaction helpers and the repository methods**

Use one transaction for each state transition. Allocate a durable sequence with:

```sql
UPDATE rooms SET next_seq = next_seq + 1 WHERE id = ? RETURNING next_seq - 1;
```

Within `AcceptMessage`, hash `kind`, `actor_uid`, and body with SHA-256; on unique conflict, load the original row and return it only when the full hash and actor match. Insert the message, turn binding, and acceptance event before commit. `AppendNote` inserts a terminal message/event without a turn binding. `AcceptControl` stores a canonical payload containing kind, expected turn, and text before the Agent call; `FinishControl` terminalizes it with a success, stale-turn, not-sent, or needs-review outcome. `FailDispatch` changes only `dispatching` to the supplied terminal state. `ResolveReview` journals the recover command and its chosen transition in one transaction, so retrying the same recovery ID returns `RecoveryResult{Duplicate:true}` and never triggers dispatch again. A first control retry moves needs-review to dispatching and returns its persisted `RetryCommand`; a prompt retry moves back to the normal FIFO queue. `SetRoomStatus` compare-and-swaps the room state and appends its event atomically. Implement every transition with predicates such as `WHERE state = 'dispatching'` and require exactly one affected row.

Persist normalized error codes and SHA-256 diagnostic digests only; never persist raw RPC error text or App Server stderr in `turn_bindings` or durable recovery payloads. Duplicate command lookup returns the stored `State` and `ErrorCode` in `Acceptance`, so a retried stale or failed control command has the same terminal response and cannot reach the Agent.

Also implement a narrow connection store used by the daemon:

```go
type ConnectionStore interface {
    Connected(context.Context, room.ConnectionRecord) error
    Ack(context.Context, room.ConnectionID, room.Seq) error
    Disconnected(context.Context, room.ConnectionID, time.Time) error
    CloseStale(context.Context, room.RoomID, time.Time) error
}
```

- [ ] **Step 5: Add concurrent duplicate, rollback, monotonic sequence, and reopen tests**

```go
func TestConcurrentDuplicateHasOneWinner(t *testing.T) {
    s := openTestStore(t)
    seedRoomAndMember(t, s, "team", 1001, "alice")
    var wg sync.WaitGroup
    results := make(chan room.Acceptance, 16)
    for range 16 {
        wg.Add(1)
        go func() {
            defer wg.Done()
            got, err := s.AcceptMessage(context.Background(), "team", room.Actor{UID: 1001, Name: "alice"}, room.SubmitInput{ClientMessageID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Text: "one"})
            if err != nil { t.Error(err); return }
            results <- got
        }()
    }
    wg.Wait()
    close(results)
    var first int64
    for got := range results { if first == 0 { first = got.MessageID }; if got.MessageID != first { t.Fatalf("different rows") } }
    assertCounts(t, s, 1, 1)
}
```

Add an unexported transaction hook used only by same-package tests to fail before commit, then assert zero messages, bindings, and events. Close and reopen the file database and assert `LoadRecoveryImage` returns the same thread, queue, binding, checkpoint, and latest sequence.

Repeat idempotency tests for note, steer, cancel, and all recover actions. Reusing one client message ID with a different kind, actor, target turn, target message, action, or body returns `ErrIdempotencyConflict` and preserves the first row.

On daemon recovery, mark every connection row whose `disconnected_at` is null as disconnected with the recovery timestamp before accepting a new connection; connection rows never confer membership or authority.

- [ ] **Step 6: Run store tests under the race detector**

Run: `gofmt -w internal/store && go test -race ./internal/store`

Expected: PASS.

- [ ] **Step 7: Commit persistence**

```bash
git add go.mod go.sum internal/store
git commit -m "feat: persist room state and durable events"
```

### Task 4: Implement JSON-RPC and the pinned Codex adapter

**Files:**
- Create: `internal/codex/compat.go`
- Create: `internal/codex/jsonrpc.go`
- Create: `internal/codex/protocol.go`
- Create: `internal/codex/adapter.go`
- Create: `internal/codex/requests.go`
- Create: `internal/codex/jsonrpc_test.go`
- Create: `internal/codex/adapter_test.go`
- Create: `internal/codex/testdata/schema-0.151.0-alpha.7.2.sha256`
- Create: `internal/codex/testdata/golden/initialize.json`
- Create: `internal/codex/testdata/golden/account-read.json`
- Create: `internal/codex/testdata/golden/thread-start.json`
- Create: `internal/codex/testdata/golden/thread-resume.json`
- Create: `internal/codex/testdata/golden/thread-read.json`
- Create: `internal/codex/testdata/golden/turn-start.json`
- Create: `internal/codex/testdata/golden/turn-steer.json`
- Create: `internal/codex/testdata/golden/turn-interrupt.json`

**Interfaces:**
- Consumes: `room.Agent`, fixed project root, an `io.Reader`/`io.Writer` pair, and a server-request callback.
- Produces: `RPCClient.Call(context.Context, string, any, any) error`, `RPCClient.Notify(context.Context, string, any) error`, and `Adapter` implementing `room.Agent`.

- [ ] **Step 1: Record the exact compatibility fixture and write golden request tests**

`schema-0.151.0-alpha.7.2.sha256` contains:

```text
codex-cli 0.151.0-alpha.7.2
codex_app_server_protocol.schemas.json sha256 31ae67beb2c94cc9509f6a71968600062dc8c6d7fe45437ed3a9129838f4d2d9
codex_app_server_protocol.v2.schemas.json sha256 a586cdc50f84c56c7654387e869b470a689796fa1c57678dcafb5921bb2d5255
generated 2026-09-03
```

```go
func TestStartTurnUsesFixedCWDAndUnrestrictedPolicy(t *testing.T) {
    rpc, recorder := newRecordingRPC(t)
    adapter, err := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
    if err != nil { t.Fatal(err) }
    recorder.reply("turn/start", `{"turn":{"id":"turn-1","status":"inProgress","items":[]}}`)
    _, err = adapter.StartTurn(context.Background(), "thread-1", "00000000000000000000000000000001", "[participant: alice]\nrun tests")
    if err != nil { t.Fatal(err) }
    assertGoldenJSON(t, "testdata/golden/turn-start.json", recorder.request("turn/start"))
}
```

The turn golden must include exactly:

```json
{"id":1,"method":"turn/start","params":{"threadId":"thread-1","input":[{"type":"text","text":"[participant: alice]\nrun tests"}],"clientUserMessageId":"00000000000000000000000000000001","cwd":"/srv/project","approvalPolicy":"never","sandboxPolicy":{"type":"dangerFullAccess"}}}
```

Thread start/resume goldens use `"sandbox":"danger-full-access"`, `"approvalPolicy":"never"`, and the same `cwd`. The pinned schema and [official App Server protocol documentation](https://learn.chatgpt.com/docs/app-server#protocol) omit the JSON-RPC `jsonrpc` member on the wire; every request, response, notification, reverse request, and reverse response golden does the same.

- [ ] **Step 2: Run adapter tests and verify failure**

Run: `go test ./internal/codex -run 'TestStartTurn|TestThread'`

Expected: FAIL because `RPCClient` and `Adapter` do not exist.

- [ ] **Step 3: Implement concurrent JSONL JSON-RPC correlation**

```go
type wireMessage struct {
    ID json.RawMessage `json:"id,omitempty"`
    Method string `json:"method,omitempty"`
    Params json.RawMessage `json:"params,omitempty"`
    Result json.RawMessage `json:"result,omitempty"`
    Error *RPCError `json:"error,omitempty"`
    Trace json.RawMessage `json:"trace,omitempty"`
    EmittedAtMs *int64 `json:"emittedAtMs,omitempty"`
}

type RPCError struct { Code int `json:"code"`; Message string `json:"message"`; Data json.RawMessage `json:"data,omitempty"` }
type CallError struct { BytesWritten int; Err error }

func (c *RPCClient) Call(ctx context.Context, method string, params any, result any) error
func (c *RPCClient) Notify(ctx context.Context, method string, params any) error
func (c *RPCClient) Events() <-chan Notification
func (c *RPCClient) Run(ctx context.Context) error
```

Use an unbuffered direct child-stdin writer, one writer mutex held for the complete JSON value plus newline, a monotonically increasing numeric request ID, and a pending map protected by a mutex. The write loop reports the exact number of request bytes accepted before failure in `CallError`; context cancellation while waiting for the writer and any serialization/validation failure report zero. The read loop uses an 8 MiB maximum JSONL line size and must distinguish responses, notifications, and server requests while calls are outstanding. Context cancellation removes the pending entry. EOF fails every pending call once.

Before unmarshaling, reject duplicate object keys and invalid UTF-8. Reject a top-level `jsonrpc` member as unknown because the pinned wire schema omits it. Require one complete JSON value with no trailing value and exactly one legal shape: response (`id` and exactly one of `result`/`error`, no method/params/trace/timestamp), notification (`method` without `id`, optionally with the schema-defined `emittedAtMs`), or server request (`method` with `id`, optionally with the schema-defined `trace`). Reject unknown response IDs, duplicate active request IDs, invalid ID types, `result` plus `error`, missing both, misplaced `trace`/`emittedAtMs`, and all other unknown top-level fields. Any such stream violation is fatal to that session and completes all pending calls exactly once.

Add table tests for zero/short/concurrent JSONL writes, an overlong line, duplicate keys, result-and-error coexistence, neither result nor error, unknown response ID, trailing JSON, malformed UTF-8, EOF, timeout before the first byte, and timeout after a partial write. Run them under `-race`.

- [ ] **Step 4: Implement typed protocol records and the fixed adapter**

```go
const approvalPolicy = "never"
const threadSandbox = "danger-full-access"
const turnSandboxType = "dangerFullAccess"

type AdapterConfig struct { ProjectRoot string }

func NewAdapter(rpc *RPCClient, cfg AdapterConfig) (*Adapter, error) {
    if !filepath.IsAbs(cfg.ProjectRoot) { return nil, ErrProjectRootNotAbsolute }
    return &Adapter{rpc: rpc, projectRoot: filepath.Clean(cfg.ProjectRoot), events: make(chan room.AgentEvent, 128)}, nil
}
```

Implement `initialize`/`initialized` with `clientInfo.name` set exactly to `agent_romm`, `account/read` with `{"refreshToken":false}`, `thread/start`, `thread/read` with `includeTurns: true`, `thread/resume`, `turn/start`, `turn/steer` with `expectedTurnId`, and `turn/interrupt`. If `account/read` returns `requiresOpenaiAuth: true` with `account: null`, initialization fails as unauthenticated; providers that return `requiresOpenaiAuth: false` remain valid. Adapter configuration exposes no sandbox, approval, model, provider, author, or alternate cwd field.

For both `thread/start` and `thread/resume`, validate the response-level `cwd` equals the adapter's canonical root, `approvalPolicy` equals `never`, and `sandbox.type` equals `dangerFullAccess`. A missing or different value is `ErrRuntimePolicyMismatch`; never retry after dropping fields. `thread/read`/resume snapshots with a different thread cwd are returned as a binding mismatch for Task 7 to place in `thread-needs-repair`.

Every mutating adapter method maps delivery certainty identically. `DeliveryNotSent` is allowed only when serialization/validation/context cancellation happens before the writer lock emits a byte, or the direct writer reports exactly zero request bytes accepted. Once any byte may have been accepted, timeout, cancellation, partial write, EOF, malformed/oversized response, unknown response ID, JSON-RPC error, result decode failure, or response policy mismatch becomes `MutationError{Certainty: DeliveryUnknown}`. Only a complete, strictly valid success response is success. Cover this matrix independently for `StartThread`, `StartTurn`, `SteerTurn`, and `InterruptTurn`; none is automatically retried. An uncertain `StartThread` is routed to `thread-needs-repair`, while the other three freeze the affected work in `needs-review`.

- [ ] **Step 5: Reject every reverse request safely**

```go
func RejectServerRequest(method string) (any, *RPCError) {
    switch method {
    case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
        return map[string]string{"decision": "cancel"}, nil
    default:
        return nil, &RPCError{Code: -32090, Message: "agent_romm does not service App Server input requests"}
    }
}
```

Test `item/tool/requestUserInput`, `item/permissions/requestApproval`, `mcpServer/elicitation/request`, `item/tool/call`, an unknown method, duplicate request IDs, and response-write failure. Each produces an `AgentEvent{Kind:"unsupported-server-request"}` so the coordinator can durably fail and interrupt the active turn. Late completion notifications must not overwrite that failure.

Compatibility validation is fail-closed. Parse `initialize.userAgent` as whitespace-delimited tokens, require the first token to split into exactly `agent_romm/<version>`, and compare `<version>` byte-for-byte to the version from the accepted `codex-cli <version>` output. This matches the pinned binary when `clientInfo.name` identifies this integration as required by the official documentation. Reject missing tokens, alternate products, leading/trailing characters, `.7.2` versus `.7.20`, and suffix/prefix collisions; substring matching is forbidden.

- [ ] **Step 6: Run JSON-RPC and adapter tests with the race detector**

Run: `gofmt -w internal/codex && go test -race ./internal/codex`

Expected: PASS and no real Codex process starts.

- [ ] **Step 7: Commit the adapter**

```bash
git add internal/codex
git commit -m "feat: add pinned Codex App Server adapter"
```

### Task 5: Supervise and restart the App Server without PID-reuse hazards

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/codex/supervisor.go`
- Create: `internal/codex/process.go`
- Create: `internal/codex/process_linux.go`
- Create: `internal/codex/process_unsupported.go`
- Create: `internal/codex/runtime.go`
- Create: `internal/codex/supervisor_test.go`
- Create: `internal/codex/runtime_test.go`
- Create: `internal/codex/testdata/fakeappserver/main.go`

**Interfaces:**
- Consumes: `room.RuntimeCheckpoint`, a pinned `VersionPolicy`, a `ProcessManager`, and a checkpoint sink closed over one room ID.
- Produces: `Supervisor.Probe`, `Supervisor.ReapPrevious`, `Supervisor.Start`, `Supervisor.Stop`, an initialized `Session`, and a stable restartable `Runtime` implementing `room.Agent`.

- [ ] **Step 1: Write fail-closed version and previous-process tests**

```go
func TestSupervisorRejectsUnknownVersionBeforeAppServer(t *testing.T) {
    pm := &fakeProcessManager{cliVersion: "codex-cli 0.152.0"}
    s := mustSupervisor(t, pm)
    _, err := s.Start(context.Background(), "/srv/project", func(context.Context, room.RuntimeCheckpoint) error { return nil })
    if !errors.Is(err, ErrUnsupportedCodexVersion) { t.Fatalf("got %v", err) }
    if pm.startCalls != 0 { t.Fatalf("started child %d times", pm.startCalls) }
}

func TestReapPreviousRefusesUnprovenPID(t *testing.T) {
    pm := &fakeProcessManager{match: ProcessMismatch}
    s := mustSupervisor(t, pm)
    err := s.ReapPrevious(context.Background(), &room.RuntimeCheckpoint{State: room.RuntimeProcessRunning, Generation: "g-1", PID: 42, PGID: 42, ProcessStart: "old"})
    if !errors.Is(err, ErrProcessIdentityMismatch) { t.Fatalf("got %v", err) }
    if pm.stopCalls != 0 { t.Fatalf("signaled a persisted process") }
}
```

Add cases for `ProcessAbsent`, `ProcessMatches`, `ProcessMismatch`, `ProcessUnknown`, a running checkpoint with PID/PGID `0`, `1`, or negative values, and identity changing while inspected. Only `ProcessAbsent` permits startup. Every other prior-generation case returns a typed owner-repair error and records zero calls to `StopCurrent`; persisted metadata is never a signal target.

- [ ] **Step 2: Run supervisor tests and verify failure**

Run: `go test ./internal/codex -run 'TestSupervisor|TestReapPrevious'`

Expected: FAIL because supervisor types are undefined.

- [ ] **Step 3: Implement exact supervisor and process contracts**

Run: `go get golang.org/x/sys@v0.37.0`

```go
type ProcessIdentity struct { Generation string; PID int; PGID int; StartIdentity string }
type ProcessMatch int
const (ProcessUnknown ProcessMatch = iota; ProcessAbsent; ProcessMatches; ProcessMismatch)

type ProcessManager interface {
    Version(context.Context, string) (string, error)
    Start(context.Context, ProcessSpec) (*Child, error)
    Match(context.Context, ProcessIdentity) (ProcessMatch, error)
    StopCurrent(context.Context, *Child, time.Duration) error
}

type ProcessSpec struct { Executable string; Args []string; Env []string; Dir string; Generation string }
type Child struct {
    Identity ProcessIdentity
    Stdin io.WriteCloser
    Stdout io.ReadCloser
    Stderr io.ReadCloser
    Done <-chan struct{}
    WaitErr func() error
}
type Session struct {
    Adapter *Adapter
    Child *Child
    Done <-chan struct{}
    Err func() error
}
type CheckpointSink func(context.Context, room.RuntimeCheckpoint) error

func (s *Supervisor) Probe(context.Context, string) (room.RuntimeCheckpoint, error)
func (s *Supervisor) ReapPrevious(context.Context, *room.RuntimeCheckpoint) error
func (s *Supervisor) Start(context.Context, string, CheckpointSink) (*Session, error)
func (s *Supervisor) Stop(context.Context, *Session) error

type VersionPolicy struct { AllowedCLI map[string]struct{}; SchemaSHA256 string }

func DefaultVersionPolicy() VersionPolicy {
    return VersionPolicy{AllowedCLI: map[string]struct{}{"codex-cli 0.151.0-alpha.7.2": {}}, SchemaSHA256: "31ae67beb2c94cc9509f6a71968600062dc8c6d7fe45437ed3a9129838f4d2d9"}
}
```

`VersionPolicy.ValidateCLI` trims outer whitespace and requires an exact allowlist key. `ValidateUserAgent` follows Task 4's exact first-token parser; it must not use substring, suffix-trimmed, or prefix-only matching. Generate each process generation from 128 bits of `crypto/rand`, encoded as lowercase hex. Random-source failure stops before child creation.

`Start(ctx, projectRoot, sink)` checks `--version`, starts `app-server --listen stdio://`, captures identity, and calls `sink(ctx, runningCheckpoint)` before initialize. If persistence fails, it stops only that in-memory child. It creates a session context, starts `RPCClient.Run` before issuing initialize, and exposes one `Session.Done` that closes when either the JSON-RPC loop exits or the child exits; `Session.Err` returns only a normalized code/digest. It drains stderr into a bounded 64 KiB ring, initializes, checks the exact `userAgent`, and returns. On a post-checkpoint failure, it safely stops the current session and persists a stopped checkpoint. Raw stderr and RPC text never enter a returned/logged error.

The Session retains the checkpoint sink passed to `Start`. `Supervisor.Stop` is idempotent: remove the Adapter from service, cancel the RPC context, close its pipe endpoints, call `StopCurrent` for the original child, wait for both RPC loop and process reaping, and persist stopped state before returning. A protocol violation can therefore terminate and reap a child that remains alive; a child exit can terminate the JSON-RPC loop. Tests induce each side independently and assert `Session.Done`, one cleanup, and no surviving process/goroutine.

`Probe` checks the CLI version, initialize user-agent/schema contract, and account readiness using one temporary child, then closes/reaps that original handle and returns a `RuntimeProcessStopped` checkpoint with PID/PGID zero. It never starts, reads, or resumes a thread; cwd and unrestricted-policy acceptance are therefore rechecked by the first real thread operation under `serve`. It never leaves a child behind and returns an error if cleanup cannot be proven.

- [ ] **Step 4: Implement Linux child ownership without check-then-signal races**

Linux records PID, PGID, and `/proc/{pid}/stat` start time, sets `Pdeathsig`, and starts a dedicated process group. `ReapPrevious` never signals: a missing/stopped checkpoint and `ProcessAbsent` permit startup; `ProcessMatches`, `ProcessMismatch`, and `ProcessUnknown` all stop with typed owner-repair errors. A running checkpoint with PID or PGID `<= 1` is invalid and also stops.

`StopCurrent` accepts only the original `*Child` returned by `Start`. Coordinate exit observation, final reaping, and group signaling with child-owned state: observe exit without reaping (`waitid(..., WNOWAIT)` or equivalent), acquire the child lock before final `Wait`, and acquire that same lock before any signal. Thus an exited PID/PGID cannot be reaped and reused between the decision and signal. Close stdin, wait a bounded grace period, signal only while the original child is unreaped, then reap exactly once. Reject nil/foreign handles and any live handle with PID/PGID `<= 1`; an already reaped original handle is an idempotent no-op and can never signal. Tests force exit at every boundary and prove no post-reap signal. `process_unsupported.go` uses `//go:build !linux` and all production operations return `ErrUnsupportedPlatform`.

- [ ] **Step 5: Add a stable runtime proxy with bounded restart**

```go
type RuntimeConfig struct {
    ProjectRoot string
    RestartInitial time.Duration
    RestartMaximum time.Duration
}

type Sleeper interface { Sleep(context.Context, time.Duration) error }
func NewRuntime(*Supervisor, RuntimeConfig, CheckpointSink, Sleeper) (*Runtime, error)
func (r *Runtime) Start(context.Context) error
func (r *Runtime) Close(context.Context) error
```

`Runtime` implements every `room.Agent` method and exposes one stable `Events()` channel for its lifetime. `Start` synchronously obtains the first fully initialized session, then one owner goroutine forwards adapter events and watches `Session.Done`. On either JSON-RPC or child failure it atomically removes the adapter, immediately emits one `runtime-unavailable` event, then calls `Supervisor.Stop`. It may attempt a replacement only after that session is fully stopped/reaped and stopped-checkpoint persistence succeeds; cleanup/persistence failures remain recovering and back off without starting a second child. Replacement attempts start at 250 ms, double to a 10-second cap, and stop on cancellation. A fully validated replacement emits `runtime-ready`; the event channel closes only after `Close` finishes.

Each Agent method snapshots the current adapter and makes at most one call. With no adapter, mutations return `MutationError{Certainty: DeliveryNotSent, Err: ErrRuntimeUnavailable}`; if a selected session dies during the call, Task 4's byte-count rule determines uncertainty. Runtime never retries a mutation. Coordinator handling in Tasks 6–7 changes room status to `recovering`, moves unresolved active work to `needs-review`, keeps queued work frozen, and on `runtime-ready` reconciles/resumes the stored thread before setting `ready` and dispatching again.

Every Runtime event forward selects between the bounded output channel and Runtime cancellation. Backpressure may pause App Server reads, but no event is silently dropped; `Close` cancellation always releases a blocked forwarder before waiting, preventing shutdown deadlock after Coordinator exits.

Test stable event delivery across two sessions, exact 250 ms/capped 10 s backoff with a fake clock, no mutation retry during a crash, cancelled shutdown, exactly one close of the public event channel, and immediate `DeliveryNotSent` while unavailable.

- [ ] **Step 6: Add and exercise the deterministic fake App Server**

The fake executable supports `--version` and `app-server --listen stdio://`, answers initialize with an exact matching `userAgent`, records requests to a harness-owned path, streams configured deltas/completions, and has deterministic EOF, delayed-response, malformed-response, reverse-request, and crash-after-receive-before-response modes. These are test-helper arguments only; `agent_romm` has no fake mode.

Run: `gofmt -w internal/codex && go test -race ./internal/codex -run 'TestSupervisor|TestReapPrevious|TestRuntime|TestFakeAppServer'`

Expected: PASS without a leaked child or goroutine.

- [ ] **Step 7: Commit process supervision**

```bash
git add go.mod go.sum internal/codex
git commit -m "feat: supervise the Codex App Server process"
```

### Task 6: Build the single-owner room coordinator

**Files:**
- Create: `internal/room/commands.go`
- Create: `internal/room/coordinator.go`
- Create: `internal/room/coordinator_test.go`

**Interfaces:**
- Consumes: `Repository`, `Agent`, `EventSink`, and `Clock` from Task 1.
- Produces: `NewCoordinator`, `Recover`, `Run`, `Submit`, `Note`, `Steer`, `Cancel`, and `Snapshot`. All mutable coordinator state is owned by the `Run` goroutine.

- [ ] **Step 1: Write concurrent FIFO, note, and stale-turn tests**

```go
func TestCoordinatorSerializesConcurrentSubmissions(t *testing.T) {
    repo := newMemoryRepository(t)
    agent := newFakeAgent("thread-1")
    c := startCoordinator(t, repo, agent)
    actors := []Actor{{UID: 1001, Name: "alice"}, {UID: 1002, Name: "bob"}, {UID: 1003, Name: "carol"}}
    submitConcurrently(t, c, actors, []SubmitInput{
        {ClientMessageID: "0000000000000000000000000000000a", Text: "first"},
        {ClientMessageID: "0000000000000000000000000000000b", Text: "second"},
        {ClientMessageID: "0000000000000000000000000000000c", Text: "third"},
    })
    snap, err := c.Snapshot(context.Background())
    if err != nil { t.Fatal(err) }
    if len(snap.Queue) != 2 { t.Fatalf("queue=%#v", snap.Queue) }
    if got := agent.StartTurnCalls(); len(got) != 1 { t.Fatalf("calls=%#v", got) }
    assertStrictlyIncreasingAcceptanceSeqs(t, repo.acceptances())
}

func TestNoteNeverCallsAgent(t *testing.T) {
    repo := newMemoryRepository(t)
    agent := newFakeAgent("thread-1")
    c := startCoordinator(t, repo, agent)
    _, err := c.Note(context.Background(), Actor{UID: 1002, Name: "bob"}, SubmitInput{ClientMessageID: "0000000000000000000000000000000d", Text: "human context"})
    if err != nil { t.Fatal(err) }
    if len(agent.AllCalls()) != 0 { t.Fatalf("agent calls=%#v", agent.AllCalls()) }
}

func TestStaleCancelDoesNotInterruptNewTurn(t *testing.T) {
    c, agent := coordinatorWithActiveTurn(t, "turn-2")
    _, err := c.Cancel(context.Background(), Actor{UID: 1002, Name: "bob"}, CancelInput{ClientMessageID: "0000000000000000000000000000000e", ExpectedTurnID: "turn-1"})
    if !errors.Is(err, ErrStaleTurn) { t.Fatalf("got %v", err) }
    if len(agent.InterruptCalls()) != 0 { t.Fatal("interrupted a newer turn") }
}
```

- [ ] **Step 2: Run coordinator tests and verify failure**

Run: `go test ./internal/room -run 'TestCoordinator|TestNote|TestStale'`

Expected: FAIL because coordinator commands are undefined.

- [ ] **Step 3: Implement one command loop and deterministic dispatch**

```go
type Coordinator struct {
    roomID RoomID
    projectRoot string
    repository Repository
    agent Agent
    sink EventSink
    commands chan any
    agentEvents <-chan AgentEvent
    projection *Projection
    state coordinatorState
}

func (c *Coordinator) Run(ctx context.Context) error {
    for {
        select {
        case <-ctx.Done(): return ctx.Err()
        case command := <-c.commands: c.handleCommand(ctx, command)
        case event, ok := <-c.agentEvents:
            if !ok { return ErrAgentRuntimeClosed }
            c.handleAgentEvent(ctx, event)
        }
    }
}
```

Every public method posts a typed command carrying a one-shot response channel. `Submit` first validates and calls `Repository.AcceptMessage`; duplicates return the prior acceptance without another dispatch. When idle, `dispatchNext` calls `BeginDispatch`, then `Agent.StartTurn` with exactly `"[participant: " + actor.Name + "]\n" + text`. When active, the accepted request remains FIFO queued.

`NewCoordinator` does not perform recovery or start goroutines. `Run` must already be executing before any public method, including `Recover`; public methods select on both command delivery/result and their context so startup failure or shutdown cannot deadlock a caller. Tests call `startCoordinator` before `Recover` and assert cancellation releases a blocked caller.

- [ ] **Step 4: Implement explicit steer and cancel preconditions**

`Steer` and `Cancel` first journal their client message ID through `AcceptControl`, return the stored terminal result on an exact duplicate, and compare `ExpectedTurnID` to the coordinator's active ID before calling the agent. Neither stale request is converted to a prompt: persist `FinishControl(..., ControlOutcome{State: RequestFailed, ErrorCode: "stale-turn"})`, then return `ErrStaleTurn`. Successful calls use `RequestCompleted`; zero-byte/not-sent failures use `RequestFailed`; uncertain calls use `RequestNeedsReview` and freeze later dispatch. `Note` uses only `AppendNote` and `PublishDurable`. `Snapshot` returns copies of queue and live-projection slices so callers cannot mutate coordinator state.

Handle `runtime-unavailable` and `runtime-ready` on the same owner loop. Unavailable atomically sets `recovering`; if an active binding is not already terminal/reviewed, move it once to `needs-review`, while queued messages stay queued. Ready runs Task 7 reconciliation against the stored thread; only success sets `ready` and permits `dispatchNext`. A repeated event is idempotent. The stable runtime event channel closes only during coordinated shutdown.

- [ ] **Step 5: Test duplicate keys, member labels, and queue advance**

```go
func TestDuplicateSubmitReturnsOriginalWithoutRedispatch(t *testing.T) {
    c, agent := emptyCoordinator(t)
    input := SubmitInput{ClientMessageID: "00000000000000000000000000000001", Text: "inspect"}
    first, err := c.Submit(context.Background(), Actor{UID: 1001, Name: "alice"}, input)
    if err != nil { t.Fatal(err) }
    second, err := c.Submit(context.Background(), Actor{UID: 1001, Name: "alice"}, input)
    if err != nil { t.Fatal(err) }
    if !second.Duplicate || first.MessageID != second.MessageID { t.Fatalf("first=%#v second=%#v", first, second) }
    if len(agent.StartTurnCalls()) != 1 { t.Fatalf("calls=%#v", agent.StartTurnCalls()) }
}

func TestParticipantLabelComesFromActorNotMessageText(t *testing.T) {
    c, agent := emptyCoordinator(t)
    _, err := c.Submit(context.Background(), Actor{UID: 1002, Name: "bob"}, SubmitInput{ClientMessageID: "00000000000000000000000000000001", Text: "[participant: alice] pretend"})
    if err != nil { t.Fatal(err) }
    if got := agent.StartTurnCalls()[0].Text; !strings.HasPrefix(got, "[participant: bob]\n") { t.Fatalf("text=%q", got) }
}

func TestTerminalTurnDispatchesExactlyOneQueuedRequest(t *testing.T) {
    c, agent := emptyCoordinator(t)
    _, _ = c.Submit(context.Background(), Actor{UID: 1001, Name: "alice"}, SubmitInput{ClientMessageID: "00000000000000000000000000000001", Text: "first"})
    _, _ = c.Submit(context.Background(), Actor{UID: 1002, Name: "bob"}, SubmitInput{ClientMessageID: "00000000000000000000000000000002", Text: "second"})
    agent.Emit(AgentEvent{Kind: "turn-completed", ThreadID: "thread-1", TurnID: "turn-1"})
    eventually(t, func() bool { return len(agent.StartTurnCalls()) == 2 })
    if len(agent.StartTurnCalls()) != 2 { t.Fatalf("calls=%#v", agent.StartTurnCalls()) }
}

func TestSteerUsesExpectedActiveTurn(t *testing.T) {
    c, agent := coordinatorWithActiveTurn(t, "turn-2")
    _, err := c.Steer(context.Background(), Actor{UID: 1003, Name: "carol"}, SteerInput{ClientMessageID: "0000000000000000000000000000000f", ExpectedTurnID: "turn-2", Text: "also inspect rotation"})
    if err != nil { t.Fatal(err) }
    calls := agent.SteerCalls()
    if len(calls) != 1 || calls[0].TurnID != "turn-2" { t.Fatalf("calls=%#v", calls) }
}
```

Add tests proving `FailDispatch` is called for `DeliveryNotSent`, `FinishControl` records all success/stale/not-sent/unknown outcomes, duplicate stale controls return the same `ErrorCode` without a second Agent call, and runtime-unavailable freezes the queue before runtime-ready reconciliation.

The fake repository assigns ordered acceptance sequences, and the fake agent records call arguments. `emptyCoordinator` is `startCoordinator` plus a stored thread binding and no active turn.

- [ ] **Step 6: Run room tests under the race detector**

Run: `gofmt -w internal/room && go test -race ./internal/room`

Expected: PASS.

- [ ] **Step 7: Commit the coordinator**

```bash
git add internal/room
git commit -m "feat: coordinate a single shared Codex turn"
```

### Task 7: Add transient projection and crash-safe recovery states

**Files:**
- Create: `internal/room/projection.go`
- Create: `internal/room/recovery.go`
- Create: `internal/room/recovery_test.go`
- Modify: `internal/room/coordinator.go`
- Modify: `internal/store/recovery.go`
- Modify: `internal/store/recovery_test.go`

**Interfaces:**
- Consumes: `AgentEvent`, `RecoveryImage`, repository compare-and-swap methods, and `MutationError.Certainty`.
- Produces: completed-item replacement, `needs-review`, all three recovery actions, and `thread-needs-repair` with no automatic replacement thread.

- [ ] **Step 1: Write transient-to-durable projection tests**

Create this projection surface before implementing it:

```go
type ProjectedItem struct { Partial string; Completed *CompletedItem }
type ProjectionUpdate struct { Transient *TransientEvent; Durable *CompletedItem }
func NewProjection() *Projection
func (p *Projection) Apply(AgentEvent) ProjectionUpdate
func (p *Projection) Item(TurnID, ItemID) ProjectedItem
func (p *Projection) Snapshot() (revision uint64, items []LiveItemSnapshot)
```

```go
func TestCompletedItemReplacesTransientProjection(t *testing.T) {
    p := NewProjection()
    p.Apply(AgentEvent{Kind: "item-delta", ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Delta: "par"})
    p.Apply(AgentEvent{Kind: "item-delta", ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Delta: "tial"})
    completed := CompletedItem{ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Payload: json.RawMessage(`{"text":"final"}`)}
    result := p.Apply(AgentEvent{Kind: "item-completed", Completed: &completed})
    if result.Durable == nil || string(result.Durable.Payload) != `{"text":"final"}` { t.Fatalf("result=%#v", result) }
    if got := p.Item("turn-1", "item-1"); got.Partial != "" { t.Fatalf("partial=%q", got.Partial) }
}
```

Assert delta results are transient and have no `room.Seq`; only `Repository.RecordCompletedItem` allocates a durable sequence. Projection increments one in-memory revision for every applied delta and completion/removal, and stamps each emitted `TransientEvent`. `Projection.Snapshot` atomically returns the current revision plus a stable copy sorted by `(thread ID, turn ID, item ID)`, includes only nonempty partial items, and removes an item as soon as its complete replacement is applied. Because Projection is owned by the Coordinator loop, `Coordinator.Snapshot` obtains both values in the same command and embeds them in `Snapshot.ProjectionRevision` and `Snapshot.LiveItems`.

- [ ] **Step 2: Write ambiguous-dispatch and queue-freeze tests**

```go
func TestUnknownStartTurnEntersNeedsReviewWithoutRetry(t *testing.T) {
    repo := newMemoryRepository(t)
    agent := newFakeAgent("thread-1")
    agent.startTurnErr = &MutationError{Operation: "turn/start", Certainty: DeliveryUnknown, Err: io.ErrUnexpectedEOF}
    c := startCoordinator(t, repo, agent)
    _, _ = c.Submit(context.Background(), Actor{UID: 1001, Name: "alice"}, SubmitInput{ClientMessageID: "00000000000000000000000000000001", Text: "mutate"})
    _, _ = c.Submit(context.Background(), Actor{UID: 1002, Name: "bob"}, SubmitInput{ClientMessageID: "00000000000000000000000000000002", Text: "next"})
    eventually(t, func() bool { return repo.state("00000000000000000000000000000001") == RequestNeedsReview })
    if len(agent.StartTurnCalls()) != 1 { t.Fatalf("automatic retry occurred") }
    if repo.state("00000000000000000000000000000002") != RequestQueued { t.Fatalf("queue was not frozen") }
}
```

- [ ] **Step 3: Run recovery tests and verify failure**

Run: `go test ./internal/room ./internal/store -run 'TestCompletedItem|TestUnknownStartTurn|TestRecover|TestThreadNeedsRepair'`

Expected: FAIL because projection and recovery transitions are incomplete.

- [ ] **Step 4: Implement certainty-aware dispatch and startup reconciliation**

If `StartTurn` returns `DeliveryNotSent`, call `FailDispatch` with `RequestFailed` plus a normalized code/digest and advance FIFO only when room status is still ready. If it returns `DeliveryUnknown`, transactionally mark it `needs-review`, publish the durable review event, and freeze agent dispatch. Never store or emit raw errors. On `Recover`, apply these rules:

```text
queued                                -> keep queued
dispatching without recorded turn ID -> needs-review
running proven terminal in history   -> recorded terminal state
running not proven terminal           -> needs-review
missing/corrupt/mismatched thread     -> thread-needs-repair
```

`Recover` may call `ReadThread` and `ResumeThread` for the stored ID; it must never call `StartThread` when an ID existed or an initial `thread/start` outcome was uncertain.

For runtime replacement, first persist `recovering`; reconcile completed history and the prior active binding through the same rules, resume only the stored thread, then persist `ready`. If reconciliation cannot prove a terminal state, retain `needs-review`; if the binding or cwd mismatches, persist `thread-needs-repair`. A late event from an old adapter generation is ignored by the Runtime and cannot mutate the new projection.

- [ ] **Step 5: Implement transactional recovery decisions**

`RecoveryRetry` records that duplicate external effects are possible and permits exactly one new call. For a prompt/recovery-prompt it moves the reviewed request back to FIFO queued. For steer/cancel it moves the control to dispatching and returns the persisted `RetryCommand`; Coordinator rechecks that the expected turn is still active, then invokes that same control once or terminalizes it as stale. `RecoverySkip` terminally records the original as failed/skipped without calling the agent. `RecoveryContinue` atomically terminalizes any reviewed original and accepts a new prompt with `ReplacementMessageID`; blank or reused replacement IDs fail without unfreezing. `RecoveryResult.Duplicate` prevents replay when the recovery response itself was lost. A state predicate ensures two concurrent recoveries have one winner and one `ErrStaleRecovery`.

- [ ] **Step 6: Cover every recovery race and late event**

```go
func TestRecoveryActions(t *testing.T) {
    cases := []struct {
        name string
        input RecoverInput
        wantOriginal RequestState
        wantStartCalls int
        wantReplacement bool
    }{
        {"retry", RecoverInput{ClientMessageID: "00000000000000000000000000000010", TargetMessageID: "00000000000000000000000000000001", Action: RecoveryRetry}, RequestRunning, 1, false},
        {"skip", RecoverInput{ClientMessageID: "00000000000000000000000000000010", TargetMessageID: "00000000000000000000000000000001", Action: RecoverySkip}, RequestFailed, 0, false},
        {"continue", RecoverInput{ClientMessageID: "00000000000000000000000000000010", TargetMessageID: "00000000000000000000000000000001", Action: RecoveryContinue, ReplacementMessageID: "00000000000000000000000000000002", Instruction: "inspect current state"}, RequestFailed, 1, true},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            c, repo, agent := reviewedCoordinator(t, "00000000000000000000000000000001")
            err := c.Resolve(context.Background(), Actor{UID: 1002, Name: "bob"}, tc.input)
            if err != nil { t.Fatal(err) }
            eventually(t, func() bool { return repo.state("00000000000000000000000000000001") == tc.wantOriginal })
            if len(agent.StartTurnCalls()) != tc.wantStartCalls { t.Fatalf("calls=%#v", agent.StartTurnCalls()) }
            if repo.hasMessage("00000000000000000000000000000002") != tc.wantReplacement { t.Fatalf("replacement=%v", repo.hasMessage("00000000000000000000000000000002")) }
        })
    }
}

func TestConcurrentRecoveriesHaveOneWinner(t *testing.T) {
    c, _, _ := reviewedCoordinator(t, "00000000000000000000000000000001")
    start := make(chan struct{})
    results := make(chan error, 2)
    ids := []ClientMessageID{"00000000000000000000000000000011", "00000000000000000000000000000012"}
    for index, action := range []RecoveryAction{RecoveryRetry, RecoverySkip} {
        go func(index int, action RecoveryAction) {
            <-start
            results <- c.Resolve(context.Background(), Actor{UID: 1002, Name: "bob"}, RecoverInput{ClientMessageID: ids[index], TargetMessageID: "00000000000000000000000000000001", Action: action})
        }(index, action)
    }
    close(start)
    errs := []error{<-results, <-results}
    if countNil(errs) != 1 || countIs(errs, ErrStaleRecovery) != 1 { t.Fatalf("errors=%v", errs) }
}

func TestLateCompletionCannotOverwriteUnsupportedFailure(t *testing.T) {
    c, repo, agent := coordinatorWithUnsupportedFailure(t, "turn-1")
    agent.Emit(AgentEvent{Kind: "turn-completed", ThreadID: "thread-1", TurnID: "turn-1"})
    eventually(t, func() bool { return repo.state("00000000000000000000000000000001") == RequestFailed })
    if repo.state("00000000000000000000000000000001") != RequestFailed { t.Fatalf("state=%s", repo.state("00000000000000000000000000000001")) }
}

func TestMismatchedResumeCWDNeverCreatesReplacementThread(t *testing.T) {
    c, repo, agent := recoveringCoordinator(t, ThreadSnapshot{ID: "thread-1", CWD: "/wrong"})
    if err := c.Recover(context.Background()); err != nil { t.Fatal(err) }
    if repo.roomStatus() != RoomThreadNeedsRepair { t.Fatalf("status=%s", repo.roomStatus()) }
    if agent.StartThreadCallCount() != 0 { t.Fatal("created a replacement thread") }
}
```

Add the same no-replacement assertion for an uncertain initial `thread/start`. Use the store transaction hook to crash each recovery before commit and prove there is no half-unfreeze or double dispatch. `countNil` and `countIs` are small test helpers that count exact error outcomes.

Add steer and cancel `DeliveryUnknown` cases for retry, skip, and continue. Assert a first retry makes one matching control call, a stale expected turn makes none, and resending the same recovery ID after a lost response returns the stored result without a second control or prompt call.

- [ ] **Step 7: Run affected tests under the race detector**

Run: `gofmt -w internal/room internal/store && go test -race ./internal/room ./internal/store`

Expected: PASS.

- [ ] **Step 8: Commit recovery semantics**

```bash
git add internal/room internal/store
git commit -m "feat: recover ambiguous room work safely"
```

### Task 8: Serve authenticated Unix sessions with replay and backpressure

**Files:**
- Create: `internal/identity/peer.go`
- Create: `internal/identity/peer_linux.go`
- Create: `internal/identity/peer_unsupported.go`
- Create: `internal/identity/peer_test.go`
- Create: `internal/daemon/server.go`
- Create: `internal/daemon/session.go`
- Create: `internal/daemon/hub.go`
- Create: `internal/daemon/replay.go`
- Create: `internal/daemon/server_test.go`
- Create: `internal/daemon/replay_test.go`
- Create: `internal/observability/logger.go`
- Create: `internal/observability/logger_test.go`

**Interfaces:**
- Consumes: protocol framing, `room.Coordinator`, member lookup, event replay, and kernel Unix peer credentials.
- Produces: `daemon.NewServer(Config, Dependencies)`, `Serve`, `Shutdown`, and a bounded fan-out hub.

- [ ] **Step 1: Write identity fail-closed tests**

```go
func TestUnknownPeerGetsNoHistoryAndNoPersistence(t *testing.T) {
    deps := testDependencies(t)
    deps.Peers = fixedPeerResolver{peer: identity.Peer{UID: 9999}}
    server, client := startPipeSession(t, deps)
    defer server.Shutdown(context.Background())
    writeHello(t, client, 0)
    got := readError(t, client)
    if got.Code != "member-not-found" { t.Fatalf("got %#v", got) }
    if deps.Store.historyReads != 0 || deps.Store.writeCalls != 0 { t.Fatal("unauthenticated peer touched room data") }
}

func TestPayloadCannotChangeBoundActor(t *testing.T) {
    deps := testDependencies(t)
    calls := deps.Coordinator.(*recordingRoomService)
    deps.Peers = fixedPeerResolver{peer: identity.Peer{UID: 1002}}
    _, client := startPipeSession(t, deps)
    writeHello(t, client, 0)
    writeRawSubmitWithExtraIdentityFields(t, client, "uid", 1001, "author", "alice")
    if got := readError(t, client); got.Code != "invalid-request" { t.Fatalf("got %#v", got) }
    if got := calls.callCount(); got != 0 { t.Fatalf("spoof request reached coordinator: %d", got) }
    writeSubmit(t, client, "00000000000000000000000000000001", "clean request")
    eventually(t, func() bool { return calls.callCount() == 1 })
    if got := calls.lastActor(); got.UID != 1002 || got.Name != "bob" { t.Fatalf("actor=%#v", got) }
}
```

The second request is rejected by strict protocol decoding when it contains unknown fields; a valid retry still binds Bob from peer UID.

- [ ] **Step 2: Run identity and daemon tests and verify failure**

Run: `go test ./internal/identity ./internal/daemon -run 'TestUnknownPeer|TestPayloadCannot|TestPeer'`

Expected: FAIL because identity and daemon packages do not exist.

- [ ] **Step 3: Implement production peer resolution without fallback**

```go
type Peer struct { UID room.UID; PID int }
type Resolver interface { Resolve(net.Conn) (Peer, error) }
```

The production resolver first type-asserts `net.Conn` to `*net.UnixConn`; other transports fail. On Linux, use `SyscallConn` and `unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)`. `peer_unsupported.go` uses `//go:build !linux` and always returns `ErrUnsupportedPlatform`. Any syscall, cast, validity, or unsupported-platform error closes the connection. Production code never substitutes daemon EUID, a username, payload data, or an environment variable. Generic daemon tests on non-Linux use only the injected fake resolver; real macOS peer support remains Milestone 3.

- [ ] **Step 4: Implement authenticate-before-replay sessions**

```go
type Config struct {
    RoomID room.RoomID
    RoomName string
    ProjectRoot string
    ExecutionOwner string
    SocketPath string
    SocketGID uint32
    MaximumFrameBytes uint32
    ClientBufferEvents int
    ClientBufferBytes int
    RequestTimeout time.Duration
    HandshakeTimeout time.Duration
    IdleTimeout time.Duration
}

type RoomService interface {
    Submit(context.Context, room.Actor, room.SubmitInput) (room.Acceptance, error)
    Note(context.Context, room.Actor, room.SubmitInput) (room.Acceptance, error)
    Steer(context.Context, room.Actor, room.SteerInput) (room.Acceptance, error)
    Cancel(context.Context, room.Actor, room.CancelInput) (room.Acceptance, error)
    Resolve(context.Context, room.Actor, room.RecoverInput) error
    Snapshot(context.Context) (room.Snapshot, error)
}

type Dependencies struct {
    Peers identity.Resolver
    IDs interface { NewConnectionID() (room.ConnectionID, error) }
    Members interface { FindMember(context.Context, room.RoomID, room.UID) (room.Member, error) }
    Events interface {
        LatestSeq(context.Context, room.RoomID) (room.Seq, error)
        Events(context.Context, room.RoomID, room.Seq, room.Seq, int) ([]room.DurableEvent, error)
    }
    Coordinator RoomService
    Hub *Hub
    Connections interface {
        Connected(context.Context, room.ConnectionRecord) error
        Ack(context.Context, room.ConnectionID, room.Seq) error
        Disconnected(context.Context, room.ConnectionID, time.Time) error
        CloseStale(context.Context, room.RoomID, time.Time) error
    }
    ProjectView interface {
        Status(context.Context) ([]byte, error)
        Diff(context.Context) ([]byte, error)
    }
}

func NewServer(cfg Config, deps Dependencies) (*Server, error)
func (s *Server) Serve(context.Context) error
func (s *Server) Shutdown(context.Context) error
```

Accept only `*net.UnixConn`, resolve and authorize the peer before reading replay state, then require a version-1 hello within a 10-second `HandshakeTimeout`. Bind the resulting `room.Actor` to the session object; request payloads never carry or replace it. Record connection open only after both peer authorization and a valid hello; record later acknowledgements and disconnects against that server-generated connection ID. Dispatch mutating requests to the coordinator with a per-request deadline. Serve `queue` and `status` from `Coordinator.Snapshot`, `who` from the hub's authenticated-member snapshot, and `diff` from `ProjectView.Diff`; return a response or structured error for every request. Wire errors contain only stable codes and safe fixed explanations—never raw upstream RPC errors, stderr, paths outside the published project root, or wrapped `error.Error()` text. Echo heartbeats without touching room state and close a connection after a 90-second `IdleTimeout` with no valid frame; neither timeout cancels active Codex work.

- [ ] **Step 5: Implement gap-free replay with harmless duplicates**

Register a bounded live subscription first, then read `highWater := Events.LatestSeq`, replay `(lastApplied, highWater]`, and call `Coordinator.Snapshot` through its serialized owner loop. Send one unsequenced `runtime-snapshot` carrying both `ProjectionRevision` and `LiveItems`, then drain the registered live subscription. Live durable events at or below `highWater` may appear twice and are sent with the same sequence for client deduplication; durable events above it remain queued. For transient broadcasts, the server discards every event with `Revision <= snapshot.ProjectionRevision` because its effect is already represented by the replacement snapshot, and forwards only larger revisions. The subscription-before-snapshot order plus this revision fence makes the handoff gap-free and non-duplicating. Reconnect after daemon crash gets no false partials because only the new daemon's in-memory projection contributes live items.

The hub exposes this exact concurrency-safe surface:

```go
type Broadcast struct { Durable *room.DurableEvent; Transient *room.TransientEvent }
type Subscription interface {
    Events() <-chan Broadcast
    Closed() bool
    Close()
}
func NewHub(maxEvents int, maxBytes int) *Hub
func (h *Hub) Subscribe(room.Actor) Subscription
func (h *Hub) PublishDurable(room.DurableEvent)
func (h *Hub) PublishTransient(room.TransientEvent)
func (h *Hub) Members() []room.Actor
```

`Members` returns one actor per UID, sorted by UID, even when that member has multiple live sessions; closing the last session removes the actor.

- [ ] **Step 6: Test slow clients and connection isolation**

```go
func TestSlowClientIsDroppedWithoutBlockingFastClient(t *testing.T) {
    hub := NewHub(2, 1<<20)
    slow := hub.Subscribe(room.Actor{UID: 1001, Name: "alice"})
    fast := hub.Subscribe(room.Actor{UID: 1002, Name: "bob"})
    var got []room.Seq
    hub.PublishDurable(durable(1))
    got = append(got, (<-fast.Events()).Durable.Seq)
    hub.PublishDurable(durable(2))
    got = append(got, (<-fast.Events()).Durable.Seq)
    hub.PublishDurable(durable(3))
    got = append(got, (<-fast.Events()).Durable.Seq)
    if !slow.Closed() { t.Fatal("slow subscriber remained") }
    if !slices.Equal(got, []room.Seq{1, 2, 3}) { t.Fatalf("fast=%v", got) }
}
```

Production configuration permits at most 256 queued outbound events and 16 MiB per client; exceeding either limit closes only that connection with a resumable slow-client error. Also open two real Unix connections, send an oversized/malformed frame on one, and prove the other can submit and receive a durable event. Disconnecting either session must not cancel the coordinator context or active agent turn.

Add a barrier-controlled replay test in which a delta is published after subscription but before Coordinator snapshot. Assert it appears once via `runtime-snapshot`, its queued revision is filtered, and a delta published after the snapshot is forwarded once. Repeat with an item completion between the snapshot and drain to prove stale partial data cannot reappear.

For every pre-handshake, malformed, oversized, unauthenticated, or unknown-field request, assert the store has no new connection record, message, idempotency key, event, or sequence and the fake agent has no new call. Binding the listener refuses a symlink, regular file, or socket not proven to be this daemon's stale endpoint; after bind it chowns the socket to `SocketGID`, chmods it to `0660`, and verifies the resulting metadata.

- [ ] **Step 7: Add metadata-only structured operational logging**

`internal/observability` wraps `log/slog` with constructors for connection, accepted message, room sequence, Codex thread/turn/item lifecycle, exit status, and recovery transitions. Its exported logging methods accept only validated fixed-format client/connection IDs, numeric UIDs/sequences, lifecycle enums, normalized error-code enums, SHA-256 diagnostic digests, and byte counts—never `error`, stderr bytes, prompts, completed payloads, environment values, authorization headers, Codex home contents, or credentials. Invalid client IDs are rejected before these methods are called.

```go
func TestLifecycleLogOmitsPromptAndCredentialMaterial(t *testing.T) {
    var out bytes.Buffer
    log := New(&out)
    log.TurnStarted(context.Background(), TurnFields{ConnectionID: "0000000000000000000000000000000c", ActorUID: 1002, ThreadID: "thread-1", TurnID: "turn-1"})
    text := out.String()
    for _, want := range []string{"0000000000000000000000000000000c", "1002", "thread-1", "turn-1"} { if !strings.Contains(text, want) { t.Fatalf("missing %s", want) } }
    for _, forbidden := range []string{"prompt text", "authorization", "api_key"} { if strings.Contains(text, forbidden) { t.Fatalf("logged %s", forbidden) } }
}
```

Add an end-to-end redaction test with `sentinel := "authorization=Bearer SECRET-api_key"`: inject it separately in a rejected client ID, an App Server JSON-RPC error message/data, and captured stderr. Pass only `FailureCode`, SHA-256 digest, and byte count into `log.Failure`, then assert the JSON output contains the normalized codes/digests but neither the sentinel nor any substring `authorization`, `Bearer`, `SECRET`, or `api_key`. Also scan all call sites to prove none pass raw `error` or diagnostic text to `slog`.

- [ ] **Step 8: Run identity, daemon, and observability packages with race detection**

Run: `gofmt -w internal/identity internal/daemon internal/observability && go test -race ./internal/identity ./internal/daemon ./internal/observability`

Expected: PASS on Linux; on non-Linux the production resolver compiles and rejects while tests use injected peers.

- [ ] **Step 9: Commit authenticated serving**

```bash
git add internal/identity internal/daemon internal/observability
git commit -m "feat: serve authenticated room sessions"
```

### Task 9: Add initialization, owner checks, repair, locking, and Git projection

**Files:**
- Create: `internal/config/layout.go`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `internal/admin/init.go`
- Create: `internal/admin/lock.go`
- Create: `internal/admin/repair.go`
- Create: `internal/admin/init_test.go`
- Create: `internal/admin/repair_test.go`
- Create: `internal/gitview/git.go`
- Create: `internal/gitview/git_test.go`

**Interfaces:**
- Consumes: OS user/group lookup, Git argv execution, `store.Open`, supervisor readiness, and the daemon single-instance lock.
- Produces: owner-only initialization and thread repair, immutable runtime configuration, safe filesystem layout, and bounded read-only Git status/diff.

- [ ] **Step 1: Write project-root, EUID, and partial-state tests**

```go
func TestInitRejectsNonOwnerWithoutPartialState(t *testing.T) {
    root := makeGitRepository(t)
    state := filepath.Join(t.TempDir(), "state")
    deps := initDependencies(t, 2000)
    err := Init(context.Background(), deps, InitOptions{Project: root, StateDir: state, Room: "team", ExecutionOwner: "alice", Members: []string{"alice", "bob", "carol"}, FullOwnerAccess: true})
    if !errors.Is(err, ErrExecutionOwnerMismatch) { t.Fatalf("got %v", err) }
    if _, statErr := os.Stat(state); !errors.Is(statErr, fs.ErrNotExist) { t.Fatalf("partial state: %v", statErr) }
}

func TestCanonicalProjectMustEqualGitTopLevel(t *testing.T) {
    root := makeGitRepository(t)
    err := validateProject(filepath.Join(root, "subdir"))
    if !errors.Is(err, ErrProjectNotGitRoot) { t.Fatalf("got %v", err) }
}
```

Table-test relative paths, missing paths, files, non-Git directories, Git subdirectories, symlinks, a state directory equal to or nested under the project root, missing group membership, wrong group, and missing `--full-owner-access` acknowledgment.

- [ ] **Step 2: Run admin and Git tests and verify failure**

Run: `go test ./internal/config ./internal/admin ./internal/gitview`

Expected: FAIL because the packages do not exist.

- [ ] **Step 3: Implement an explicit safe layout and immutable config**

```go
type InitOptions struct {
    Project string
    StateDir string
    Room string
    ExecutionOwner string
    Members []string
    SharedGroup string
    FullOwnerAccess bool
}

type RuntimeConfig struct {
    SchemaVersion int `json:"schemaVersion"`
    RoomID room.RoomID `json:"roomId"`
    RoomName string `json:"roomName"`
    HostID string `json:"hostId"`
    ProjectRoot string `json:"projectRoot"`
    ExecutionOwnerUID room.UID `json:"executionOwnerUid"`
    ExecutionOwnerName string `json:"executionOwnerName"`
    FullOwnerAccess bool `json:"fullOwnerAccess"`
    SharedGroupGID uint32 `json:"sharedGroupGid"`
    SocketPath string `json:"socketPath"`
    DatabasePath string `json:"databasePath"`
    Members []room.Member `json:"members"`
    CodexVersion string `json:"codexVersion"`
    SchemaSHA256 string `json:"schemaSha256"`
}
```

`InitOptions.StateDir` must be absolute. Resolve its nearest existing ancestor, reject symlink traversal that changes the intended parent, and reject a final state path equal to or below the canonical project root. Create the top directory as owner/shared-group `0710`, nested `private` as `0700`, config as `0600`, database as owner-only, and socket at the top level with post-bind mode `0660`. Write config to a temporary sibling with `0600`, fsync it and its directory, then rename. Roll back newly created paths on pre-commit failure without deleting a pre-existing directory.

Generate both the stable `RoomID` and `HostID` from independent 128-bit `crypto/rand` reads; `InitOptions.Room` becomes the human-readable `RoomName`. Persist the IDs and name identically in config and the `rooms` row. A random-source failure aborts initialization before permanent state. Call `Store.InitializeRoom` exactly once with canonical member names and UIDs.

- [ ] **Step 4: Implement owner validation and single-instance lock**

Resolve every username with `os/user`; require `geteuid()` to equal the named owner UID before creating state. Verify all members, including owner, belong to the explicit shared group. `serve` reads config, checks EUID and immutable config/project invariants, acquires the lock, and only then opens SQLite or starts App Server. The lock stores daemon PID and process-start identity; a live or unprovable holder fails closed.

After identity, group, and project validation but before publishing permanent state, `init` calls `Supervisor.Probe` in the actual init environment and stores its stopped readiness checkpoint. Every `serve` constructs Runtime and calls `Runtime.Start`; its Supervisor repeats daemon-service version, user-agent, and authentication readiness, then Coordinator recovery resumes/reads the stored thread and verifies cwd plus fixed unrestricted policy before socket bind. A probe failure removes only paths created by this init attempt.

Before starting App Server, every `serve` also re-resolves the stored project path, reruns `git rev-parse --show-toplevel`, and requires byte-for-byte equality with the stored canonical root. Missing paths, changed symlink targets, a different Git root, or a false `FullOwnerAccess` config value stop before database mutation, socket bind, resume, or dispatch.

- [ ] **Step 5: Implement host-local thread repair**

`RepairUse(ctx, threadID)` requires the daemon lock, owner EUID, and a reviewed candidate whose returned `ThreadSnapshot.CWD` equals the canonical project root; it transactionally binds the candidate and preserves the old ID/reason in a durable repair event. `RepairCreate(ctx)` requires the same checks, calls `StartThread` exactly once, and treats an unknown result as `thread-needs-repair` rather than retrying. Both fail with zero changes while the daemon holds the lock.

- [ ] **Step 6: Implement bounded Git status and diff without a shell**

```go
func (v View) Status(ctx context.Context) ([]byte, error) {
    return v.run(ctx, "status", "--short", "--untracked-files=all")
}
func (v View) Diff(ctx context.Context) ([]byte, error) {
    return v.run(ctx, "diff", "--no-ext-diff", "--binary", "--")
}
```

Use `exec.CommandContext` with `cmd.Dir = canonicalRoot`, a 5-second deadline, a 4 MiB bounded stdout writer, and a separate 64 KiB stderr diagnostic writer. Cancel and reap Git when either bound is exceeded. Never interpolate paths into a shell. Tests use filenames containing spaces, semicolons, backticks, and `$()` and prove no extra command executes.

- [ ] **Step 7: Run config, admin, and Git tests**

Run: `gofmt -w internal/config internal/admin internal/gitview && go test -race ./internal/config ./internal/admin ./internal/gitview`

Expected: PASS.

- [ ] **Step 8: Commit administration and Git projection**

```bash
git add internal/config internal/admin internal/gitview
git commit -m "feat: initialize and repair owner-hosted rooms"
```

### Task 10: Implement the byte bridge and asynchronous line client

**Files:**
- Create: `internal/bridge/relay.go`
- Create: `internal/bridge/relay_test.go`
- Create: `internal/client/client.go`
- Create: `internal/client/commands.go`
- Create: `internal/client/render.go`
- Create: `internal/client/cursor.go`
- Create: `internal/client/ssh.go`
- Create: `internal/client/client_test.go`
- Create: `internal/client/commands_test.go`
- Create: `internal/client/ssh_test.go`

**Interfaces:**
- Consumes: protocol envelopes and a system `ssh` child whose stdin/stdout are protocol-only.
- Produces: byte-only `bridge.Relay`, strict command parsing, cursor persistence, reconnect handshake, event rendering, and `SSHLauncher.Start`.

- [ ] **Step 1: Write bridge half-close and stdout-purity tests**

```go
func TestRelayCopiesBothDirectionsAndStopsOnDisconnect(t *testing.T) {
    sshInR, sshInW := io.Pipe()
    sshOutR, sshOutW := io.Pipe()
    socketClient, socketServer := net.Pipe()
    done := make(chan error, 1)
    go func() { done <- Relay(context.Background(), sshInR, sshOutW, socketClient) }()
    go func() { _, _ = sshInW.Write([]byte("from-client")) }()
    got := make([]byte, len("from-client"))
    if _, err := io.ReadFull(socketServer, got); err != nil { t.Fatal(err) }
    if string(got) != "from-client" { t.Fatalf("got %q", got) }
    _ = socketServer.Close()
    select { case <-done: case <-time.After(time.Second): t.Fatal("relay leaked") }
    _ = sshOutR.Close()
}
```

Diagnostics are returned as errors to the caller and are never written to the relay's protocol stdout.

- [ ] **Step 2: Write complete command parser tests**

```go
func TestParseCommands(t *testing.T) {
    cases := []struct{ input string; kind CommandKind }{
        {"fix the test", CommandPrompt}, {"/steer add logging", CommandSteer},
        {"/note human only", CommandNote}, {"/queue", CommandQueue},
        {"/status", CommandStatus}, {"/who", CommandWho},
        {"/cancel", CommandCancel}, {"/diff", CommandDiff},
        {"/recover retry 00000000000000000000000000000001", CommandRecoverRetry},
        {"/recover skip 00000000000000000000000000000001", CommandRecoverSkip},
        {"/recover continue 00000000000000000000000000000001 inspect state", CommandRecoverContinue},
        {"/quit", CommandQuit},
    }
    for _, tc := range cases {
        got, err := ParseCommand(tc.input)
        if err != nil || got.Kind != tc.kind { t.Fatalf("%q: %#v %v", tc.input, got, err) }
    }
}
```

Also reject missing steer/note text, missing recovery IDs, missing continue instruction, extra arguments to no-argument commands, blank lines, and unknown slash commands.

- [ ] **Step 3: Run bridge/client tests and verify failure**

Run: `go test ./internal/bridge ./internal/client`

Expected: FAIL because the packages do not exist.

- [ ] **Step 4: Implement bridge shutdown and exact SSH argv**

```go
func (l SSHLauncher) Start(ctx context.Context, target string) (*Connection, error) {
    cmd := exec.CommandContext(ctx, l.Executable, "-T", target, "agent_romm bridge")
    stdin, err := cmd.StdinPipe()
    if err != nil { return nil, err }
    stdout, err := cmd.StdoutPipe()
    if err != nil { return nil, err }
    cmd.Stderr = l.Stderr
    if err := cmd.Start(); err != nil { return nil, err }
    return newConnection(cmd, stdout, stdin), nil
}
```

Reject an empty target; resolve only the literal system `ssh` executable in production; never add user text, room, project path, or a formatted remote command. `Relay` runs two copy loops, closes the Unix side when either direction terminates, and waits for both loops so it does not leak goroutines.

- [ ] **Step 5: Implement cursor-safe reconnect and rendering**

Generate every client message ID and framed request ID from independent 128-bit values read through `crypto/rand.Reader`, encoded as exactly 32 lowercase hex characters; a random-source failure rejects the input instead of using time, PID, or a counter. Persist `{host target, room ID, last durable seq}` with write-fsync-rename and mode `0600`. On reconnect, send the cursor in hello, apply durable events only when `seq > lastApplied`, atomically save each advanced cursor, then atomically replace all partial items from `runtime-snapshot` and set the local projection revision. Apply later transient events only when their revision is greater than the local revision; revisions need not be contiguous because completed-item removals also advance the server revision. Render transient deltas without advancing the durable cursor; replace them on completed events. Keep SSH/auth/connect diagnostics on stderr and shared room output on stdout.

The client attaches its last displayed active turn ID to steer/cancel without asking the user to type it. Every prompt, note, steer, cancel, and recover command gets one command ID; recover-continue gets a second independent replacement-prompt ID. If no active turn is displayed, steer/cancel fail locally and send nothing.

Keep accepted-response status for outbound mutating requests in memory. If SSH disconnects before a response, reconnect and resend the identical request with the same client message ID after replay; never mint a replacement ID for that retry. Send heartbeat every 20 seconds while otherwise idle, require any valid frame or heartbeat response within 60 seconds, and reconnect with exponential backoff starting at 250 ms and capped at 10 seconds with crypto-random jitter. `/quit` cancels reconnect immediately. These timers govern the client transport only, never an active Codex turn.

On welcome, print room, canonical project, execution owner, active turn, and `fmt.Sprintf("ALL AGENT ACTIONS RUN WITH %s'S FULL RUNTIME AUTHORITY; TRANSCRIPT ATTRIBUTION IS NOT TAMPER-PROOF.", welcome.ExecutionOwner)` before accepting input. Reject a welcome that does not explicitly set `fullOwnerAccess: true`, because this plan has no restricted fallback mode.

- [ ] **Step 6: Test disconnect continuity and malicious display payloads**

Use a fake launcher returning `net.Pipe`; disconnect it during a streamed item and reconnect with the recorded cursor. Assert durable events render once, an unacknowledged request is resent with the identical message ID, the final completed item replaces partial text, and the client never sends `/note` to the agent. With a fake clock, assert 20-second heartbeat, 60-second dead-peer detection, capped reconnect backoff, and immediate `/quit`. Test deterministic random-source failure and 10,000 generated IDs for exact 32-character hex format and uniqueness. Escape non-printing control characters in actor names and messages except supported newline/tab so transcript data cannot inject terminal control sequences.

- [ ] **Step 7: Run bridge/client tests with race detection**

Run: `gofmt -w internal/bridge internal/client && go test -race ./internal/bridge ./internal/client`

Expected: PASS.

- [ ] **Step 8: Commit bridge and client**

```bash
git add internal/bridge internal/client
git commit -m "feat: add SSH bridge and shared room client"
```

### Task 11: Wire the CLI and prove the local multi-client vertical slice

**Files:**
- Create: `cmd/agent_romm/main.go`
- Create: `internal/cli/run.go`
- Create: `internal/cli/run_test.go`
- Create: `internal/integration/local_process_test.go`
- Create: `internal/integration/testdata/fakessh/main.go`
- Create: `README.md`

**Interfaces:**
- Consumes: all packages from Tasks 1–10.
- Produces: one `agent_romm` binary exposing `init`, `serve`, `connect`, `bridge`, and `repair-thread`, plus a deterministic local process acceptance test.

- [ ] **Step 1: Write CLI contract tests before wiring**

```go
func TestRootHelpListsOnlySupportedCommands(t *testing.T) {
    var stdout, stderr bytes.Buffer
    code := Run(context.Background(), []string{"help"}, &stdout, &stderr, productionDependencies())
    if code != 0 { t.Fatalf("code=%d stderr=%s", code, stderr.String()) }
    for _, command := range []string{"init", "serve", "connect", "bridge", "repair-thread"} {
        if !strings.Contains(stdout.String(), command) { t.Fatalf("missing %s", command) }
    }
    for _, forbidden := range []string{"--as-user", "--fake", "--local"} {
        if strings.Contains(stdout.String(), forbidden) { t.Fatalf("unsafe option %s", forbidden) }
    }
}
```

Also test `init` requires at least three distinct members, explicit shared group, absolute state directory, and `--full-owner-access`; `connect` accepts exactly one SSH target; `bridge` accepts no arguments; `repair-thread` requires exactly one of `--use THREAD_ID` or `--create`. Run every root/subcommand parser against `--as-user`, `--fake`, and `--local` and require a nonzero exit with no side effect; production dependencies expose no environment-variable equivalent.

- [ ] **Step 2: Run CLI tests and verify failure**

Run: `go test ./internal/cli -run 'TestRootHelp|TestInitFlags|TestRepairFlags'`

Expected: FAIL because CLI wiring does not exist.

- [ ] **Step 3: Implement thin subcommand wiring**

```go
func main() {
    os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, cli.ProductionDependencies()))
}
```

`run.go` uses `flag.FlagSet` with `ContinueOnError`; it constructs packages and maps typed errors to stable nonzero exit codes. Business logic stays in internal packages. `serve` verifies owner EUID and config/project invariants, acquires the lock, opens the store, and calls `ReapPrevious` only to prove a prior child absent—never to signal it. It creates the stable Runtime with this exact closure:

```go
checkpoint := func(ctx context.Context, cp room.RuntimeCheckpoint) error {
    return st.SaveRuntimeCheckpoint(ctx, cfg.RoomID, cp)
}
```

It synchronously starts Runtime, constructs Coordinator, starts `Coordinator.Run` in a tracked goroutine, waits for `Coordinator.Recover` through that live owner loop, and only then binds the Unix socket. On any pre-bind failure it cancels Coordinator, closes Runtime, waits for both, and closes the store.

Shutdown first stops accepting and cancels/waits for client sessions, then cancels the Coordinator root context and calls `Runtime.Close` to unblock any in-flight RPC while the original child handle is closed/signaled/reaped safely and its Supervisor persists `RuntimeProcessStopped` with PID/PGID zero. It waits for the Coordinator goroutine, accepting `context.Canceled`/`ErrAgentRuntimeClosed` only during this requested shutdown, then closes SQLite and removes only its own verified socket. Never wait for Coordinator before closing Runtime, because a command may be blocked on that session. Production `serve` on `!linux` exits with `unsupported-platform`; the M1 macOS integration uses explicitly injected test fakes, not a CLI bypass.

`bridge` reads its socket locator from the current remote account's config path returned by `os.UserConfigDir()/agent_romm/bridge.json`; M1 tests provision this non-secret locator for each fake remote home. No socket path is accepted from client protocol data.

- [ ] **Step 4: Write the local three-client process test**

The process test never tries to inject dependencies into the production `main`. `local_process_test.go` contains a helper-process test entry compiled only into the Go test binary. Child invocations run `os.Args[0] -test.run=^TestAgentRommHelperProcess$ -- <role> <fixture-path>`; after `--`, the test-only handler constructs explicit fake peer/process/SSH dependencies and calls `cli.Run`. The production binary has no handler, flag, environment switch, or exported constructor that selects these fakes. Fixture paths contain no credentials and are accepted only by the `_test.go` helper parser.

The test performs these exact operations:

```text
1. Build the production `agent_romm`, fakeappserver, and fakessh into `t.TempDir`; use production `agent_romm` only for help and negative bypass-flag checks.
2. Create a temporary Git repository and initialize a room database.
3. Start a test-binary helper process whose `cli.Run("serve", testDependencies)` bundle launches fakeappserver and whose peer resolver assigns alice, bob, carol in connection order.
4. Start three test-binary helper client processes with injected SSH launchers; fakessh execs the production `agent_romm bridge` against the Unix socket.
5. Submit alice's prompt, then Bob's and Carol's prompts while turn 1 is active.
6. Assert one active fake turn, two FIFO queued requests, identical durable sequences on all clients, and transient deltas followed by one completed projection.
7. Send Bob's /note and assert it appears everywhere but not in fake App Server calls.
8. Send stale and current /steer and /cancel commands; assert only current IDs reach App Server.
9. Kill one bridge, finish the active turn, reconnect, and assert replay fills the durable gap without stopping work.
10. Crash fake App Server after receiving turn/start but before responding; assert the stable runtime enters recovering, restarts with bounded backoff, reconciles the same thread, leaves the ambiguous command in needs-review, and makes zero automatic mutation retries.
11. Exercise retry, skip, and continue in separate subtests and prove one transactional winner.
12. Stop cleanly, restart daemon with the persisted stopped checkpoint, and assert the same thread ID, completed history, queue, and latest room sequence. Add a separate live prior-generation checkpoint case that refuses startup without sending any signal.
```

The helper processes receive explicit test fixture objects through their `_test.go`-only argument parser. They must never resolve or execute the installed `codex` binary and fail if the fake request log contains more mutating calls than expected. A source scan and production-binary tests assert that the helper role names, fixture parser, and dependency selectors do not exist in `cmd/agent_romm`, non-test packages, help text, or production argument handling.

- [ ] **Step 5: Add focused fault-injection subtests**

Add subtests for an oversized frame isolated to one connection, slow-client eviction, owner-EUID mismatch before DB/App Server access, CLI-version mismatch before child start, App Server `userAgent` mismatch before thread access, unsupported reverse request cancellation, and initial `thread/start` uncertainty requiring host-local repair.

- [ ] **Step 6: Write the M1 README with an explicit maturity warning**

The README must state before any command example:

```text
agent_romm Milestone 1 is a local engine demo for trusted collaborators. It is not yet the three-machine release. All agent work executes with the execution owner's full runtime authority; transcript attribution is best-effort and owner-tamperable. Default tests use a fake App Server and do not consume model quota.
```

Document build/test commands, package boundaries, exact pinned dependencies, the difference between durable events and transient deltas, recovery commands, and the next Linux SSH milestone. Do not advertise production readiness.

- [ ] **Step 7: Run complete verification**

Run: `gofmt -w cmd internal && go mod tidy && go vet ./... && go test ./... && go test -race ./...`

Expected: all commands PASS; default tests make no real Codex or network call.

- [ ] **Step 8: Build and smoke-test help**

Run: `go build -trimpath -o ./bin/agent_romm ./cmd/agent_romm && ./bin/agent_romm help`

Expected: build succeeds and help lists only `init`, `serve`, `connect`, `bridge`, and `repair-thread`.

- [ ] **Step 9: Commit the completed local vertical slice**

```bash
git add cmd internal README.md go.mod go.sum
git commit -m "feat: complete the local multi-client MVP core"
```

## Spec Coverage Review

| Approved design area | Implemented here | Deferred with no M1 bypass |
|---|---|---|
| Purpose, one room/thread, owner authority | Global constraints; Tasks 1, 6, 11 | Real three-person use is M2 |
| SSH-native architecture | Task 10 keeps the production `ssh -T` and fixed bridge command boundary | Real sshd, keys, host checking, ProxyJump, and three hosts are M2 |
| Unix identity and membership | Task 8 Linux kernel resolver plus injected test resolver | Three real Unix accounts are M2; macOS credentials are M3 |
| Installation, owner EUID, state modes | Task 9 | systemd/launchd examples and host provisioning are M2/M3 |
| Framing and handshake | Task 2 | No alternate HTTP/WebSocket transport |
| Complete line-command UX | Tasks 6–11 | ANSI polish and packaging are release work |
| FIFO, steer, cancel, notes | Task 6 | Parallel turns/worktrees remain excluded |
| Codex lifecycle and unrestricted policy | Tasks 4–5, including Linux child ownership and stable restart proxy | Paid real-Codex acceptance is M2; macOS lifecycle is M3 |
| SQLite, idempotency, durable sequence | Task 3 | No distributed database or leader election |
| Delta projection and reconnect replay | Tasks 7–8, 10 | Transient deltas remain non-durable by design |
| Failure recovery and thread repair | Tasks 5, 7, 9, 11 | Automatic ambiguous retry remains forbidden |
| Operational logs and attribution limits | Task 8 observability step; Task 11 README | Tamper-resistant compliance audit remains a non-goal |
| Test strategy | Focused tests in every task; Task 11 local process acceptance | Linux SSH fixture and real Codex E2E are M2 |
| Open-source deliverables | M1 README and clean module | license, contributor docs, CI hardening, secret scan, GitHub remote, and release are M4 |

## Test Helper Contract

All helpers below live only in the package's `_test.go` files. They call `t.Helper()`, register cleanup with `t.Cleanup`, and fail the calling test immediately on fixture-construction errors.

- `mustFrame` uses the real protocol writer; `oneByteReader` returns at most one byte per `Read`.
- `openTestStore` opens a file under `t.TempDir`; `seedRoomAndMember` uses an explicit test seed transaction; `assertCounts` queries exact message and event counts.
- `newRecordingRPC` uses `net.Pipe` and a JSONL recorder; `recorder.reply` installs one method response, `recorder.request` returns the decoded request, and `assertGoldenJSON` compares structurally normalized JSON.
- `fakeProcessManager` implements every `ProcessManager` method with counters and configured results; `mustSupervisor` uses `DefaultVersionPolicy` and the fake process manager.
- `newMemoryRepository` implements the complete `Repository` contract under a mutex; `newFakeAgent` owns a buffered event channel and call log; `startCoordinator` starts `Run` and cancels it in cleanup; `submitConcurrently` releases goroutines from one barrier; `eventually` polls with a one-second test deadline; `coordinatorWithActiveTurn` seeds a running binding through repository APIs.
- `testDependencies` creates isolated fake ports; `startPipeSession` runs one daemon session over `net.Pipe` with the injected peer resolver; `writeHello`, `readError`, and `writeRawSubmitWithExtraIdentityFields` all use the real framed protocol; `durable` creates a durable event with the supplied sequence.
- `makeGitRepository` invokes `git init` with argv under `t.TempDir`; `initDependencies` supplies explicit fake user/group/EUID lookups and a fake readiness probe without changing production constructors.
- `TestAgentRommHelperProcess` exists only in `local_process_test.go`; roles after `--` construct explicit in-memory/test-executable dependencies and call `cli.Run`. Production `main` always calls `ProductionDependencies` and cannot select the helper path.

These helpers do not expose a CLI flag, environment-controlled identity, or production fake App Server path.

## Plan Completion Checklist

- [x] Every spec requirement assigned to Milestone 1 is implemented by a task above.
- [x] Every real SSH, real multi-account, real Codex, service-installation, cross-host, and release requirement remains explicitly deferred to the next milestone plan.
- [x] `rg -n -i 'T[B]D|T[O]DO|F[I]XME|P[L]ACEHOLDER|implement[[:space:]]+later|add[[:space:]]+appropriate|similar[[:space:]]+to' docs/superpowers/plans/2026-09-03-agent-romm-core-local-mvp.md` returns no matches.
- [x] All interface names in Tasks 2–11 match the Task 1 contract or are defined in their producing task.
- [x] `git diff --check` is clean.
- [x] The plan itself is committed separately before implementation starts.
