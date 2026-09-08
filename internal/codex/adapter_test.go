package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"agent_romm/internal/room"
	"agent_romm/internal/store"
)

const testClientMessageID = room.ClientMessageID("00000000000000000000000000000001")

type recoveryTestSink struct{}

func (recoveryTestSink) PublishDurable(room.DurableEvent)     {}
func (recoveryTestSink) PublishTransient(room.TransientEvent) {}
func (recoveryTestSink) Now() time.Time                       { return time.Unix(1, 0) }

func TestSnapshotCompletionEvidenceMatchesPinnedItemStatuses(t *testing.T) {
	adapter := &Adapter{projectRoot: "/srv/project"}
	terminals := map[string][]string{
		"commandExecution":    {"completed", "failed", "declined"},
		"fileChange":          {"completed", "failed", "declined"},
		"mcpToolCall":         {"completed", "failed"},
		"dynamicToolCall":     {"completed", "failed"},
		"collabAgentToolCall": {"completed", "failed", "interrupted"},
	}
	for kind, allowed := range terminals {
		for _, status := range []string{"inProgress", "completed", "failed", "declined", "interrupted", "unknown", ""} {
			for _, turnStatus := range []string{"inProgress", "completed", "failed", "interrupted"} {
				t.Run(kind+"/"+status+"/"+turnStatus, func(t *testing.T) {
					want := false
					for _, terminal := range allowed {
						want = want || status == terminal
					}
					raw := json.RawMessage(fmt.Sprintf(`{"id":"thread-1","cwd":"/srv/project","turns":[{"id":"turn-1","status":%q,"items":[{"id":"item-1","type":%q,"status":%q}]}]}`, turnStatus, kind, status))
					snapshot, err := adapter.snapshot(raw, "thread-1")
					if err != nil {
						t.Fatal(err)
					}
					if got := len(snapshot.Turns[0].Items) == 1; got != want {
						t.Fatalf("completed=%v want=%v", got, want)
					}
				})
			}
		}
	}
	for _, turnStatus := range []string{"inProgress", "completed", "failed", "interrupted"} {
		t.Run("statusless/"+turnStatus, func(t *testing.T) {
			raw := json.RawMessage(fmt.Sprintf(`{"id":"thread-1","cwd":"/srv/project","turns":[{"id":"turn-1","status":%q,"items":[{"id":"item-1","type":"agentMessage","text":"may be partial"}]}]}`, turnStatus))
			snapshot, err := adapter.snapshot(raw, "thread-1")
			if err != nil {
				t.Fatal(err)
			}
			if got := len(snapshot.Turns[0].Items) == 1; got != (turnStatus == "completed") {
				t.Fatalf("snapshot=%#v", snapshot)
			}
		})
	}
}

func TestSnapshotRecoveryPersistsOnlyCompletedCommandOutput(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	actor := room.Actor{UID: 1001, Name: "alice"}
	if err := s.InitializeRoom(ctx, store.RoomSeed{ID: "team", DisplayName: "Team", HostID: "host-1", ProjectRoot: "/srv/project", ExecutionOwnerUID: actor.UID, Members: []room.Member{{UID: actor.UID, Name: actor.Name}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindThread(ctx, "team", room.ThreadSnapshot{ID: "thread-1", CWD: "/srv/project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptMessage(ctx, "team", actor, room.SubmitInput{ClientMessageID: testClientMessageID, Text: "run command"}); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginDispatch(ctx, "team", testClientMessageID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindRunningTurn(ctx, "team", testClientMessageID, "turn-1"); err != nil {
		t.Fatal(err)
	}
	partial := `{"type":"commandExecution","id":"item-1","command":"echo final","cwd":"/srv/project","processId":null,"status":"inProgress","commandActions":[],"aggregatedOutput":"par","exitCode":null,"durationMs":null}`
	final := `{"type":"commandExecution","id":"item-1","command":"echo final","cwd":"/srv/project","processId":null,"status":"completed","commandActions":[],"aggregatedOutput":"final","exitCode":0,"durationMs":1}`
	rpc, _ := newRPCPeer(t, func(request map[string]json.RawMessage) json.RawMessage {
		if string(request["method"]) == `"thread/read"` {
			return policyThreadResponse("thread-1", "/srv/project", `[{"id":"turn-1","status":"inProgress","items":[`+partial+`]}]`)
		}
		return policyThreadResponse("thread-1", "/srv/project", `[{"id":"turn-1","status":"completed","items":[`+final+`]}]`)
	})
	adapter, err := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := room.NewCoordinator("team", "/srv/project", s, adapter, recoveryTestSink{}, recoveryTestSink{})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(runCtx) }()
	defer func() { cancel(); <-done }()
	if err := c.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, "team", 0, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Kind != "item/completed" {
			continue
		}
		count++
		var payload struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if string(payload.Payload) != final {
			t.Fatalf("persisted unfinished item=%s", payload.Payload)
		}
	}
	snap, _ := c.Snapshot(ctx)
	if count != 1 || snap.Status != room.RoomReady || snap.Active != nil {
		t.Fatalf("count=%d snapshot=%#v", count, snap)
	}
}

type rpcTestPeer struct {
	t        *testing.T
	reader   *bufio.Reader
	writer   *io.PipeWriter
	requests chan []byte
	done     chan error
	cancel   context.CancelFunc
	onCall   func(map[string]json.RawMessage) json.RawMessage
}

func newRPCPeer(t *testing.T, onCall func(map[string]json.RawMessage) json.RawMessage) (*RPCClient, *rpcTestPeer) {
	t.Helper()
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	rpc := NewRPCClient(clientReads, clientWrites)
	ctx, cancel := context.WithCancel(context.Background())
	peer := &rpcTestPeer{
		t: t, reader: bufio.NewReader(serverReads), writer: serverWrites,
		requests: make(chan []byte, 32), done: make(chan error, 1), cancel: cancel, onCall: onCall,
	}
	go func() { peer.done <- rpc.Run(ctx) }()
	go peer.serve()
	t.Cleanup(func() {
		cancel()
		_ = serverWrites.Close()
		_ = serverReads.Close()
		_ = clientReads.Close()
		_ = clientWrites.Close()
	})
	return rpc, peer
}

func (p *rpcTestPeer) serve() {
	for {
		line, err := p.reader.ReadBytes('\n')
		if err != nil {
			return
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		copyLine := append([]byte(nil), line...)
		p.requests <- copyLine
		var message map[string]json.RawMessage
		if err := json.Unmarshal(line, &message); err != nil {
			return
		}
		id, isCall := message["id"]
		if !isCall {
			continue
		}
		if _, hasMethod := message["method"]; !hasMethod {
			continue
		}
		result := json.RawMessage(`{}`)
		if p.onCall != nil {
			result = p.onCall(message)
		}
		response, _ := json.Marshal(struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
		}{ID: id, Result: result})
		response = append(response, '\n')
		if _, err := p.writer.Write(response); err != nil {
			return
		}
	}
}

func (p *rpcTestPeer) nextRequest(t *testing.T) []byte {
	t.Helper()
	select {
	case request := <-p.requests:
		return request
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for request")
		return nil
	}
}

func methodOf(message map[string]json.RawMessage) string {
	var method string
	_ = json.Unmarshal(message["method"], &method)
	return method
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "golden", name))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSpace(data)
}

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	want := readGolden(t, name)
	if !bytes.Equal(got, want) {
		t.Fatalf("request mismatch\n got: %s\nwant: %s", got, want)
	}
}

func threadJSON(id, cwd string, turns string) string {
	return `{"id":"` + id + `","cwd":"` + cwd + `","turns":` + turns + `}`
}

func jsonArrayOf(element string, count int) string {
	if count == 0 {
		return "[]"
	}
	return "[" + strings.Repeat(element+",", count-1) + element + "]"
}

func policyThreadResponse(id, cwd string, turns string) json.RawMessage {
	return json.RawMessage(`{"thread":` + threadJSON(id, cwd, turns) + `,"cwd":"` + cwd + `","approvalPolicy":"never","sandbox":{"type":"dangerFullAccess"}}`)
}

func TestAdapterRequestGoldens(t *testing.T) {
	t.Run("initialize and account read", func(t *testing.T) {
		rpc, peer := newRPCPeer(t, func(request map[string]json.RawMessage) json.RawMessage {
			switch methodOf(request) {
			case "initialize":
				return json.RawMessage(`{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"linux","userAgent":"agent_romm/0.151.0-alpha.7.2 pinned"}`)
			case "account/read":
				return json.RawMessage(`{"account":{"type":"apiKey"},"requiresOpenaiAuth":true}`)
			default:
				return json.RawMessage(`{}`)
			}
		})
		adapter, err := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := adapter.Initialize(context.Background(), SupportedCLIOutput)
		if err != nil {
			t.Fatal(err)
		}
		if result.UserAgent != "agent_romm/0.151.0-alpha.7.2 pinned" {
			t.Fatalf("userAgent=%q", result.UserAgent)
		}
		assertGolden(t, "initialize.json", peer.nextRequest(t))
		assertGolden(t, "initialized.json", peer.nextRequest(t))
		assertGolden(t, "account-read.json", peer.nextRequest(t))
	})

	t.Run("thread start", func(t *testing.T) {
		rpc, peer := newRPCPeer(t, func(map[string]json.RawMessage) json.RawMessage {
			return policyThreadResponse("thread-1", "/srv/project", `[]`)
		})
		adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
		snapshot, err := adapter.StartThread(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.ID != "thread-1" || snapshot.CWD != "/srv/project" {
			t.Fatalf("snapshot=%#v", snapshot)
		}
		assertGolden(t, "thread-start.json", peer.nextRequest(t))
	})

	t.Run("thread read", func(t *testing.T) {
		rpc, peer := newRPCPeer(t, func(map[string]json.RawMessage) json.RawMessage {
			return json.RawMessage(`{"thread":` + threadJSON("thread-1", "/srv/project", `[]`) + `}`)
		})
		adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
		if _, err := adapter.ReadThread(context.Background(), "thread-1"); err != nil {
			t.Fatal(err)
		}
		assertGolden(t, "thread-read.json", peer.nextRequest(t))
	})

	t.Run("thread resume", func(t *testing.T) {
		rpc, peer := newRPCPeer(t, func(map[string]json.RawMessage) json.RawMessage {
			return policyThreadResponse("thread-1", "/srv/project", `[]`)
		})
		adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
		if _, err := adapter.ResumeThread(context.Background(), "thread-1"); err != nil {
			t.Fatal(err)
		}
		assertGolden(t, "thread-resume.json", peer.nextRequest(t))
	})

	t.Run("turn start", func(t *testing.T) {
		rpc, peer := newRPCPeer(t, func(map[string]json.RawMessage) json.RawMessage {
			return json.RawMessage(`{"turn":{"id":"turn-1","status":"inProgress","items":[]}}`)
		})
		adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
		turnID, err := adapter.StartTurn(context.Background(), "thread-1", testClientMessageID, "[participant: alice]\nrun tests")
		if err != nil {
			t.Fatal(err)
		}
		if turnID != "turn-1" {
			t.Fatalf("turnID=%q", turnID)
		}
		assertGolden(t, "turn-start.json", peer.nextRequest(t))
	})

	t.Run("turn steer", func(t *testing.T) {
		rpc, peer := newRPCPeer(t, func(map[string]json.RawMessage) json.RawMessage {
			return json.RawMessage(`{"turnId":"turn-1"}`)
		})
		adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
		if err := adapter.SteerTurn(context.Background(), "thread-1", "turn-1", "inspect the race"); err != nil {
			t.Fatal(err)
		}
		assertGolden(t, "turn-steer.json", peer.nextRequest(t))
	})

	t.Run("turn interrupt", func(t *testing.T) {
		rpc, peer := newRPCPeer(t, func(map[string]json.RawMessage) json.RawMessage { return json.RawMessage(`{}`) })
		adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
		if err := adapter.InterruptTurn(context.Background(), "thread-1", "turn-1"); err != nil {
			t.Fatal(err)
		}
		assertGolden(t, "turn-interrupt.json", peer.nextRequest(t))
	})
}

func TestAdapterRejectsInvalidConfigurationAndCompatibility(t *testing.T) {
	rpc := NewRPCClient(bytes.NewReader(nil), io.Discard)
	if _, err := NewAdapter(rpc, AdapterConfig{ProjectRoot: "relative"}); !errors.Is(err, ErrProjectRootNotAbsolute) {
		t.Fatalf("err=%v", err)
	}
	valid := "agent_romm/0.151.0-alpha.7.2 extra"
	cases := []string{
		"", "codex/0.151.0-alpha.7.2", "xagent_romm/0.151.0-alpha.7.2",
		"agent_romm/0.151.0-alpha.7.20", "agent_romm/0.151.0-alpha.7.2x",
		"agent_romm/0.151.0-alpha.7.2/extra",
	}
	if err := ValidateUserAgent(SupportedCLIOutput, valid); err != nil {
		t.Fatalf("valid user agent rejected: %v", err)
	}
	for _, userAgent := range cases {
		t.Run(userAgent, func(t *testing.T) {
			if err := ValidateUserAgent(SupportedCLIOutput, userAgent); !errors.Is(err, ErrIncompatibleUserAgent) {
				t.Fatalf("userAgent=%q err=%v", userAgent, err)
			}
		})
	}
}

func TestInitializeAuthenticationRules(t *testing.T) {
	for _, tc := range []struct {
		name     string
		account  string
		requires bool
		wantErr  error
	}{
		{"required and missing", "null", true, ErrUnauthenticated},
		{"not required and missing", "null", false, nil},
		{"required and present", `{"type":"apiKey"}`, true, nil},
		{"invalid account shape", `42`, false, ErrInvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rpc, _ := newRPCPeer(t, func(request map[string]json.RawMessage) json.RawMessage {
				if methodOf(request) == "initialize" {
					return json.RawMessage(`{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"linux","userAgent":"agent_romm/0.151.0-alpha.7.2"}`)
				}
				return json.RawMessage(`{"account":` + tc.account + `,"requiresOpenaiAuth":` + map[bool]string{true: "true", false: "false"}[tc.requires] + `}`)
			})
			adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
			_, err := adapter.Initialize(context.Background(), SupportedCLIOutput)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err=%v want=%v", err, tc.wantErr)
			}
		})
	}
}

func TestThreadPolicyAndBindingValidation(t *testing.T) {
	tests := []struct {
		name   string
		method string
		result json.RawMessage
		want   error
	}{
		{"start missing cwd", "start", json.RawMessage(`{"thread":{"id":"thread-1","cwd":"/srv/project","turns":[]},"approvalPolicy":"never","sandbox":{"type":"dangerFullAccess"}}`), ErrRuntimePolicyMismatch},
		{"start wrong approval", "start", json.RawMessage(`{"thread":{"id":"thread-1","cwd":"/srv/project","turns":[]},"cwd":"/srv/project","approvalPolicy":"on-request","sandbox":{"type":"dangerFullAccess"}}`), ErrRuntimePolicyMismatch},
		{"resume wrong policy casing", "resume", json.RawMessage(`{"thread":{"id":"thread-1","cwd":"/srv/project","turns":[]},"cwd":"/srv/project","approvalPolicy":"never","sandbox":{"type":"danger-full-access"}}`), ErrRuntimePolicyMismatch},
		{"read wrong cwd", "read", json.RawMessage(`{"thread":{"id":"thread-1","cwd":"/wrong","turns":[]}}`), ErrThreadBindingMismatch},
		{"resume wrong thread cwd", "resume", json.RawMessage(`{"thread":{"id":"thread-1","cwd":"/wrong","turns":[]},"cwd":"/srv/project","approvalPolicy":"never","sandbox":{"type":"dangerFullAccess"}}`), ErrThreadBindingMismatch},
		{"read wrong id", "read", json.RawMessage(`{"thread":{"id":"other","cwd":"/srv/project","turns":[]}}`), ErrThreadBindingMismatch},
		{"read null turns", "read", json.RawMessage(`{"thread":{"id":"thread-1","cwd":"/srv/project","turns":null}}`), ErrInvalidResponse},
		{"read turn null items", "read", json.RawMessage(`{"thread":{"id":"thread-1","cwd":"/srv/project","turns":[{"id":"turn-1","status":"completed","items":null}]}}`), ErrInvalidResponse},
		{"read turn object items", "read", json.RawMessage(`{"thread":{"id":"thread-1","cwd":"/srv/project","turns":[{"id":"turn-1","status":"completed","items":{}}]}}`), ErrInvalidResponse},
		{"read turn scalar items", "read", json.RawMessage(`{"thread":{"id":"thread-1","cwd":"/srv/project","turns":[{"id":"turn-1","status":"completed","items":42}]}}`), ErrInvalidResponse},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rpc, _ := newRPCPeer(t, func(map[string]json.RawMessage) json.RawMessage { return tc.result })
			adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
			var err error
			switch tc.method {
			case "start":
				_, err = adapter.StartThread(context.Background())
			case "read":
				_, err = adapter.ReadThread(context.Background(), "thread-1")
			case "resume":
				_, err = adapter.ResumeThread(context.Background(), "thread-1")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
		})
	}
}

func TestSnapshotRejectsInvalidThreadTurnAndItemIDs(t *testing.T) {
	adapter := &Adapter{projectRoot: "/srv/project"}
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"thread control", `{"id":"bad\u0000id","cwd":"/srv/project","turns":[]}`},
		{"thread bidi override", `{"id":"bad\u202eid","cwd":"/srv/project","turns":[]}`},
		{"turn zero width only", `{"id":"thread-1","cwd":"/srv/project","turns":[{"id":"\u200b","status":"completed","items":[]}]}`},
		{"item embedded zero width", `{"id":"thread-1","cwd":"/srv/project","turns":[{"id":"turn-1","status":"completed","items":[{"type":"agentMessage","id":"bad\u200bid"}]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adapter.snapshot(json.RawMessage(tc.raw), "")
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	valid, err := adapter.snapshot(json.RawMessage(`{"id":"线程-一","cwd":"/srv/project","turns":[{"id":"轮次-一","status":"completed","items":[{"type":"agentMessage","id":"项目-一"}]}]}`), "")
	if err != nil || valid.ID != "线程-一" || len(valid.Turns) != 1 || len(valid.Turns[0].Items) != 1 || valid.Turns[0].ID != "轮次-一" || valid.Turns[0].Items[0].ItemID != "项目-一" {
		t.Fatalf("valid non-ASCII snapshot=%#v err=%v", valid, err)
	}
}

func TestMutationResponsesRejectInvalidIDsWithUnknownDelivery(t *testing.T) {
	badID, err := json.Marshal("bad\u202eid")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		result json.RawMessage
		call   func(*Adapter) error
	}{
		{
			name:   "thread start",
			result: json.RawMessage(`{"thread":{"id":` + string(badID) + `,"cwd":"/srv/project","turns":[]},"cwd":"/srv/project","approvalPolicy":"never","sandbox":{"type":"dangerFullAccess"}}`),
			call:   func(a *Adapter) error { _, err := a.StartThread(context.Background()); return err },
		},
		{
			name:   "turn start",
			result: json.RawMessage(`{"turn":{"id":` + string(badID) + `,"status":"inProgress","items":[]}}`),
			call: func(a *Adapter) error {
				_, err := a.StartTurn(context.Background(), "thread-1", testClientMessageID, "run")
				return err
			},
		},
		{
			name:   "turn steer",
			result: json.RawMessage(`{"turnId":` + string(badID) + `}`),
			call: func(a *Adapter) error {
				return a.SteerTurn(context.Background(), "thread-1", "turn-1", "continue")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rpc, _ := newRPCPeer(t, func(map[string]json.RawMessage) json.RawMessage { return tc.result })
			adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
			err := tc.call(adapter)
			var mutation *room.MutationError
			if !errors.As(err, &mutation) || mutation.Certainty != room.DeliveryUnknown || !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestMappedNotificationsRejectInvalidProtocolIDs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		params string
	}{
		{"delta thread", "item/agentMessage/delta", `{"threadId":"bad\u0000","turnId":"turn-1","itemId":"item-1","delta":"x"}`},
		{"delta turn", "item/agentMessage/delta", `{"threadId":"thread-1","turnId":"bad\u0000","itemId":"item-1","delta":"x"}`},
		{"delta item", "item/agentMessage/delta", `{"threadId":"thread-1","turnId":"turn-1","itemId":"bad\u0000","delta":"x"}`},
		{"item started thread", "item/started", `{"threadId":"bad\u0000","turnId":"turn-1","startedAtMs":1,"item":{"type":"agentMessage","id":"item-1"}}`},
		{"item completed turn", "item/completed", `{"threadId":"thread-1","turnId":"bad\u0000","completedAtMs":1,"item":{"type":"agentMessage","id":"item-1"}}`},
		{"item nested ID", "item/completed", `{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"agentMessage","id":"bad\u0000"}}`},
		{"turn outer thread", "turn/completed", `{"threadId":"bad\u0000","turn":{"id":"turn-1","status":"completed","items":[]}}`},
		{"turn nested ID", "turn/completed", `{"threadId":"thread-1","turn":{"id":"bad\u0000","status":"completed","items":[]}}`},
		{"error thread", "error", `{"threadId":"bad\u0000","turnId":"turn-1","willRetry":false,"error":{"message":"failed"}}`},
		{"error turn", "error", `{"threadId":"thread-1","turnId":"bad\u0000","willRetry":false,"error":{"message":"failed"}}`},
		{"delta bidi override", "item/agentMessage/delta", `{"threadId":"bad\u202eid","turnId":"turn-1","itemId":"item-1","delta":"x"}`},
		{"delta zero width only", "item/agentMessage/delta", `{"threadId":"\u200b","turnId":"turn-1","itemId":"item-1","delta":"x"}`},
		{"delta embedded zero width", "item/agentMessage/delta", `{"threadId":"thread-1","turnId":"turn-1","itemId":"bad\u200bid","delta":"x"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event, ok := mapNotification(Notification{Method: tc.method, Params: json.RawMessage(tc.params)})
			if !ok || event.Kind != "protocol-error" || !errors.Is(event.Error, ErrInvalidResponse) {
				t.Fatalf("event=%#v ok=%v", event, ok)
			}
		})
	}
}

func TestPeerOriginatedAngleBracketIDIsRejected(t *testing.T) {
	peerID := strings.Repeat("<", 4097)
	rpc, _ := newRPCPeer(t, func(map[string]json.RawMessage) json.RawMessage {
		return policyThreadResponse(peerID, "/srv/project", `[]`)
	})
	adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
	_, err := adapter.StartThread(context.Background())
	var mutation *room.MutationError
	if !errors.As(err, &mutation) || mutation.Certainty != room.DeliveryUnknown || !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("err=%v", err)
	}
}

func TestStartTurnRejectsNonArrayItemsWithUnknownDelivery(t *testing.T) {
	for _, items := range []string{"null", `{}`, `42`} {
		t.Run(items, func(t *testing.T) {
			rpc, _ := newRPCPeer(t, func(map[string]json.RawMessage) json.RawMessage {
				return json.RawMessage(`{"turn":{"id":"turn-1","status":"inProgress","items":` + items + `}}`)
			})
			adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
			_, err := adapter.StartTurn(context.Background(), "thread-1", testClientMessageID, "run")
			var mutation *room.MutationError
			if !errors.As(err, &mutation) || mutation.Certainty != room.DeliveryUnknown || !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestDecodeProtocolTurnEnforcesItemCap(t *testing.T) {
	for _, tc := range []struct {
		name    string
		count   int
		wantErr bool
	}{
		{name: "at cap", count: 16384},
		{name: "over cap", count: 16385, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"id":"turn-1","status":"completed","items":` + jsonArrayOf("0", tc.count) + `}`)
			_, err := decodeProtocolTurn(raw)
			if tc.wantErr != errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("items=%d err=%v wantErr=%v", tc.count, err, tc.wantErr)
			}
		})
	}
}

func TestOverCapTurnIsUnknownForMutationAndProtocolErrorForNotification(t *testing.T) {
	items := jsonArrayOf("0", 16385)
	rpc, _ := newRPCPeer(t, func(map[string]json.RawMessage) json.RawMessage {
		return json.RawMessage(`{"turn":{"id":"turn-1","status":"inProgress","items":` + items + `}}`)
	})
	adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
	_, err := adapter.StartTurn(context.Background(), "thread-1", testClientMessageID, "run")
	var mutation *room.MutationError
	if !errors.As(err, &mutation) || mutation.Certainty != room.DeliveryUnknown || !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("StartTurn err=%v", err)
	}
	event, ok := mapNotification(Notification{
		Method: "turn/completed",
		Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":` + items + `}}`),
	})
	if !ok || event.Kind != "protocol-error" || !errors.Is(event.Error, ErrInvalidResponse) {
		t.Fatalf("event=%#v ok=%v", event, ok)
	}
}

func TestSnapshotEnforcesTurnAndItemCaps(t *testing.T) {
	adapter := &Adapter{projectRoot: "/srv/project"}
	emptyTurn := `{"id":"turn-1","status":"completed","items":[]}`
	item := `{"type":"agentMessage","id":"item-1"}`
	itemsAtCap := jsonArrayOf(item, 16384)
	turnAtItemCap := `{"id":"turn-1","status":"completed","items":` + itemsAtCap + `}`

	t.Run("turn count at cap", func(t *testing.T) {
		snapshot, err := adapter.snapshot(json.RawMessage(threadJSON("thread-1", "/srv/project", jsonArrayOf(emptyTurn, 4096))), "thread-1")
		if err != nil || len(snapshot.Turns) != 4096 {
			t.Fatalf("turns=%d err=%v", len(snapshot.Turns), err)
		}
	})
	t.Run("turn count over cap", func(t *testing.T) {
		_, err := adapter.snapshot(json.RawMessage(threadJSON("thread-1", "/srv/project", jsonArrayOf(emptyTurn, 4097))), "thread-1")
		if !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("items per turn at cap", func(t *testing.T) {
		snapshot, err := adapter.snapshot(json.RawMessage(threadJSON("thread-1", "/srv/project", `[`+turnAtItemCap+`]`)), "thread-1")
		if err != nil || len(snapshot.Turns) != 1 || len(snapshot.Turns[0].Items) != 16384 {
			t.Fatalf("snapshot turns=%d err=%v", len(snapshot.Turns), err)
		}
	})
	t.Run("items per turn over cap", func(t *testing.T) {
		items := jsonArrayOf(item, 16385)
		turn := `{"id":"turn-1","status":"completed","items":` + items + `}`
		_, err := adapter.snapshot(json.RawMessage(threadJSON("thread-1", "/srv/project", `[`+turn+`]`)), "thread-1")
		if !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("total items at cap", func(t *testing.T) {
		turns := jsonArrayOf(turnAtItemCap, 4)
		snapshot, err := adapter.snapshot(json.RawMessage(threadJSON("thread-1", "/srv/project", turns)), "thread-1")
		if err != nil || len(snapshot.Turns) != 4 {
			t.Fatalf("turns=%d err=%v", len(snapshot.Turns), err)
		}
		total := 0
		for _, turn := range snapshot.Turns {
			total += len(turn.Items)
		}
		if total != 65536 {
			t.Fatalf("total items=%d", total)
		}
	})
	t.Run("total items over cap", func(t *testing.T) {
		oneItemTurn := `{"id":"turn-2","status":"completed","items":[` + item + `]}`
		turns := `[` + strings.Repeat(turnAtItemCap+",", 4) + oneItemTurn + `]`
		_, err := adapter.snapshot(json.RawMessage(threadJSON("thread-1", "/srv/project", turns)), "thread-1")
		if !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestHostileTinyItemTailDoesNotAllocatePerElement(t *testing.T) {
	turnWithItems := func(count int) json.RawMessage {
		return json.RawMessage(`{"id":"turn-1","status":"completed","items":` + jsonArrayOf("0", count) + `}`)
	}
	allocated := func(raw json.RawMessage) (int64, error) {
		var decodeErr error
		result := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, decodeErr = decodeProtocolTurn(raw)
			}
		})
		return result.AllocedBytesPerOp(), decodeErr
	}
	smallRaw := turnWithItems(16385)
	largeRaw := turnWithItems(100000)
	smallBytes, smallErr := allocated(smallRaw)
	largeBytes, largeErr := allocated(largeRaw)
	if !errors.Is(smallErr, ErrInvalidResponse) || !errors.Is(largeErr, ErrInvalidResponse) {
		t.Errorf("small err=%v large err=%v", smallErr, largeErr)
	}
	// The generous four-input-byte allowance covers the one bounded RawMessage
	// copy. Retaining a RawMessage header per hostile tail element exceeds it.
	if allowance := smallBytes + int64(4*len(largeRaw)); largeBytes > allowance {
		t.Errorf("large allocation=%d small=%d allowance=%d", largeBytes, smallBytes, allowance)
	}
}

func TestStreamJSONArrayRejectsMalformedAndTrailingData(t *testing.T) {
	for _, raw := range []string{"null", `{}`, `[0,]`, `[0] true`, `[`, `[0`} {
		t.Run(raw, func(t *testing.T) {
			if _, err := streamJSONArray(json.RawMessage(raw), 10, nil); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("raw=%q err=%v", raw, err)
			}
		})
	}
	count, err := streamJSONArray(json.RawMessage("[0,1] \t\n"), 2, nil)
	if err != nil || count != 2 {
		t.Fatalf("valid array count=%d err=%v", count, err)
	}
}

func TestThreadSnapshotCopiesItemsAndMapsStatuses(t *testing.T) {
	result := json.RawMessage(`{"thread":{"id":"thread-1","cwd":"/srv/project","turns":[{"id":"turn-1","status":"completed","items":[{"type":"agentMessage","id":"item-1","text":"done"}]},{"id":"turn-2","status":"inProgress","items":[]},{"id":"turn-3","status":"interrupted","items":[]},{"id":"turn-4","status":"failed","items":[]}]}}`)
	rpc, _ := newRPCPeer(t, func(map[string]json.RawMessage) json.RawMessage { return result })
	adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
	snapshot, err := adapter.ReadThread(context.Background(), "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []room.RequestState{room.RequestCompleted, room.RequestRunning, room.RequestInterrupted, room.RequestFailed}
	for i, state := range want {
		if snapshot.Turns[i].State != state {
			t.Fatalf("turn %d state=%q want=%q", i, snapshot.Turns[i].State, state)
		}
	}
	if got := string(snapshot.Turns[0].Items[0].Payload); got != `{"type":"agentMessage","id":"item-1","text":"done"}` {
		t.Fatalf("payload=%s", got)
	}
	original := append([]byte(nil), snapshot.Turns[0].Items[0].Payload...)
	snapshot.Turns[0].Items[0].Payload[0] = '['
	if original[0] != '{' {
		t.Fatal("test fixture unexpectedly mutated")
	}
}

func TestAdapterMapsNotificationsAndReverseRequests(t *testing.T) {
	rpc, peer := newRPCPeer(t, nil)
	adapter, err := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
	if err != nil {
		t.Fatal(err)
	}
	inputs := []string{
		`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","delta":"part"},"emittedAtMs":1}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":2,"item":{"type":"agentMessage","id":"item-1","text":"final"}}}`,
		`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[]}}}`,
		`{"id":"approval-1","method":"item/commandExecution/requestApproval","params":{},"trace":null}`,
	}
	for _, input := range inputs {
		if _, err := io.WriteString(peer.writer, input+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	wantKinds := []string{"item-delta", "item-completed", "turn-completed", "unsupported-server-request"}
	for _, kind := range wantKinds {
		select {
		case event := <-adapter.Events():
			if event.Kind != kind {
				t.Fatalf("kind=%q want=%q", event.Kind, kind)
			}
			if kind == "item-completed" {
				if event.Completed == nil || string(event.Completed.Payload) != `{"type":"agentMessage","id":"item-1","text":"final"}` {
					t.Fatalf("completed=%#v", event.Completed)
				}
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", kind)
		}
	}
	response := peer.nextRequest(t)
	if string(response) != `{"id":"approval-1","result":{"decision":"cancel"}}` {
		t.Fatalf("reverse response=%s", response)
	}
}

func TestAdapterEventPipelineBackpressuresWithoutDropping(t *testing.T) {
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	rpc := NewRPCClient(clientReads, clientWrites)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- rpc.Run(ctx) }()
	adapter, err := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
	if err != nil {
		t.Fatal(err)
	}

	const eventCount = 8
	writeDone := make(chan error, 1)
	go func() {
		for i := 0; i < eventCount; i++ {
			line := fmt.Sprintf(`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","delta":"%d"}}`+"\n", i)
			if _, err := io.WriteString(serverWrites, line); err != nil {
				writeDone <- err
				return
			}
		}
		writeDone <- nil
	}()

	select {
	case err := <-writeDone:
		t.Fatalf("producer was not backpressured before Events was consumed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	for i := 0; i < eventCount; i++ {
		select {
		case event := <-adapter.Events():
			if event.Kind != "item-delta" || event.Delta != strconv.Itoa(i) {
				t.Fatalf("event %d=%#v", i, event)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = serverWrites.Close()
	_ = serverReads.Close()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("Run did not converge")
	}
}

func TestRPCBusyReverseRequestBypassesFullAdapterEventPipeline(t *testing.T) {
	serverReads, serverWrites := io.Pipe()
	writer := newCloseBlockingWriter()
	rpc := NewRPCClient(serverReads, writer)
	adapter, err := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- rpc.Run(ctx) }()

	if _, err := io.WriteString(serverWrites, `{"id":1,"method":"unknown"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	<-writer.entered
	deadline := time.Now().Add(time.Second)
	for len(adapter.events) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(adapter.events) != 1 {
		t.Fatal("first reverse observation did not reach the mapped queue")
	}

	notification := Notification{
		Method: "item/agentMessage/delta",
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","delta":"blocked"}`),
	}
	rpc.events <- notification
	rpc.events <- notification
	if len(rpc.events) != 1 {
		t.Fatal("raw event queue was not saturated")
	}

	callDone := make(chan error, 1)
	go func() { callDone <- rpc.Call(context.Background(), "call", nil, nil) }()
	waitForPendingCount(t, rpc, 1)
	if _, err := io.WriteString(serverWrites, `{"id":2,"method":"unknown"}`+"\n"); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-runDone:
		if !errors.Is(err, ErrServerRequestBusy) {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(100 * time.Millisecond):
		cancel()
		_ = writer.Close()
		_ = serverWrites.Close()
		<-runDone
		<-callDone
		t.Fatal("busy reverse request blocked behind the full event pipeline")
	}
	_ = serverWrites.Close()
	callErr := <-callDone
	var typed *CallError
	if !errors.As(callErr, &typed) || typed.BytesWritten != 0 || !errors.Is(callErr, ErrServerRequestBusy) {
		t.Fatalf("pending call err=%v", callErr)
	}
	if writer.closeCount() != 1 {
		t.Fatalf("writer closed %d times", writer.closeCount())
	}
	select {
	case <-rpc.Done():
	default:
		t.Fatal("Done was not closed")
	}
	first, ok := <-adapter.Events()
	if !ok || first.Kind != "unsupported-server-request" {
		t.Fatalf("first event=%#v ok=%v", first, ok)
	}
	// Transport failure must not discard the two already accepted deltas;
	// the overloaded second reverse request itself was never admitted.
	for i := 0; i < 2; i++ {
		if event, ok := <-adapter.Events(); !ok || event.Kind != "item-delta" || event.Delta != "blocked" {
			t.Fatalf("accepted delta %d lost: %#v", i, event)
		}
	}
	if extra, ok := <-adapter.Events(); ok {
		t.Fatalf("busy reverse request unexpectedly published %#v", extra)
	}
}

func TestInitializeDrainsFilteredNotificationsWithBoundedQueues(t *testing.T) {
	var peer *rpcTestPeer
	rpc, createdPeer := newRPCPeer(t, func(request map[string]json.RawMessage) json.RawMessage {
		switch methodOf(request) {
		case "initialize":
			for i := 0; i < 16; i++ {
				if _, err := io.WriteString(peer.writer, `{"method":"thread/status/changed","params":{}}`+"\n"); err != nil {
					t.Errorf("write filtered notification: %v", err)
				}
			}
			return json.RawMessage(`{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"linux","userAgent":"agent_romm/0.151.0-alpha.7.2"}`)
		case "account/read":
			return json.RawMessage(`{"account":{"type":"apiKey"},"requiresOpenaiAuth":true}`)
		default:
			return json.RawMessage(`{}`)
		}
	})
	peer = createdPeer
	adapter, err := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := adapter.Initialize(ctx, SupportedCLIOutput); err != nil {
		t.Fatal(err)
	}
}

func TestEveryReverseRequestEmitsUnsupportedAgentEvent(t *testing.T) {
	methods := []string{
		"item/commandExecution/requestApproval", "item/fileChange/requestApproval",
		"item/tool/requestUserInput", "item/permissions/requestApproval",
		"mcpServer/elicitation/request", "item/tool/call", "unknown/method",
	}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			rpc, peer := newRPCPeer(t, nil)
			adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
			if _, err := io.WriteString(peer.writer, `{"id":"request-1","method":"`+method+`","params":{}}`+"\n"); err != nil {
				t.Fatal(err)
			}
			select {
			case event := <-adapter.Events():
				if event.Kind != "unsupported-server-request" {
					t.Fatalf("event=%#v", event)
				}
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for unsupported event")
			}
			_ = peer.nextRequest(t)
		})
	}
}

func TestAdapterRejectsMalformedPinnedNotifications(t *testing.T) {
	tests := []string{
		`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1"}}`,
		`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","item":{"type":"agentMessage","id":"item-1","text":"x"}}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"type":"agentMessage","id":"item-1","text":"x"}}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"id":"item-1"}}}`,
		`{"method":"error","params":{"threadId":"thread-1","turnId":"turn-1","willRetry":false}}`,
		`{"method":"error","params":{"threadId":"thread-1","turnId":"turn-1","willRetry":false,"error":{}}}`,
		`{"method":"turn/started","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[]}}}`,
		`{"method":"item/reasoning/summaryTextDelta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","delta":"x"}}`,
		`{"method":"item/reasoning/summaryTextDelta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","delta":"x","summaryIndex":"0"}}`,
		`{"method":"item/reasoning/textDelta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","delta":"x"}}`,
		`{"method":"item/reasoning/textDelta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","delta":"x","contentIndex":1.5}}`,
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			rpc, peer := newRPCPeer(t, nil)
			adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
			if _, err := io.WriteString(peer.writer, input+"\n"); err != nil {
				t.Fatal(err)
			}
			select {
			case event := <-adapter.Events():
				if event.Kind != "protocol-error" || !errors.Is(event.Error, ErrInvalidResponse) {
					t.Fatalf("event=%#v", event)
				}
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for protocol error")
			}
		})
	}
}

func TestTurnNotificationsRejectNonArrayItems(t *testing.T) {
	for _, items := range []string{"null", `{}`, `42`} {
		t.Run(items, func(t *testing.T) {
			event, ok := mapNotification(Notification{
				Method: "turn/completed",
				Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":` + items + `}}`),
			})
			if !ok || event.Kind != "protocol-error" || !errors.Is(event.Error, ErrInvalidResponse) {
				t.Fatalf("event=%#v ok=%v", event, ok)
			}
		})
	}
}

func TestReasoningDeltaAcceptsAnyInt64Index(t *testing.T) {
	for _, tc := range []struct {
		method string
		field  string
	}{
		{"item/reasoning/summaryTextDelta", `"summaryIndex":-1`},
		{"item/reasoning/textDelta", `"contentIndex":9223372036854775807`},
	} {
		event, ok := mapNotification(Notification{
			Method: tc.method,
			Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","delta":"x",` + tc.field + `}`),
		})
		if !ok || event.Kind != "item-delta" || event.Delta != "x" {
			t.Fatalf("method=%s event=%#v ok=%v", tc.method, event, ok)
		}
	}
}

func TestStableAdapterErrorClassifiesInvalidWireIDsAsInvalidResponses(t *testing.T) {
	for _, err := range []error{ErrInvalidWireID, &CallError{Err: ErrInvalidWireID}} {
		if got := stableAdapterError(err); !errors.Is(got, ErrInvalidResponse) {
			t.Fatalf("stableAdapterError(%v)=%v", err, got)
		}
	}
}

func TestMutationErrorKeepsZeroByteProtocolFailuresUnknown(t *testing.T) {
	for _, protocolErr := range []error{ErrUnknownResponseID, ErrInvalidWireID, ErrInvalidWireMessage, ErrResultDecode, io.EOF} {
		err := mutationError("turn/start", &CallError{BytesWritten: 0, Err: protocolErr})
		var mutation *room.MutationError
		if !errors.As(err, &mutation) || mutation.Certainty != room.DeliveryUnknown {
			t.Fatalf("protocolErr=%v mutation=%#v", protocolErr, mutation)
		}
	}
}

type mutationOperation struct {
	name string
	call func(context.Context, *Adapter) error
}

func mutationOperations() []mutationOperation {
	return []mutationOperation{
		{"thread/start", func(ctx context.Context, a *Adapter) error { _, err := a.StartThread(ctx); return err }},
		{"turn/start", func(ctx context.Context, a *Adapter) error {
			_, err := a.StartTurn(ctx, "thread-1", testClientMessageID, "run")
			return err
		}},
		{"turn/steer", func(ctx context.Context, a *Adapter) error { return a.SteerTurn(ctx, "thread-1", "turn-1", "more") }},
		{"turn/interrupt", func(ctx context.Context, a *Adapter) error { return a.InterruptTurn(ctx, "thread-1", "turn-1") }},
	}
}

type partialBlockingWriter struct {
	once    sync.Once
	wrote   chan struct{}
	release chan struct{}
}

type responseAfterWriteTransport struct {
	ready    chan struct{}
	once     sync.Once
	response *bytes.Reader
	mu       sync.Mutex
	written  bytes.Buffer
}

func newResponseAfterWriteTransport(response string) *responseAfterWriteTransport {
	return &responseAfterWriteTransport{
		ready: make(chan struct{}), response: bytes.NewReader([]byte(response)),
	}
}

func (t *responseAfterWriteTransport) Read(p []byte) (int, error) {
	<-t.ready
	return t.response.Read(p)
}

func (t *responseAfterWriteTransport) Write(p []byte) (int, error) {
	t.mu.Lock()
	n, err := t.written.Write(p)
	complete := bytes.Contains(t.written.Bytes(), []byte{'\n'})
	t.mu.Unlock()
	if complete {
		t.once.Do(func() { close(t.ready) })
	}
	return n, err
}

func (w *partialBlockingWriter) Write(p []byte) (int, error) {
	n := 0
	w.once.Do(func() {
		n = 1
		close(w.wrote)
		<-w.release
	})
	if n == 1 {
		return 1, nil
	}
	return 0, errors.New("unexpected second write")
}

func TestEveryMutationDistinguishesNotSentFromUnknown(t *testing.T) {
	for _, operation := range mutationOperations() {
		t.Run(operation.name+" canceled before write", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			rpc := NewRPCClient(bytes.NewReader(nil), io.Discard)
			adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
			err := operation.call(ctx, adapter)
			var mutation *room.MutationError
			if !errors.As(err, &mutation) || mutation.Certainty != room.DeliveryNotSent {
				t.Fatalf("err=%v", err)
			}
		})

		t.Run(operation.name+" partial write", func(t *testing.T) {
			writer := &partialBlockingWriter{wrote: make(chan struct{}), release: make(chan struct{})}
			rpc := NewRPCClient(bytes.NewReader(nil), writer)
			adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() { result <- operation.call(ctx, adapter) }()
			<-writer.wrote
			cancel()
			close(writer.release)
			err := <-result
			var mutation *room.MutationError
			if !errors.As(err, &mutation) || mutation.Certainty != room.DeliveryUnknown {
				t.Fatalf("err=%v", err)
			}
		})

		t.Run(operation.name+" zero write", func(t *testing.T) {
			rpc := NewRPCClient(bytes.NewReader(nil), &shortWriter{zero: true})
			adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
			err := operation.call(context.Background(), adapter)
			var mutation *room.MutationError
			if !errors.As(err, &mutation) || mutation.Certainty != room.DeliveryNotSent {
				t.Fatalf("err=%v", err)
			}
		})

		for _, scenario := range []struct {
			name           string
			response       string
			wantTextAbsent []string
		}{
			{"rpc error", `{"id":1,"error":{"code":-32000,"message":"private rpc detail","data":{"secret":"token"}}}` + "\n", []string{"private rpc detail", "secret", "token"}},
			{"result decode", `{"id":1,"result":"wrong shape"}` + "\n", nil},
			{"unknown response id", `{"id":99,"result":{}}` + "\n", nil},
			{"eof", "", nil},
		} {
			t.Run(operation.name+" "+scenario.name, func(t *testing.T) {
				transport := newResponseAfterWriteTransport(scenario.response)
				rpc := NewRPCClient(transport, transport)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				runDone := make(chan error, 1)
				go func() { runDone <- rpc.Run(ctx) }()
				adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
				err := operation.call(context.Background(), adapter)
				var mutation *room.MutationError
				if !errors.As(err, &mutation) || mutation.Certainty != room.DeliveryUnknown {
					t.Fatalf("err=%v", err)
				}
				for _, forbidden := range scenario.wantTextAbsent {
					if strings.Contains(err.Error(), forbidden) {
						t.Fatalf("error exposed %q: %v", forbidden, err)
					}
				}
				<-runDone
			})
		}

		t.Run(operation.name+" response timeout", func(t *testing.T) {
			rpc := NewRPCClient(bytes.NewReader(nil), io.Discard)
			adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			defer cancel()
			err := operation.call(ctx, adapter)
			var mutation *room.MutationError
			if !errors.As(err, &mutation) || mutation.Certainty != room.DeliveryUnknown {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestTextMutationsPreflightEscapedSizeBeforeRPCRegistration(t *testing.T) {
	text := strings.Repeat("<", MaxJSONLLineBytes/6+1)
	for _, tc := range []struct {
		name string
		call func(*Adapter) error
	}{
		{"turn/start", func(adapter *Adapter) error {
			_, err := adapter.StartTurn(context.Background(), "thread-1", testClientMessageID, text)
			return err
		}},
		{"turn/steer", func(adapter *Adapter) error {
			return adapter.SteerTurn(context.Background(), "thread-1", "turn-1", text)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rpc := NewRPCClient(bytes.NewReader(nil), io.Discard)
			adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
			err := tc.call(adapter)
			var mutation *room.MutationError
			if !errors.As(err, &mutation) || mutation.Certainty != room.DeliveryNotSent || !errors.Is(err, ErrRPCMessageTooLarge) {
				t.Fatalf("err=%v", err)
			}
			rpc.stateMu.Lock()
			nextID := rpc.nextID
			rpc.stateMu.Unlock()
			if nextID != 0 {
				t.Fatalf("RPC registered request ID %d before rejecting obvious oversize", nextID)
			}
		})
	}
}

func TestAdapterRejectsInvalidIDsBeforeRPCRegistration(t *testing.T) {
	invalidIDs := []struct {
		name string
		id   string
	}{
		{"blank", " \t\u2003"},
		{"over 4096 bytes", strings.Repeat("a", 4097)},
		{"control", "bad\x00id"},
		{"bidi override", "bad\u202eid"},
		{"zero width only", "\u200b"},
		{"embedded zero width", "bad\u200bid"},
		{"invalid UTF-8", string([]byte{'b', 0xff, 'd'})},
	}
	assertRejected := func(t *testing.T, call func(*Adapter) error, mutationExpected bool) {
		t.Helper()
		writer := &rejectingCountingCloser{}
		rpc := NewRPCClient(bytes.NewReader(nil), writer)
		adapter := &Adapter{rpc: rpc, projectRoot: "/srv/project"}
		err := call(adapter)
		if !errors.Is(err, ErrInvalidAgentRequest) {
			t.Fatalf("err=%v", err)
		}
		if mutationExpected {
			var mutation *room.MutationError
			if !errors.As(err, &mutation) || mutation.Certainty != room.DeliveryNotSent {
				t.Fatalf("mutation err=%v", err)
			}
		}
		rpc.stateMu.Lock()
		nextID := rpc.nextID
		rpc.stateMu.Unlock()
		writes, bytes, _ := writer.counts()
		if nextID != 0 || writes != 0 || bytes != 0 {
			t.Fatalf("nextID=%d writes=%d bytes=%d", nextID, writes, bytes)
		}
	}
	for _, invalid := range invalidIDs {
		t.Run("read "+invalid.name, func(t *testing.T) {
			assertRejected(t, func(a *Adapter) error {
				_, err := a.ReadThread(context.Background(), room.ThreadID(invalid.id))
				return err
			}, false)
		})
	}
	bad := room.ThreadID("bad\u202eid")
	badTurn := room.TurnID("bad\u200bid")
	for _, tc := range []struct {
		name     string
		mutation bool
		call     func(*Adapter) error
	}{
		{"resume thread ID", false, func(a *Adapter) error { _, err := a.ResumeThread(context.Background(), bad); return err }},
		{"start turn thread ID", true, func(a *Adapter) error {
			_, err := a.StartTurn(context.Background(), bad, testClientMessageID, "run")
			return err
		}},
		{"steer thread ID", true, func(a *Adapter) error { return a.SteerTurn(context.Background(), bad, "turn-1", "run") }},
		{"steer turn ID", true, func(a *Adapter) error { return a.SteerTurn(context.Background(), "thread-1", badTurn, "run") }},
		{"interrupt thread ID", true, func(a *Adapter) error { return a.InterruptTurn(context.Background(), bad, "turn-1") }},
		{"interrupt turn ID", true, func(a *Adapter) error { return a.InterruptTurn(context.Background(), "thread-1", badTurn) }},
	} {
		t.Run(tc.name, func(t *testing.T) { assertRejected(t, tc.call, tc.mutation) })
	}
}

func TestAdapterAllows4096ByteIDsToReachRPC(t *testing.T) {
	valid := strings.Repeat("<", 4096)
	for _, tc := range []struct {
		name string
		call func(*Adapter) error
	}{
		{"thread read", func(a *Adapter) error { _, err := a.ReadThread(context.Background(), room.ThreadID(valid)); return err }},
		{"thread resume", func(a *Adapter) error {
			_, err := a.ResumeThread(context.Background(), room.ThreadID(valid))
			return err
		}},
		{"turn start", func(a *Adapter) error {
			_, err := a.StartTurn(context.Background(), room.ThreadID(valid), testClientMessageID, "run")
			return err
		}},
		{"turn steer", func(a *Adapter) error {
			return a.SteerTurn(context.Background(), room.ThreadID(valid), room.TurnID(valid), "run")
		}},
		{"turn interrupt", func(a *Adapter) error {
			return a.InterruptTurn(context.Background(), room.ThreadID(valid), room.TurnID(valid))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer := &rejectingCountingCloser{}
			rpc := NewRPCClient(bytes.NewReader(nil), writer)
			adapter := &Adapter{rpc: rpc, projectRoot: "/srv/project"}
			_ = tc.call(adapter)
			rpc.stateMu.Lock()
			nextID := rpc.nextID
			rpc.stateMu.Unlock()
			writes, _, _ := writer.counts()
			if nextID != 1 || writes != 1 {
				t.Fatalf("nextID=%d writes=%d", nextID, writes)
			}
		})
	}
}

func TestAdapterAllowsNonASCIIProtocolIDToReachRPC(t *testing.T) {
	writer := &rejectingCountingCloser{}
	rpc := NewRPCClient(bytes.NewReader(nil), writer)
	adapter := &Adapter{rpc: rpc, projectRoot: "/srv/project"}
	_, _ = adapter.ReadThread(context.Background(), "线程-一")
	rpc.stateMu.Lock()
	nextID := rpc.nextID
	rpc.stateMu.Unlock()
	writes, _, _ := writer.counts()
	if nextID != 1 || writes != 1 {
		t.Fatalf("nextID=%d writes=%d", nextID, writes)
	}
}

func TestCWDPreflightRejectsBeforeRPCRegistration(t *testing.T) {
	hugeRoot := "/" + strings.Repeat("<", MaxJSONLLineBytes/6+1)
	for _, tc := range []struct {
		name     string
		mutation bool
		call     func(*Adapter) error
	}{
		{"thread start", true, func(a *Adapter) error { _, err := a.StartThread(context.Background()); return err }},
		{"thread resume", false, func(a *Adapter) error { _, err := a.ResumeThread(context.Background(), "thread-1"); return err }},
		{"turn start", true, func(a *Adapter) error {
			_, err := a.StartTurn(context.Background(), "thread-1", testClientMessageID, "run")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer := &rejectingCountingCloser{}
			rpc := NewRPCClient(bytes.NewReader(nil), writer)
			adapter := &Adapter{rpc: rpc, projectRoot: hugeRoot}
			err := tc.call(adapter)
			if !errors.Is(err, ErrRPCMessageTooLarge) {
				t.Fatalf("err=%v", err)
			}
			if tc.mutation {
				var mutation *room.MutationError
				if !errors.As(err, &mutation) || mutation.Certainty != room.DeliveryNotSent {
					t.Fatalf("mutation err=%v", err)
				}
			}
			rpc.stateMu.Lock()
			nextID := rpc.nextID
			rpc.stateMu.Unlock()
			writes, bytes, _ := writer.counts()
			if nextID != 0 || writes != 0 || bytes != 0 {
				t.Fatalf("nextID=%d writes=%d bytes=%d", nextID, writes, bytes)
			}
		})
	}
}
