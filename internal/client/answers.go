package client

import (
	"agent_romm/internal/room"
	"encoding/json"
)

// Completed agent messages are authoritative. Deltas also contain tool output
// and lack phase information, so they must not appear in the answer window.
func answerText(e room.DurableEvent) (string, error) {
	if e.Kind != "item/completed" {
		return "", nil
	}
	var body struct {
		Payload struct {
			Type  string `json:"type"`
			Phase string `json:"phase"`
			Text  string `json:"text"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(e.Payload, &body); err != nil {
		return "", err
	}
	if body.Payload.Type != "agentMessage" || (body.Payload.Phase != "" && body.Payload.Phase != "final_answer") {
		return "", nil
	}
	return body.Payload.Text, nil
}

// ReplayCursors keeps this window's progress separate from interactive clients.
// A fresh window replays history; reconnects within it resume without duplicates.
type ReplayCursors struct{ cursor Cursor }

func (m *ReplayCursors) Load(target string) (Cursor, error) {
	if m.cursor.Target == "" {
		m.cursor.Target = target
	}
	return m.cursor, nil
}
func (m *ReplayCursors) Save(c Cursor) error { m.cursor = c; return nil }
