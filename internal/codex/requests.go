package codex

import (
	"encoding/json"
	"unicode/utf8"
)

func startThreadRequest(projectRoot string) threadStartParams {
	return threadStartParams{
		CWD: projectRoot, ApprovalPolicy: approvalPolicy, Sandbox: threadSandbox,
	}
}

func resumeThreadRequest(threadID, projectRoot string) threadResumeParams {
	return threadResumeParams{
		ThreadID: threadID, CWD: projectRoot,
		ApprovalPolicy: approvalPolicy, Sandbox: threadSandbox,
	}
}

func startTurnRequest(threadID, clientMessageID, projectRoot, text string) turnStartParams {
	return turnStartParams{
		ThreadID:            threadID,
		Input:               []textInput{{Type: "text", Text: text}},
		ClientUserMessageID: clientMessageID,
		CWD:                 projectRoot,
		ApprovalPolicy:      approvalPolicy,
		SandboxPolicy:       sandboxPolicy{Type: turnSandboxType},
	}
}

func steerTurnRequest(threadID, turnID, text string) turnSteerParams {
	return turnSteerParams{
		ThreadID:       threadID,
		Input:          []textInput{{Type: "text", Text: text}},
		ExpectedTurnID: turnID,
	}
}

func preflightStartThreadRequest(projectRoot string) error {
	return preflightCall("thread/start", startThreadRequest(""), projectRoot)
}

func preflightReadThreadRequest(threadID string) error {
	return preflightCall("thread/read", threadReadParams{IncludeTurns: true}, threadID)
}

func preflightResumeThreadRequest(threadID, projectRoot string) error {
	return preflightCall("thread/resume", resumeThreadRequest("", ""), threadID, projectRoot)
}

func preflightStartTurnRequest(threadID, clientMessageID, projectRoot, text string) error {
	return preflightCall("turn/start", startTurnRequest("", "", "", ""), threadID, clientMessageID, projectRoot, text)
}

func preflightSteerTurnRequest(threadID, turnID, text string) error {
	return preflightCall("turn/steer", steerTurnRequest("", "", ""), threadID, turnID, text)
}

func preflightInterruptTurnRequest(threadID, turnID string) error {
	return preflightCall("turn/interrupt", turnInterruptParams{}, threadID, turnID)
}

// preflightCall marshals only a small baseline whose dynamic strings are all
// empty, then adds their exact default JSON-escaped lengths without allocating
// their encodings. The shortest numeric request ID is intentional: an actual
// near-boundary ID may still be rejected by RPCClient.Call's authoritative
// marshal check.
func preflightCall(method string, emptyDynamicParams any, dynamicStrings ...string) error {
	baseline, err := json.Marshal(struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{ID: 1, Method: method, Params: emptyDynamicParams})
	if err != nil {
		return ErrInvalidWireMessage
	}
	total := len(baseline)
	for _, value := range dynamicStrings {
		escapedLength := defaultJSONEscapedStringLength(value)
		if escapedLength > MaxJSONLLineBytes {
			return ErrRPCMessageTooLarge
		}
		delta := escapedLength - 2 // replace the baseline's empty string literal
		if total > MaxJSONLLineBytes-delta {
			return ErrRPCMessageTooLarge
		}
		total += delta
	}
	return nil
}

// defaultJSONEscapedStringLength matches encoding/json's default HTML-safe
// string encoding and saturates just above the outbound wire limit.
func defaultJSONEscapedStringLength(value string) int {
	length := 2 // surrounding quotes
	for index := 0; index < len(value); {
		encoded := 0
		if value[index] < utf8.RuneSelf {
			switch value[index] {
			case '\\', '"', '\b', '\f', '\n', '\r', '\t':
				encoded = 2
			case '<', '>', '&':
				encoded = 6
			default:
				if value[index] < 0x20 {
					encoded = 6
				} else {
					encoded = 1
				}
			}
			index++
		} else {
			r, size := utf8.DecodeRuneInString(value[index:])
			switch {
			case r == utf8.RuneError && size == 1:
				encoded = 6
			case r == '\u2028' || r == '\u2029':
				encoded = 6
			default:
				encoded = size
			}
			index += size
		}
		if length > MaxJSONLLineBytes-encoded {
			return MaxJSONLLineBytes + 1
		}
		length += encoded
	}
	return length
}
