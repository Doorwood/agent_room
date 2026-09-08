package client

import (
	"agent_romm/internal/protocol"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// Submission carries a stable browser message ID through reconnects and retries.
// Result is buffered so an expired HTTP request cannot block the client loop.
type Submission struct {
	TaskID         int64
	Method         string
	ExpectedTurnID string
	Context        context.Context
	ID             string
	Text           string
	Result         chan error
}

func (s Submission) envelope() (protocol.Envelope, error) {
	method := s.Method
	if method == "" {
		method = "submit"
	}
	var body any
	switch method {
	case "submit", "task_submit":
		if (method == "task_submit") != (s.TaskID > 0) {
			return protocol.Envelope{}, errors.New("invalid task submission")
		}
		request := protocol.SubmitRequest{ClientMessageID: s.ID, Text: s.Text, TaskID: s.TaskID}
		if err := request.Validate(); err != nil {
			return protocol.Envelope{}, err
		}
		body = request
	case "cancel":
		request := protocol.CancelRequest{ClientMessageID: s.ID, ExpectedTurnID: s.ExpectedTurnID}
		if err := request.Validate(); err != nil {
			return protocol.Envelope{}, err
		}
		body = request
	default:
		return protocol.Envelope{}, errors.New("unsupported browser operation")
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return protocol.Envelope{}, err
	}
	if len(encoded) > 64<<10 {
		return protocol.Envelope{}, errors.New("message exceeds 64 KiB")
	}
	sum := sha256.Sum256([]byte("browser-request:" + s.ID))
	return protocol.Envelope{Version: 1, Kind: protocol.KindRequest, ID: hex.EncodeToString(sum[:16]), Method: method, Body: encoded}, nil
}
func (s Submission) Validate() error { _, err := s.envelope(); return err }
func resolveSubmission(result chan error, err error) {
	select {
	case result <- err:
	default:
	}
}
