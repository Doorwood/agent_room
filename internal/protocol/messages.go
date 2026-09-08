package protocol

import (
	"errors"
	"strings"

	"agent_romm/internal/room"
)

type Hello struct {
	MinVersion     uint16 `json:"minVersion"`
	MaxVersion     uint16 `json:"maxVersion"`
	LastAppliedSeq uint64 `json:"lastAppliedSeq"`
}

func (hello Hello) Validate() error {
	if hello.MinVersion == 0 || hello.MinVersion > hello.MaxVersion {
		return ErrInvalidHello
	}
	return nil
}

type Welcome struct {
	RoomID          string `json:"roomId"`
	RoomName        string `json:"roomName"`
	ProjectRoot     string `json:"projectRoot"`
	ExecutionOwner  string `json:"executionOwner"`
	FullOwnerAccess bool   `json:"fullOwnerAccess"`
	ActiveTurnID    string `json:"activeTurnId,omitempty"`
	LatestSeq       uint64 `json:"latestSeq"`
}

func (welcome Welcome) Validate() error {
	if strings.TrimSpace(welcome.RoomID) == "" || strings.TrimSpace(welcome.RoomName) == "" || strings.TrimSpace(welcome.ProjectRoot) == "" || strings.TrimSpace(welcome.ExecutionOwner) == "" {
		return ErrInvalidWelcome
	}
	return nil
}

type SubmitRequest struct {
	ClientMessageID string `json:"clientMessageId"`
	Text            string `json:"text"`
}

func (request SubmitRequest) Validate() error {
	return (room.SubmitInput{ClientMessageID: room.ClientMessageID(request.ClientMessageID), Text: request.Text}).Validate()
}

type SteerRequest struct {
	ClientMessageID string `json:"clientMessageId"`
	ExpectedTurnID  string `json:"expectedTurnId"`
	Text            string `json:"text"`
}

func (request SteerRequest) Validate() error {
	return (room.SteerInput{ClientMessageID: room.ClientMessageID(request.ClientMessageID), ExpectedTurnID: room.TurnID(request.ExpectedTurnID), Text: request.Text}).Validate()
}

type CancelRequest struct {
	ClientMessageID string `json:"clientMessageId"`
	ExpectedTurnID  string `json:"expectedTurnId"`
}

func (request CancelRequest) Validate() error {
	return (room.CancelInput{ClientMessageID: room.ClientMessageID(request.ClientMessageID), ExpectedTurnID: room.TurnID(request.ExpectedTurnID)}).Validate()
}

type RecoverRequest struct {
	ClientMessageID      string `json:"clientMessageId"`
	TargetMessageID      string `json:"targetMessageId"`
	Action               string `json:"action"`
	ReplacementMessageID string `json:"replacementMessageId,omitempty"`
	Instruction          string `json:"instruction,omitempty"`
}

func (request RecoverRequest) Validate() error {
	return (room.RecoverInput{
		ClientMessageID:      room.ClientMessageID(request.ClientMessageID),
		TargetMessageID:      room.ClientMessageID(request.TargetMessageID),
		Action:               room.RecoveryAction(request.Action),
		ReplacementMessageID: room.ClientMessageID(request.ReplacementMessageID),
		Instruction:          request.Instruction,
	}).Validate()
}

type LiveItem struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Partial  string `json:"partial"`
}

type RuntimeSnapshot struct {
	ProjectionTruncated bool       `json:"projectionTruncated"`
	ProjectionRevision  uint64     `json:"projectionRevision"`
	DurableWatermark    uint64     `json:"durableWatermark"`
	LiveItems           []LiveItem `json:"liveItems"`
	ActiveTurnID        string     `json:"activeTurnId"`
}

type Ack struct {
	Seq uint64 `json:"seq"`
}

type Heartbeat struct {
	UnixMilli int64 `json:"unixMilli"`
}

type Empty struct{}

var (
	ErrInvalidHello             = errors.New("invalid hello")
	ErrInvalidWelcome           = errors.New("invalid welcome")
	ErrHandshakeRequired        = errors.New("hello handshake required")
	ErrHandshakeAlreadyComplete = errors.New("hello handshake already complete")
	ErrUnsupportedCapability    = errors.New("unsupported required capability")
	ErrUnsupportedVersion       = errors.New("unsupported protocol version range")
)

type HandshakeState struct {
	capabilities map[string]struct{}
	complete     bool
}

func NewHandshakeState(capabilities []string) *HandshakeState {
	supported := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		supported[capability] = struct{}{}
	}
	return &HandshakeState{capabilities: supported}
}

func (state *HandshakeState) Accept(envelope Envelope) error {
	if err := envelope.Validate(); err != nil {
		return err
	}
	if !state.complete {
		if envelope.Kind != KindRequest || envelope.Method != "hello" {
			return ErrHandshakeRequired
		}
		for _, capability := range envelope.Requires {
			if _, ok := state.capabilities[capability]; !ok {
				return ErrUnsupportedCapability
			}
		}
		hello, err := DecodeBody[Hello](envelope.Body)
		if err != nil {
			return err
		}
		if err := hello.Validate(); err != nil {
			return err
		}
		if hello.MinVersion > Version || hello.MaxVersion < Version {
			return ErrUnsupportedVersion
		}
		state.complete = true
		return nil
	}
	if envelope.Kind == KindRequest && envelope.Method == "hello" {
		return ErrHandshakeAlreadyComplete
	}
	return nil
}

func (state *HandshakeState) Complete() bool { return state.complete }
