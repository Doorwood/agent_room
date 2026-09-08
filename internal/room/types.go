package room

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

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

type Actor struct {
	UID  UID
	Name string
}
type SubmitInput struct {
	ClientMessageID ClientMessageID
	Text            string
}
type SteerInput struct {
	ClientMessageID ClientMessageID
	ExpectedTurnID  TurnID
	Text            string
}
type CancelInput struct {
	ClientMessageID ClientMessageID
	ExpectedTurnID  TurnID
}
type ControlKind string

const (
	ControlSteer  ControlKind = "steer"
	ControlCancel ControlKind = "cancel"
)

type ControlInput struct {
	ClientMessageID ClientMessageID
	Kind            ControlKind
	ExpectedTurnID  TurnID
	Text            string
}
type Acceptance struct {
	MessageID       int64
	ClientMessageID ClientMessageID
	Seq             Seq
	State           RequestState
	ErrorCode       string
	Duplicate       bool
	Event           DurableEvent
}
type RecoveryAction string

const (
	RecoveryRetry    RecoveryAction = "retry"
	RecoverySkip     RecoveryAction = "skip"
	RecoveryContinue RecoveryAction = "continue"
)

type RecoverInput struct {
	ClientMessageID      ClientMessageID
	TargetMessageID      ClientMessageID
	Action               RecoveryAction
	ReplacementMessageID ClientMessageID
	Instruction          string
}
type RetryKind string

const (
	RetryPrompt RetryKind = "prompt"
	RetrySteer  RetryKind = "steer"
	RetryCancel RetryKind = "cancel"
)

type RetryCommand struct {
	Kind            RetryKind
	ClientMessageID ClientMessageID
	Actor           Actor
	ExpectedTurnID  TurnID
	Text            string
}
type RecoveryResult struct {
	Duplicate bool
	Events    []DurableEvent
	Retry     *RetryCommand
}
type DeliveryCertainty string

const (
	DeliveryNotSent DeliveryCertainty = "not-sent"
	DeliveryUnknown DeliveryCertainty = "unknown"
)

type MutationError struct {
	Operation string
	Certainty DeliveryCertainty
	Err       error
}

func (e *MutationError) Error() string { return e.Operation + ": " + e.Err.Error() }
func (e *MutationError) Unwrap() error { return e.Err }

var ErrBlankMessage = errors.New("message text is blank")
var ErrInvalidClientMessageID = errors.New("client message id must be 32 lowercase hexadecimal characters")
var ErrInvalidConnectionID = errors.New("connection id must be 32 lowercase hexadecimal characters")
var ErrExpectedTurnID = errors.New("expected turn id is required")
var ErrInvalidControlKind = errors.New("invalid control kind")
var ErrInvalidRecovery = errors.New("invalid recovery input")

func (in SubmitInput) Validate() error {
	if !ValidClientMessageID(in.ClientMessageID) {
		return ErrInvalidClientMessageID
	}
	if strings.TrimSpace(in.Text) == "" {
		return ErrBlankMessage
	}
	return nil
}

func (in SteerInput) Validate() error {
	if !ValidClientMessageID(in.ClientMessageID) {
		return ErrInvalidClientMessageID
	}
	if strings.TrimSpace(string(in.ExpectedTurnID)) == "" {
		return ErrExpectedTurnID
	}
	if strings.TrimSpace(in.Text) == "" {
		return ErrBlankMessage
	}
	return nil
}

func (in CancelInput) Validate() error {
	if !ValidClientMessageID(in.ClientMessageID) {
		return ErrInvalidClientMessageID
	}
	if strings.TrimSpace(string(in.ExpectedTurnID)) == "" {
		return ErrExpectedTurnID
	}
	return nil
}

func (in ControlInput) Validate() error {
	if !ValidClientMessageID(in.ClientMessageID) {
		return ErrInvalidClientMessageID
	}
	if strings.TrimSpace(string(in.ExpectedTurnID)) == "" {
		return ErrExpectedTurnID
	}
	switch in.Kind {
	case ControlSteer:
		if strings.TrimSpace(in.Text) == "" {
			return ErrBlankMessage
		}
	case ControlCancel:
		if in.Text != "" {
			return ErrInvalidControlKind
		}
	default:
		return ErrInvalidControlKind
	}
	return nil
}

func (in RecoverInput) Validate() error {
	if !ValidClientMessageID(in.ClientMessageID) || !ValidClientMessageID(in.TargetMessageID) {
		return ErrInvalidClientMessageID
	}
	switch in.Action {
	case RecoveryRetry, RecoverySkip:
		if in.ReplacementMessageID != "" || in.Instruction != "" {
			return ErrInvalidRecovery
		}
	case RecoveryContinue:
		if !ValidClientMessageID(in.ReplacementMessageID) || in.ReplacementMessageID == in.ClientMessageID || in.ReplacementMessageID == in.TargetMessageID {
			return ErrInvalidRecovery
		}
		if strings.TrimSpace(in.Instruction) == "" {
			return ErrBlankMessage
		}
	default:
		return ErrInvalidRecovery
	}
	return nil
}

func ValidClientMessageID(id ClientMessageID) bool {
	return validHexID(string(id))
}

func ValidConnectionID(id ConnectionID) bool { return validHexID(string(id)) }

func validHexID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, b := range []byte(id) {
		if !('0' <= b && b <= '9') && !('a' <= b && b <= 'f') {
			return false
		}
	}
	return true
}

type Member struct {
	UID  UID
	Name string
}
type DurableEvent struct {
	Seq       Seq
	Kind      string
	ActorUID  UID
	Payload   json.RawMessage
	CreatedAt time.Time
}
type TransientEvent struct {
	Revision uint64
	Kind     string
	ThreadID ThreadID
	TurnID   TurnID
	ItemID   ItemID
	Delta    string
}
type CompletedItem struct {
	ThreadID ThreadID
	TurnID   TurnID
	ItemID   ItemID
	Payload  json.RawMessage
}
type TurnSnapshot struct {
	ID    TurnID
	State RequestState
	// Items contains only complete replacements proven by the adapter's
	// transport evidence. Unfinished historical items are never CompletedItem.
	Items []CompletedItem
}
type ThreadSnapshot struct {
	ID    ThreadID
	CWD   string
	Turns []TurnSnapshot
}
type TurnBinding struct {
	MessageID ClientMessageID
	TurnID    TurnID
	State     RequestState
}
type QueuedMessage struct {
	Input       SubmitInput
	Actor       Actor
	AcceptedSeq Seq
}
type RuntimeProcessState string

const (
	RuntimeProcessRunning RuntimeProcessState = "running"
	RuntimeProcessStopped RuntimeProcessState = "stopped"
)

type RuntimeCheckpoint struct {
	State        RuntimeProcessState
	Generation   string
	PID          int
	PGID         int
	ProcessStart string
	CodexVersion string
	SchemaSHA256 string
}
type RecoveryImage struct {
	Status          RoomStatus
	ThreadID        ThreadID
	Active          *TurnBinding
	PendingControls []TurnBinding
	Queue           []QueuedMessage
	Checkpoint      RuntimeCheckpoint
}
type FailureOutcome struct {
	State       RequestState
	ErrorCode   string
	ErrorDigest string
}
type ControlOutcome struct {
	State       RequestState
	ErrorCode   string
	ErrorDigest string
}
type FinishTurnInput struct {
	TurnID      TurnID
	State       RequestState
	ErrorCode   string
	ErrorDigest string
}
type ReviewReason struct {
	Code         string
	DetailDigest string
}
type RepairReason struct {
	Code         string
	DetailDigest string
}
type AgentEvent struct {
	Kind      string
	ThreadID  ThreadID
	TurnID    TurnID
	ItemID    ItemID
	Delta     string
	Completed *CompletedItem
	Error     error
}
type LiveItemSnapshot struct {
	ThreadID ThreadID
	TurnID   TurnID
	ItemID   ItemID
	Partial  string
}
type Snapshot struct {
	ProjectionTruncated bool
	Status              RoomStatus
	ThreadID            ThreadID
	Active              *TurnBinding
	Queue               []QueuedMessage
	ProjectionRevision  uint64
	LiveItems           []LiveItemSnapshot
	LatestSeq           Seq
}
type ConnectionRecord struct {
	ID             ConnectionID
	RoomID         RoomID
	UID            UID
	ConnectedAt    time.Time
	DisconnectedAt *time.Time
	LastAck        Seq
}

func (record ConnectionRecord) Validate() error {
	if !ValidConnectionID(record.ID) {
		return ErrInvalidConnectionID
	}
	return nil
}
