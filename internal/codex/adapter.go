package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"agent_romm/internal/room"
)

var (
	ErrProjectRootNotAbsolute = errors.New("Codex project root must be absolute")
	ErrNilRPCClient           = errors.New("Codex RPC client is required")
	ErrUnauthenticated        = errors.New("Codex App Server requires OpenAI authentication")
	ErrRuntimePolicyMismatch  = errors.New("Codex App Server runtime policy mismatch")
	ErrThreadBindingMismatch  = errors.New("Codex thread project binding mismatch")
	ErrInvalidResponse        = errors.New("invalid Codex App Server response")
	ErrInvalidAgentRequest    = errors.New("invalid Codex agent request")
	ErrRPCRequestFailed       = errors.New("Codex App Server rejected the request")
	ErrRPCTransport           = errors.New("Codex App Server transport failure")
	ErrTurnFailed             = errors.New("Codex turn failed")
)

const (
	// One queued mapped event applies byte backpressure after one legal wire
	// record while still allowing filtered startup notifications to drain.
	adapterEventQueueCapacity = 1
	maxSnapshotTurns          = 4096
	maxItemsPerTurn           = 16384
	maxSnapshotItems          = 65536
	maxProtocolIDBytes        = 4096
)

type AdapterConfig struct {
	ProjectRoot string
}

type Adapter struct {
	rpc         *RPCClient
	projectRoot string
	events      chan room.AgentEvent
	ownerDone   chan struct{}
	forwardDone chan struct{}
	closeOnce   sync.Once
}

var _ room.Agent = (*Adapter)(nil)

func NewAdapter(rpc *RPCClient, cfg AdapterConfig) (*Adapter, error) {
	if rpc == nil {
		return nil, ErrNilRPCClient
	}
	if !filepath.IsAbs(cfg.ProjectRoot) {
		return nil, ErrProjectRootNotAbsolute
	}
	adapter := &Adapter{
		rpc: rpc, projectRoot: filepath.Clean(cfg.ProjectRoot), events: make(chan room.AgentEvent, adapterEventQueueCapacity),
		ownerDone: make(chan struct{}), forwardDone: make(chan struct{}),
	}
	go adapter.forwardNotifications()
	return adapter, nil
}

func (a *Adapter) Events() <-chan room.AgentEvent { return a.events }

// Close is consumer cancellation, distinct from transport EOF. The session
// owner calls it on failed startup/shutdown to unblock a backpressured forwarder.
func (a *Adapter) Close() {
	if a.ownerDone == nil {
		return
	}
	a.closeOnce.Do(func() { close(a.ownerDone) })
	<-a.forwardDone
}

func (a *Adapter) Initialize(ctx context.Context, cliOutput string) (InitializeResult, error) {
	version, err := AcceptedCodexVersion(cliOutput)
	if err != nil {
		return InitializeResult{}, err
	}
	var response initializeResponse
	err = a.rpc.Call(ctx, "initialize", initializeParams{ClientInfo: clientInfo{Name: "agent_romm", Version: version}}, &response)
	if err != nil {
		return InitializeResult{}, stableAdapterError(err)
	}
	if response.CodexHome == "" || response.PlatformFamily == "" || response.PlatformOS == "" || response.UserAgent == "" {
		return InitializeResult{}, ErrInvalidResponse
	}
	if err := ValidateUserAgent(cliOutput, response.UserAgent); err != nil {
		return InitializeResult{}, err
	}
	if err := a.rpc.Notify(ctx, "initialized", nil); err != nil {
		return InitializeResult{}, stableAdapterError(err)
	}
	var account accountReadResponse
	if err := a.rpc.Call(ctx, "account/read", accountReadParams{RefreshToken: false}, &account); err != nil {
		return InitializeResult{}, stableAdapterError(err)
	}
	if account.RequiresOpenAIAuth == nil {
		return InitializeResult{}, ErrInvalidResponse
	}
	present := len(account.Account) != 0 && !bytes.Equal(bytes.TrimSpace(account.Account), []byte("null"))
	if present && !validAccount(account.Account) {
		return InitializeResult{}, ErrInvalidResponse
	}
	if *account.RequiresOpenAIAuth && !present {
		return InitializeResult{}, ErrUnauthenticated
	}
	return InitializeResult{
		UserAgent: response.UserAgent, CodexHome: response.CodexHome,
		PlatformFamily: response.PlatformFamily, PlatformOS: response.PlatformOS,
		RequiresOpenAIAuth: *account.RequiresOpenAIAuth, AccountPresent: present,
	}, nil
}

func validAccount(raw json.RawMessage) bool {
	if !isJSONObject(raw) {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	var accountType string
	if !decodeJSONString(fields["type"], &accountType) {
		return false
	}
	switch accountType {
	case "apiKey":
		return true
	case "chatgpt":
		planType, hasPlan := fields["planType"]
		email, hasEmail := fields["email"]
		if !hasPlan || !hasEmail {
			return false
		}
		var plan string
		if !decodeJSONString(planType, &plan) {
			return false
		}
		if !validPlanType(plan) {
			return false
		}
		if !bytes.Equal(bytes.TrimSpace(email), []byte("null")) {
			var address string
			if !decodeJSONString(email, &address) {
				return false
			}
		}
		return true
	case "amazonBedrock":
		if managed, ok := fields["usesCodexManagedCredentials"]; ok {
			var value bool
			return json.Unmarshal(managed, &value) == nil && !bytes.Equal(bytes.TrimSpace(managed), []byte("null"))
		}
		return true
	default:
		return false
	}
}

func validPlanType(plan string) bool {
	switch plan {
	case "free", "go", "plus", "pro", "prolite", "team",
		"self_serve_business_prolite", "self_serve_business_usage_based", "business",
		"ent26", "enterprise_cbp_automation", "enterprise_cbp_usage_based",
		"enterprise", "edu", "edu_plus", "edu_pro", "unknown":
		return true
	default:
		return false
	}
}

func (a *Adapter) StartThread(ctx context.Context) (room.ThreadSnapshot, error) {
	if err := preflightStartThreadRequest(a.projectRoot); err != nil {
		return room.ThreadSnapshot{}, notSentMutation("thread/start", err)
	}
	var response policyThreadResult
	if err := a.rpc.Call(ctx, "thread/start", startThreadRequest(a.projectRoot), &response); err != nil {
		return room.ThreadSnapshot{}, mutationError("thread/start", err)
	}
	if err := a.validatePolicy(response); err != nil {
		return room.ThreadSnapshot{}, unknownMutation("thread/start", err)
	}
	snapshot, err := a.snapshot(response.Thread, "")
	if err != nil {
		return room.ThreadSnapshot{}, unknownMutation("thread/start", err)
	}
	return snapshot, nil
}

func (a *Adapter) ReadThread(ctx context.Context, threadID room.ThreadID) (room.ThreadSnapshot, error) {
	if !validProtocolID(string(threadID)) {
		return room.ThreadSnapshot{}, ErrInvalidAgentRequest
	}
	if err := preflightReadThreadRequest(string(threadID)); err != nil {
		return room.ThreadSnapshot{}, err
	}
	var response threadResult
	if err := a.rpc.Call(ctx, "thread/read", threadReadParams{ThreadID: string(threadID), IncludeTurns: true}, &response); err != nil {
		return room.ThreadSnapshot{}, stableAdapterError(err)
	}
	return a.snapshot(response.Thread, threadID)
}

func (a *Adapter) ResumeThread(ctx context.Context, threadID room.ThreadID) (room.ThreadSnapshot, error) {
	if !validProtocolID(string(threadID)) {
		return room.ThreadSnapshot{}, ErrInvalidAgentRequest
	}
	if err := preflightResumeThreadRequest(string(threadID), a.projectRoot); err != nil {
		return room.ThreadSnapshot{}, err
	}
	var response policyThreadResult
	if err := a.rpc.Call(ctx, "thread/resume", resumeThreadRequest(string(threadID), a.projectRoot), &response); err != nil {
		return room.ThreadSnapshot{}, stableAdapterError(err)
	}
	if err := a.validatePolicy(response); err != nil {
		return room.ThreadSnapshot{}, err
	}
	return a.snapshot(response.Thread, threadID)
}

func (a *Adapter) StartTurn(ctx context.Context, threadID room.ThreadID, clientMessageID room.ClientMessageID, text string) (room.TurnID, error) {
	if !validProtocolID(string(threadID)) || !room.ValidClientMessageID(clientMessageID) || strings.TrimSpace(text) == "" {
		return "", notSentMutation("turn/start", ErrInvalidAgentRequest)
	}
	if err := preflightStartTurnRequest(string(threadID), string(clientMessageID), a.projectRoot, text); err != nil {
		return "", notSentMutation("turn/start", err)
	}
	var response turnStartResult
	request := startTurnRequest(string(threadID), string(clientMessageID), a.projectRoot, text)
	if err := a.rpc.Call(ctx, "turn/start", request, &response); err != nil {
		return "", mutationError("turn/start", err)
	}
	turn, err := decodeProtocolTurn(response.Turn)
	if err != nil {
		return "", unknownMutation("turn/start", ErrInvalidResponse)
	}
	if _, ok := mapTurnState(turn.Status); !ok {
		return "", unknownMutation("turn/start", ErrInvalidResponse)
	}
	return room.TurnID(turn.ID), nil
}

func (a *Adapter) SteerTurn(ctx context.Context, threadID room.ThreadID, turnID room.TurnID, text string) error {
	if !validProtocolID(string(threadID)) || !validProtocolID(string(turnID)) || strings.TrimSpace(text) == "" {
		return notSentMutation("turn/steer", ErrInvalidAgentRequest)
	}
	if err := preflightSteerTurnRequest(string(threadID), string(turnID), text); err != nil {
		return notSentMutation("turn/steer", err)
	}
	var response turnSteerResult
	if err := a.rpc.Call(ctx, "turn/steer", steerTurnRequest(string(threadID), string(turnID), text), &response); err != nil {
		return mutationError("turn/steer", err)
	}
	if response.TurnID == nil || !validProtocolID(*response.TurnID) || *response.TurnID != string(turnID) {
		return unknownMutation("turn/steer", ErrInvalidResponse)
	}
	return nil
}

func (a *Adapter) InterruptTurn(ctx context.Context, threadID room.ThreadID, turnID room.TurnID) error {
	if !validProtocolID(string(threadID)) || !validProtocolID(string(turnID)) {
		return notSentMutation("turn/interrupt", ErrInvalidAgentRequest)
	}
	if err := preflightInterruptTurnRequest(string(threadID), string(turnID)); err != nil {
		return notSentMutation("turn/interrupt", err)
	}
	var response map[string]json.RawMessage
	if err := a.rpc.Call(ctx, "turn/interrupt", turnInterruptParams{ThreadID: string(threadID), TurnID: string(turnID)}, &response); err != nil {
		return mutationError("turn/interrupt", err)
	}
	if response == nil {
		return unknownMutation("turn/interrupt", ErrInvalidResponse)
	}
	return nil
}

func (a *Adapter) validatePolicy(response policyThreadResult) error {
	if response.CWD == nil || *response.CWD != a.projectRoot ||
		response.ApprovalPolicy == nil || *response.ApprovalPolicy != approvalPolicy ||
		response.Sandbox == nil || response.Sandbox.Type != turnSandboxType {
		return ErrRuntimePolicyMismatch
	}
	return nil
}

func (a *Adapter) snapshot(raw json.RawMessage, expected room.ThreadID) (room.ThreadSnapshot, error) {
	thread, err := decodeProtocolThread(raw)
	if err != nil || thread.CWD == nil {
		return room.ThreadSnapshot{}, ErrInvalidResponse
	}
	if *thread.CWD != a.projectRoot || expected != "" && thread.ID != string(expected) {
		return room.ThreadSnapshot{}, ErrThreadBindingMismatch
	}
	snapshot := room.ThreadSnapshot{ID: room.ThreadID(thread.ID), CWD: *thread.CWD}
	totalItems := 0
	_, err = streamJSONArray(thread.Turns, maxSnapshotTurns, func(rawTurn json.RawMessage) error {
		turn, err := decodeProtocolTurnObject(rawTurn)
		if err != nil {
			return ErrInvalidResponse
		}
		state, ok := mapTurnState(turn.Status)
		if !ok {
			return ErrInvalidResponse
		}
		remainingItems := maxSnapshotItems - totalItems
		itemLimit := maxItemsPerTurn
		if remainingItems < itemLimit {
			itemLimit = remainingItems
		}
		turnSnapshot := room.TurnSnapshot{ID: room.TurnID(turn.ID), State: state}
		_, err = streamJSONArray(turn.Items, itemLimit, func(rawItem json.RawMessage) error {
			itemID, err := itemID(rawItem)
			if err != nil {
				return ErrInvalidResponse
			}
			totalItems++
			if !snapshotItemCompleted(rawItem, state) {
				return nil
			}
			turnSnapshot.Items = append(turnSnapshot.Items, room.CompletedItem{
				ThreadID: snapshot.ID, TurnID: turnSnapshot.ID, ItemID: room.ItemID(itemID), Payload: cloneRaw(rawItem),
			})
			return nil
		})
		if err != nil {
			return err
		}
		snapshot.Turns = append(snapshot.Turns, turnSnapshot)
		return nil
	})
	if err != nil {
		return room.ThreadSnapshot{}, ErrInvalidResponse
	}
	return snapshot, nil
}

// Thread/read and thread/resume include unfinished items. Only transport
// evidence of completion may cross the CompletedItem domain boundary. These
// closed status sets match the pinned AppServer schema; a terminal turn does
// not override an explicitly unfinished or unknown item status.
func snapshotItemCompleted(raw json.RawMessage, turnState room.RequestState) bool {
	var item struct {
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &item) != nil {
		return false
	}
	switch item.Type {
	case "commandExecution", "fileChange":
		return item.Status == "completed" || item.Status == "failed" || item.Status == "declined"
	case "mcpToolCall", "dynamicToolCall":
		return item.Status == "completed" || item.Status == "failed"
	case "collabAgentToolCall":
		return item.Status == "completed" || item.Status == "failed" || item.Status == "interrupted"
	case "imageGeneration":
		// The pinned schema leaves this status open-ended. Require a successful
		// terminal turn as well as a recognized final status.
		return turnState == room.RequestCompleted && (item.Status == "completed" || item.Status == "failed")
	case "userMessage", "hookPrompt", "agentMessage", "functionCallOutput", "plan", "reasoning", "subAgentActivity", "webSearch", "imageView", "sleep", "enteredReviewMode", "exitedReviewMode", "contextCompaction":
		// Failure/interruption can leave a statusless item's text incomplete.
		return item.Status == "" && turnState == room.RequestCompleted
	default:
		return false
	}
}

func decodeProtocolThread(raw json.RawMessage) (protocolThread, error) {
	if !isJSONObject(raw) {
		return protocolThread{}, ErrInvalidResponse
	}
	var thread protocolThread
	if err := json.Unmarshal(raw, &thread); err != nil || !validProtocolID(thread.ID) {
		return protocolThread{}, ErrInvalidResponse
	}
	return thread, nil
}

func decodeProtocolTurn(raw json.RawMessage) (protocolTurn, error) {
	turn, err := decodeProtocolTurnObject(raw)
	if err != nil {
		return protocolTurn{}, err
	}
	if _, err := streamJSONArray(turn.Items, maxItemsPerTurn, nil); err != nil {
		return protocolTurn{}, ErrInvalidResponse
	}
	return turn, nil
}

func decodeProtocolTurnObject(raw json.RawMessage) (protocolTurn, error) {
	if !isJSONObject(raw) {
		return protocolTurn{}, ErrInvalidResponse
	}
	var turn protocolTurn
	if err := json.Unmarshal(raw, &turn); err != nil || !validProtocolID(turn.ID) {
		return protocolTurn{}, ErrInvalidResponse
	}
	return turn, nil
}

func streamJSONArray(raw json.RawMessage, maxElements int, visit func(json.RawMessage) error) (int, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	open, err := decoder.Token()
	if err != nil || open != json.Delim('[') {
		return 0, ErrInvalidResponse
	}
	count := 0
	for decoder.More() {
		if count >= maxElements {
			return count, ErrInvalidResponse
		}
		var element json.RawMessage
		if err := decoder.Decode(&element); err != nil {
			return count, ErrInvalidResponse
		}
		count++
		if visit != nil {
			if err := visit(element); err != nil {
				return count, err
			}
		}
	}
	closeToken, err := decoder.Token()
	if err != nil || closeToken != json.Delim(']') {
		return count, ErrInvalidResponse
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return count, ErrInvalidResponse
	}
	return count, nil
}

func itemID(raw json.RawMessage) (string, error) {
	if !isJSONObject(raw) {
		return "", ErrInvalidResponse
	}
	var item startedItem
	if err := json.Unmarshal(raw, &item); err != nil || !validProtocolID(item.ID) || item.Type == "" {
		return "", ErrInvalidResponse
	}
	return item.ID, nil
}

func validProtocolID(value string) bool {
	if len(value) == 0 || len(value) > maxProtocolIDBytes || !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return false
	}
	for _, r := range value {
		if unicode.In(r, unicode.Cc, unicode.Cf) {
			return false
		}
	}
	return true
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

func mutationError(operation string, err error) error {
	written := 0
	var callErr *CallError
	if errors.As(err, &callErr) {
		written = callErr.BytesWritten
	}
	certainty := room.DeliveryUnknown
	if written == 0 && certainNotSentError(err) {
		certainty = room.DeliveryNotSent
	}
	return &room.MutationError{Operation: operation, Certainty: certainty, Err: stableAdapterError(err)}
}

func certainNotSentError(err error) bool {
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		return false
	}
	return !errors.Is(err, io.EOF) &&
		!errors.Is(err, ErrResultDecode) &&
		!errors.Is(err, ErrInvalidWireMessage) &&
		!errors.Is(err, ErrInvalidWireID) &&
		!errors.Is(err, ErrJSONLLineTooLong) &&
		!errors.Is(err, ErrEmptyJSONLLine) &&
		!errors.Is(err, ErrTruncatedJSONLLine) &&
		!errors.Is(err, ErrUnknownResponseID) &&
		!errors.Is(err, ErrDuplicateServerRequestID)
}

func notSentMutation(operation string, err error) error {
	return &room.MutationError{Operation: operation, Certainty: room.DeliveryNotSent, Err: err}
}

func unknownMutation(operation string, err error) error {
	return &room.MutationError{Operation: operation, Certainty: room.DeliveryUnknown, Err: err}
}

func stableAdapterError(err error) error {
	var callErr *CallError
	if errors.As(err, &callErr) {
		err = callErr.Err
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		return ErrRPCRequestFailed
	}
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, io.EOF):
		return io.EOF
	case errors.Is(err, ErrResultDecode):
		return ErrInvalidResponse
	case errors.Is(err, ErrRPCMessageTooLarge):
		return ErrRPCMessageTooLarge
	case errors.Is(err, ErrInvalidWireMessage), errors.Is(err, ErrInvalidWireID), errors.Is(err, ErrJSONLLineTooLong), errors.Is(err, ErrEmptyJSONLLine),
		errors.Is(err, ErrTruncatedJSONLLine), errors.Is(err, ErrUnknownResponseID), errors.Is(err, ErrDuplicateServerRequestID):
		return ErrInvalidResponse
	default:
		return ErrRPCTransport
	}
}

func (a *Adapter) forwardNotifications() {
	defer close(a.events)
	defer close(a.forwardDone)
	for {
		var notification Notification
		select {
		case <-a.ownerDone:
			return
		case next, ok := <-a.rpc.Events():
			if !ok {
				return
			}
			notification = next
		}
		event, ok := mapNotification(notification)
		if !ok {
			continue
		}
		select {
		case a.events <- event:
		case <-a.ownerDone:
			return
		}
	}
}

func mapNotification(notification Notification) (room.AgentEvent, bool) {
	if notification.ServerRequest {
		var ids struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
		}
		_ = json.Unmarshal(notification.Params, &ids)
		event := room.AgentEvent{Kind: "unsupported-server-request"}
		if validProtocolIDs(ids.ThreadID, ids.TurnID) {
			event.ThreadID = room.ThreadID(ids.ThreadID)
			event.TurnID = room.TurnID(ids.TurnID)
		}
		return event, true
	}
	switch notification.Method {
	case "item/agentMessage/delta", "item/plan/delta", "item/commandExecution/outputDelta", "item/fileChange/outputDelta":
		var params deltaNotification
		if json.Unmarshal(notification.Params, &params) != nil || !validProtocolIDs(params.ThreadID, params.TurnID, params.ItemID) || params.Delta == nil {
			return room.AgentEvent{Kind: "protocol-error", Error: ErrInvalidResponse}, true
		}
		return room.AgentEvent{Kind: "item-delta", ThreadID: room.ThreadID(params.ThreadID), TurnID: room.TurnID(params.TurnID), ItemID: room.ItemID(params.ItemID), Delta: *params.Delta}, true
	case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		var params deltaNotification
		if json.Unmarshal(notification.Params, &params) != nil || !validProtocolIDs(params.ThreadID, params.TurnID, params.ItemID) || params.Delta == nil ||
			notification.Method == "item/reasoning/summaryTextDelta" && params.SummaryIndex == nil ||
			notification.Method == "item/reasoning/textDelta" && params.ContentIndex == nil {
			return room.AgentEvent{Kind: "protocol-error", Error: ErrInvalidResponse}, true
		}
		return room.AgentEvent{Kind: "item-delta", ThreadID: room.ThreadID(params.ThreadID), TurnID: room.TurnID(params.TurnID), ItemID: room.ItemID(params.ItemID), Delta: *params.Delta}, true
	case "item/started":
		var params itemNotification
		if json.Unmarshal(notification.Params, &params) != nil || params.StartedAtMs == nil {
			return room.AgentEvent{Kind: "protocol-error", Error: ErrInvalidResponse}, true
		}
		id, err := itemID(params.Item)
		if err != nil || !validProtocolIDs(params.ThreadID, params.TurnID) {
			return room.AgentEvent{Kind: "protocol-error", Error: ErrInvalidResponse}, true
		}
		return room.AgentEvent{Kind: "item-started", ThreadID: room.ThreadID(params.ThreadID), TurnID: room.TurnID(params.TurnID), ItemID: room.ItemID(id)}, true
	case "item/completed":
		var params itemNotification
		if json.Unmarshal(notification.Params, &params) != nil || params.CompletedAtMs == nil {
			return room.AgentEvent{Kind: "protocol-error", Error: ErrInvalidResponse}, true
		}
		id, err := itemID(params.Item)
		if err != nil || !validProtocolIDs(params.ThreadID, params.TurnID) {
			return room.AgentEvent{Kind: "protocol-error", Error: ErrInvalidResponse}, true
		}
		completed := &room.CompletedItem{
			ThreadID: room.ThreadID(params.ThreadID), TurnID: room.TurnID(params.TurnID),
			ItemID: room.ItemID(id), Payload: cloneRaw(params.Item),
		}
		return room.AgentEvent{Kind: "item-completed", ThreadID: completed.ThreadID, TurnID: completed.TurnID, ItemID: completed.ItemID, Completed: completed}, true
	case "turn/started", "turn/completed":
		var params turnNotification
		if json.Unmarshal(notification.Params, &params) != nil || !validProtocolID(params.ThreadID) {
			return room.AgentEvent{Kind: "protocol-error", Error: ErrInvalidResponse}, true
		}
		turn, err := decodeProtocolTurn(params.Turn)
		if err != nil {
			return room.AgentEvent{Kind: "protocol-error", Error: ErrInvalidResponse}, true
		}
		state, ok := mapTurnState(turn.Status)
		if !ok {
			return room.AgentEvent{Kind: "protocol-error", Error: ErrInvalidResponse}, true
		}
		if notification.Method == "turn/started" && state != room.RequestRunning {
			return room.AgentEvent{Kind: "protocol-error", Error: ErrInvalidResponse}, true
		}
		kind := "turn-started"
		var eventErr error
		if notification.Method == "turn/completed" {
			switch state {
			case room.RequestCompleted:
				kind = "turn-completed"
			case room.RequestInterrupted:
				kind = "turn-interrupted"
			case room.RequestFailed:
				kind, eventErr = "turn-failed", ErrTurnFailed
			default:
				return room.AgentEvent{Kind: "protocol-error", Error: ErrInvalidResponse}, true
			}
		}
		return room.AgentEvent{Kind: kind, ThreadID: room.ThreadID(params.ThreadID), TurnID: room.TurnID(turn.ID), Error: eventErr}, true
	case "error":
		var params struct {
			ThreadID  string          `json:"threadId"`
			TurnID    string          `json:"turnId"`
			WillRetry *bool           `json:"willRetry"`
			Error     json.RawMessage `json:"error"`
		}
		if json.Unmarshal(notification.Params, &params) != nil || !validProtocolIDs(params.ThreadID, params.TurnID) || params.WillRetry == nil || !validTurnError(params.Error) {
			return room.AgentEvent{Kind: "protocol-error", Error: ErrInvalidResponse}, true
		}
		return room.AgentEvent{Kind: "turn-error", ThreadID: room.ThreadID(params.ThreadID), TurnID: room.TurnID(params.TurnID), Error: ErrTurnFailed}, true
	default:
		return room.AgentEvent{}, false
	}
}

func validProtocolIDs(values ...string) bool {
	for _, value := range values {
		if !validProtocolID(value) {
			return false
		}
	}
	return true
}

func validTurnError(raw json.RawMessage) bool {
	if !isJSONObject(raw) {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	var message string
	return decodeJSONString(fields["message"], &message)
}
