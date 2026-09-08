package codex

import (
	"agent_romm/internal/room"
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestQuestionUsesSameThreadReadOnlyAndMainPolicyRestored(t *testing.T) {
	var requests []turnStartParams
	rpc, _ := newRPCPeer(t, func(req map[string]json.RawMessage) json.RawMessage {
		switch string(req["method"]) {
		case `"config/read"`:
			return json.RawMessage(`{"config":{}}`)
		case `"mcpServerStatus/list"`:
			return json.RawMessage(`{"data":[],"nextCursor":null}`)
		case `"turn/start"`:
			var p turnStartParams
			if err := json.Unmarshal(req["params"], &p); err != nil {
				t.Error(err)
			}
			requests = append(requests, p)
			return json.RawMessage(`{"turn":{"id":"turn-1","status":"inProgress","items":[]}}`)
		}
		t.Errorf("unexpected request %s", req["method"])
		return nil
	})
	a, err := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.StartReadOnlyTurn(context.Background(), "thread-1", testClientMessageID, "explain"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.StartTurn(context.Background(), "thread-1", testClientMessageID, "authorized work"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].ThreadID != "thread-1" || requests[0].SandboxPolicy.Type != "readOnly" || requests[0].ApprovalPolicy != "never" || requests[1].SandboxPolicy.Type != "dangerFullAccess" {
		t.Fatalf("policies: %#v", requests)
	}
}
func TestQuestionRefusesExtensionsBeforeStartingTurn(t *testing.T) {
	for _, config := range []string{`null`, `{"mcp_servers":{"writer":{}}}`, `{"apps":{"_default":{"enabled":true}}}`, `{"hooks":{"Stop":[]}}`, `{"features":{"multi_agent":true}}`} {
		t.Run(config, func(t *testing.T) {
			calls := 0
			rpc, _ := newRPCPeer(t, func(req map[string]json.RawMessage) json.RawMessage {
				calls++
				if string(req["method"]) != `"config/read"` {
					t.Error("unsafe call", string(req["method"]))
				}
				return json.RawMessage(`{"config":` + config + `}`)
			})
			a, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
			_, err := a.StartReadOnlyTurn(context.Background(), "thread-1", testClientMessageID, "write a file")
			var mutation *room.MutationError
			if !errors.As(err, &mutation) || mutation.Certainty != room.DeliveryNotSent || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}
