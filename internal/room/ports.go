package room

import (
	"context"
	"time"
)

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

type Clock interface{ Now() time.Time }
type IDSource interface {
	NewClientMessageID() (ClientMessageID, error)
	NewConnectionID() (ConnectionID, error)
}
