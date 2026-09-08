// Package observability exposes metadata-only operational logging. Invalid
// fields suppress the whole record; diagnostic text is never accepted.
package observability

import (
	"agent_romm/internal/room"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"regexp"
)

type Logger struct{ log *slog.Logger }

func New(w io.Writer) *Logger { return &Logger{log: slog.New(slog.NewJSONHandler(w, nil))} }

type Lifecycle string

const (
	Started   Lifecycle = "started"
	Completed Lifecycle = "completed"
	Stopped   Lifecycle = "stopped"
)

type FailureCode string

const (
	InvalidClientID FailureCode = "invalid-client-id"
	RPCFailure      FailureCode = "rpc-failure"
	ProcessFailure  FailureCode = "process-failure"
	InternalFailure FailureCode = "internal-failure"
)

type DiagnosticDigest string
type ConnectionFields struct {
	ConnectionID room.ConnectionID
	ActorUID     room.UID
}
type MessageFields struct {
	ConnectionID    room.ConnectionID
	ActorUID        room.UID
	ClientMessageID room.ClientMessageID
	Seq             room.Seq
	Bytes           uint64
}
type TurnFields struct {
	ConnectionID room.ConnectionID
	ActorUID     room.UID
	ThreadID     room.ThreadID
	TurnID       room.TurnID
	ItemID       room.ItemID
}
type FailureFields struct {
	Code   FailureCode
	Digest DiagnosticDigest
	Bytes  uint64
}

var lifecycleID = regexp.MustCompile(`^(thread|turn|item)-[0-9]{1,20}$`)
var uuidID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var digestID = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validLifecycleID(id string) bool { return lifecycleID.MatchString(id) || uuidID.MatchString(id) }
func validStage(stage Lifecycle) bool {
	return stage == Started || stage == Completed || stage == Stopped
}
func (l *Logger) Connection(ctx context.Context, f ConnectionFields, stage Lifecycle) {
	if !room.ValidConnectionID(f.ConnectionID) || !validStage(stage) {
		return
	}
	l.log.InfoContext(ctx, "connection", "connection_id", string(f.ConnectionID), "actor_uid", uint32(f.ActorUID), "stage", string(stage))
}
func (l *Logger) AcceptedMessage(ctx context.Context, f MessageFields) {
	if !room.ValidConnectionID(f.ConnectionID) || !room.ValidClientMessageID(f.ClientMessageID) {
		return
	}
	l.log.InfoContext(ctx, "message-accepted", "connection_id", string(f.ConnectionID), "actor_uid", uint32(f.ActorUID), "client_message_id", string(f.ClientMessageID), "seq", uint64(f.Seq), "bytes", f.Bytes)
}
func (l *Logger) RoomSequence(ctx context.Context, seq room.Seq) {
	l.log.InfoContext(ctx, "room-sequence", "seq", uint64(seq))
}

type CommittedFields struct {
	Seq      room.Seq
	ActorUID room.UID
	Kind     string
	ThreadID room.ThreadID
	TurnID   room.TurnID
	ItemID   room.ItemID
}

// Opaque identifiers outside the recognized safe shapes remain correlatable
// through a digest; arbitrary upstream strings never become log text.
func loggedID(id string) string {
	if id == "" || validLifecycleID(id) {
		return id
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(id)))
}
func (l *Logger) Committed(ctx context.Context, f CommittedFields) {
	switch f.Kind {
	case "thread/bound", "thread/repaired", "turn/running", "turn/completed", "turn/failed", "turn/interrupted", "item/completed":
	default:
		return
	}
	l.log.InfoContext(ctx, "agent-lifecycle", "seq", uint64(f.Seq), "actor_uid", uint32(f.ActorUID), "transition", f.Kind, "thread_id", loggedID(string(f.ThreadID)), "turn_id", loggedID(string(f.TurnID)), "item_id", loggedID(string(f.ItemID)))
}
func (l *Logger) TurnStarted(ctx context.Context, f TurnFields) { l.Lifecycle(ctx, f, Started) }
func (l *Logger) Lifecycle(ctx context.Context, f TurnFields, stage Lifecycle) {
	if !room.ValidConnectionID(f.ConnectionID) || !validStage(stage) || !validLifecycleID(string(f.ThreadID)) || (f.TurnID != "" && !validLifecycleID(string(f.TurnID))) || (f.ItemID != "" && !validLifecycleID(string(f.ItemID))) {
		return
	}
	l.log.InfoContext(ctx, "agent-lifecycle", "connection_id", string(f.ConnectionID), "actor_uid", uint32(f.ActorUID), "thread_id", string(f.ThreadID), "turn_id", string(f.TurnID), "item_id", string(f.ItemID), "stage", string(stage))
}
func (l *Logger) ExitStatus(ctx context.Context, status int) {
	if status < 0 || status > 255 {
		return
	}
	l.log.InfoContext(ctx, "process-exit", "status", status)
}
func (l *Logger) Recovery(ctx context.Context, state room.RoomStatus) {
	switch state {
	case room.RoomReady, room.RoomRecovering, room.RoomThreadNeedsRepair:
		l.log.InfoContext(ctx, "recovery", "state", string(state))
	}
}
func (l *Logger) Failure(ctx context.Context, f FailureFields) {
	switch f.Code {
	case InvalidClientID, RPCFailure, ProcessFailure, InternalFailure:
	default:
		return
	}
	if !digestID.MatchString(string(f.Digest)) {
		return
	}
	l.log.InfoContext(ctx, "failure", "code", string(f.Code), "digest", string(f.Digest), "bytes", f.Bytes)
}
