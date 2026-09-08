package codex

import (
	"encoding/json"

	"agent_romm/internal/room"
)

const (
	approvalPolicy  = "never"
	threadSandbox   = "danger-full-access"
	turnSandboxType = "dangerFullAccess"
)

type initializeParams struct {
	ClientInfo clientInfo `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeResponse struct {
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
	UserAgent      string `json:"userAgent"`
}

type accountReadParams struct {
	RefreshToken bool `json:"refreshToken"`
}

type accountReadResponse struct {
	Account            json.RawMessage `json:"account"`
	RequiresOpenAIAuth *bool           `json:"requiresOpenaiAuth"`
}

type threadStartParams struct {
	CWD            string `json:"cwd"`
	ApprovalPolicy string `json:"approvalPolicy"`
	Sandbox        string `json:"sandbox"`
}

type threadReadParams struct {
	ThreadID     string `json:"threadId"`
	IncludeTurns bool   `json:"includeTurns"`
}

type threadResumeParams struct {
	ThreadID       string `json:"threadId"`
	CWD            string `json:"cwd"`
	ApprovalPolicy string `json:"approvalPolicy"`
	Sandbox        string `json:"sandbox"`
}

type textInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type turnStartParams struct {
	ThreadID            string        `json:"threadId"`
	Input               []textInput   `json:"input"`
	ClientUserMessageID string        `json:"clientUserMessageId"`
	CWD                 string        `json:"cwd"`
	ApprovalPolicy      string        `json:"approvalPolicy"`
	SandboxPolicy       sandboxPolicy `json:"sandboxPolicy"`
}

type sandboxPolicy struct {
	Type string `json:"type"`
}

type turnSteerParams struct {
	ThreadID       string      `json:"threadId"`
	Input          []textInput `json:"input"`
	ExpectedTurnID string      `json:"expectedTurnId"`
}

type turnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type protocolThread struct {
	ID    string          `json:"id"`
	CWD   *string         `json:"cwd"`
	Turns json.RawMessage `json:"turns"`
}

type protocolTurn struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Items  json.RawMessage `json:"items"`
}

type threadResult struct {
	Thread json.RawMessage `json:"thread"`
}

type policyThreadResult struct {
	Thread         json.RawMessage `json:"thread"`
	CWD            *string         `json:"cwd"`
	ApprovalPolicy *string         `json:"approvalPolicy"`
	Sandbox        *sandboxPolicy  `json:"sandbox"`
}

type turnStartResult struct {
	Turn json.RawMessage `json:"turn"`
}

type turnSteerResult struct {
	TurnID *string `json:"turnId"`
}

type deltaNotification struct {
	ThreadID     string  `json:"threadId"`
	TurnID       string  `json:"turnId"`
	ItemID       string  `json:"itemId"`
	Delta        *string `json:"delta"`
	SummaryIndex *int64  `json:"summaryIndex"`
	ContentIndex *int64  `json:"contentIndex"`
}

type itemNotification struct {
	ThreadID      string          `json:"threadId"`
	TurnID        string          `json:"turnId"`
	Item          json.RawMessage `json:"item"`
	StartedAtMs   *int64          `json:"startedAtMs"`
	CompletedAtMs *int64          `json:"completedAtMs"`
}

type turnNotification struct {
	ThreadID string          `json:"threadId"`
	Turn     json.RawMessage `json:"turn"`
}

type startedItem struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type InitializeResult struct {
	UserAgent          string
	CodexHome          string
	PlatformFamily     string
	PlatformOS         string
	RequiresOpenAIAuth bool
	AccountPresent     bool
}

func mapTurnState(status string) (room.RequestState, bool) {
	switch status {
	case "completed":
		return room.RequestCompleted, true
	case "interrupted":
		return room.RequestInterrupted, true
	case "failed":
		return room.RequestFailed, true
	case "inProgress":
		return room.RequestRunning, true
	default:
		return "", false
	}
}
