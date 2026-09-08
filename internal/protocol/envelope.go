package protocol

import (
	"encoding/json"
	"errors"
	"strings"
)

const Version uint16 = 1

type Kind string

const (
	KindRequest  Kind = "request"
	KindResponse Kind = "response"
	KindEvent    Kind = "event"
	KindError    Kind = "error"
)

type Envelope struct {
	Version  uint16          `json:"version"`
	Kind     Kind            `json:"kind"`
	ID       string          `json:"id,omitempty"`
	Method   string          `json:"method"`
	Requires []string        `json:"requires,omitempty"`
	Seq      *uint64         `json:"seq,omitempty"`
	Body     json.RawMessage `json:"body"`
}

var (
	ErrInvalidEnvelope = errors.New("invalid envelope")
	ErrInvalidVersion  = errors.New("unsupported protocol version")
	ErrInvalidKind     = errors.New("invalid envelope kind")
	ErrInvalidID       = errors.New("invalid envelope id")
)

func (envelope Envelope) Validate() error {
	if envelope.Version != Version {
		return ErrInvalidVersion
	}
	switch envelope.Kind {
	case KindRequest, KindResponse, KindEvent, KindError:
	default:
		return ErrInvalidKind
	}
	if strings.TrimSpace(envelope.Method) == "" {
		return ErrInvalidEnvelope
	}
	if envelope.Kind == KindRequest || envelope.Kind == KindResponse {
		if !ValidMessageID(envelope.ID) {
			return ErrInvalidID
		}
	} else if envelope.ID != "" && !ValidMessageID(envelope.ID) {
		return ErrInvalidID
	}
	if len(envelope.Body) == 0 {
		return ErrInvalidEnvelope
	}
	return validateJSON(envelope.Body)
}
