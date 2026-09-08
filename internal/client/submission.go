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
	Context context.Context
	ID      string
	Text    string
	Result  chan error
}

func (s Submission) envelope() (protocol.Envelope, error) {
	body := protocol.SubmitRequest{ClientMessageID: s.ID, Text: s.Text}
	if err := body.Validate(); err != nil {
		return protocol.Envelope{}, err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return protocol.Envelope{}, err
	}
	if len(encoded) > 64<<10 {
		return protocol.Envelope{}, errors.New("message exceeds 64 KiB")
	}
	sum := sha256.Sum256([]byte("browser-request:" + s.ID))
	return protocol.Envelope{Version: 1, Kind: protocol.KindRequest, ID: hex.EncodeToString(sum[:16]), Method: "submit", Body: encoded}, nil
}
func (s Submission) Validate() error { _, err := s.envelope(); return err }
func resolveSubmission(result chan error, err error) {
	select {
	case result <- err:
	default:
	}
}
