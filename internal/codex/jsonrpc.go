package codex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"unicode/utf8"
)

const (
	MaxJSONLLineBytes = 8 << 20
	// One raw event is enough to decouple filtered startup notifications while
	// keeping legal 8 MiB records under strict byte backpressure.
	rpcEventQueueCapacity = 1
)

var (
	ErrJSONLLineTooLong         = errors.New("Codex App Server JSONL line exceeds 8 MiB")
	ErrEmptyJSONLLine           = errors.New("Codex App Server emitted an empty JSONL line")
	ErrTruncatedJSONLLine       = errors.New("Codex App Server JSONL line is truncated")
	ErrInvalidWireMessage       = errors.New("invalid Codex App Server wire message")
	ErrInvalidWireID            = errors.New("invalid Codex App Server request id")
	ErrUnknownResponseID        = errors.New("unknown Codex App Server response id")
	ErrDuplicateServerRequestID = errors.New("duplicate active Codex App Server request id")
	ErrServerRequestBusy        = errors.New("Codex App Server reverse request capacity exceeded")
	ErrRPCAlreadyRunning        = errors.New("Codex App Server RPC client is already running")
	ErrRPCClosed                = errors.New("Codex App Server RPC client is closed")
	ErrInvalidRPCMethod         = errors.New("Codex App Server RPC method is empty")
	ErrRPCMessageTooLarge       = errors.New("Codex App Server outbound JSONL line exceeds 8 MiB")
	ErrResultDecode             = errors.New("invalid Codex App Server result")
)

type RPCError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return "Codex App Server RPC error"
	}
	return fmt.Sprintf("Codex App Server RPC error %d", e.Code)
}

type CallError struct {
	BytesWritten int
	Err          error
}

func (e *CallError) Error() string {
	if e == nil || e.Err == nil {
		return "Codex App Server call failed"
	}
	return e.Err.Error()
}

func (e *CallError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type Notification struct {
	Method        string
	Params        json.RawMessage
	EmittedAtMs   *int64
	ServerRequest bool
}

type ServerRequest struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
	Trace  json.RawMessage
}

type ServerRequestCallback func(context.Context, ServerRequest)

type pendingPhase uint8

const (
	pendingPreWrite pendingPhase = iota
	pendingInDirectWrite
	pendingWriteDone
	pendingRejected
)

type pendingCall struct {
	response            chan callResult
	phase               pendingPhase
	bytesWritten        int
	quarantinedResponse *callResult
}

type callResult struct {
	result json.RawMessage
	rpcErr *RPCError
	err    error
}

type RPCClient struct {
	reader      *bufio.Reader
	readerClose io.Closer
	writer      io.Writer
	writerClose io.Closer
	writerGate  chan struct{}
	closeOnce   sync.Once

	stateMu     sync.Mutex
	nextID      int64
	pending     map[string]*pendingCall
	runStarted  bool
	runCancel   context.CancelFunc
	terminalErr error
	terminalCh  chan struct{}
	done        chan struct{}

	callbackMu sync.RWMutex
	callback   ServerRequestCallback

	serverActive map[string]struct{}
	serverSlots  chan struct{}
	handlers     sync.WaitGroup

	events chan Notification
}

func NewRPCClient(reader io.Reader, writer io.Writer) *RPCClient {
	c := &RPCClient{
		reader:       bufio.NewReaderSize(reader, 64<<10),
		writer:       writer,
		writerGate:   make(chan struct{}, 1),
		pending:      make(map[string]*pendingCall),
		terminalCh:   make(chan struct{}),
		done:         make(chan struct{}),
		serverActive: make(map[string]struct{}),
		serverSlots:  make(chan struct{}, 1),
		events:       make(chan Notification, rpcEventQueueCapacity),
	}
	if closer, ok := reader.(io.Closer); ok {
		c.readerClose = closer
	}
	if closer, ok := writer.(io.Closer); ok {
		c.writerClose = closer
	}
	c.writerGate <- struct{}{}
	return c
}

func (c *RPCClient) Events() <-chan Notification { return c.events }
func (c *RPCClient) Done() <-chan struct{}       { return c.done }

func (c *RPCClient) SetServerRequestCallback(callback ServerRequestCallback) error {
	c.callbackMu.Lock()
	c.callback = callback
	c.callbackMu.Unlock()
	return nil
}

func (c *RPCClient) Run(ctx context.Context) (retErr error) {
	c.stateMu.Lock()
	if c.runStarted {
		c.stateMu.Unlock()
		return ErrRPCAlreadyRunning
	}
	c.runStarted = true
	runCtx, cancel := context.WithCancel(ctx)
	c.runCancel = cancel
	terminal := c.terminalErr
	c.stateMu.Unlock()

	defer func() {
		c.failAll(retErr)
		cancel()
		c.handlers.Wait()
		close(c.events)
		close(c.done)
	}()
	if terminal != nil {
		return terminal
	}
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-runCtx.Done():
			c.failAll(runCtx.Err())
		case <-c.terminalCh:
		}
	}()
	defer func() { <-watchDone }()

	for {
		line, err := readJSONLLine(c.reader)
		if err != nil {
			c.failAll(err)
			return c.firstTerminal(err)
		}
		message, err := decodeWireMessage(line)
		if err != nil {
			c.failAll(err)
			return c.firstTerminal(err)
		}
		switch message.kind {
		case wireResponse:
			if err := c.deliverResponse(message); err != nil {
				c.failAll(err)
				return c.firstTerminal(err)
			}
		case wireNotification:
			notification := Notification{
				Method: message.method, Params: message.params, EmittedAtMs: cloneInt64(message.emittedAtMs),
			}
			select {
			case c.events <- notification:
			case <-runCtx.Done():
				err := runCtx.Err()
				c.failAll(err)
				return c.firstTerminal(err)
			}
		case wireServerRequest:
			if err := c.admitServerRequest(runCtx, message.idKey); err != nil {
				c.failAll(err)
				return c.firstTerminal(err)
			}
			work := prepareServerRequest(message)
			observation := Notification{Method: message.method, Params: message.params, ServerRequest: true}
			select {
			case c.events <- observation:
			case <-runCtx.Done():
				c.releaseServerRequest(message.idKey)
				err := runCtx.Err()
				c.failAll(err)
				return c.firstTerminal(err)
			}
			if err := c.startServerRequest(runCtx, work); err != nil {
				c.failAll(err)
				return c.firstTerminal(err)
			}
		}
	}
}

func (c *RPCClient) Call(ctx context.Context, method string, params any, result any) error {
	if method == "" {
		return &CallError{Err: ErrInvalidRPCMethod}
	}
	if err := ctx.Err(); err != nil {
		return &CallError{Err: err}
	}
	id, key, pending, err := c.registerPending()
	if err != nil {
		return &CallError{Err: err}
	}
	request := struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params,omitempty"`
	}{ID: id, Method: method, Params: params}
	line, err := json.Marshal(request)
	if err != nil {
		return c.finishPreWriteFailure(key, pending, ErrInvalidWireMessage)
	}
	if len(line) > MaxJSONLLineBytes {
		return c.finishPreWriteFailure(key, pending, ErrRPCMessageTooLarge)
	}
	written, err := c.writeJSONL(ctx, line, func() error {
		return c.beginPendingWrite(key, pending)
	})
	if stateErr := c.recordWriteOutcome(key, pending, written); stateErr != nil {
		c.failAll(stateErr)
	}
	if err != nil {
		removed := c.removePending(key, pending)
		if written > 0 || !isPreWriteContextError(err) {
			c.failAll(err)
		}
		if !removed {
			response := <-pending.response
			return decodeCallResult(written, response, result)
		}
		return &CallError{BytesWritten: written, Err: err}
	}

	select {
	case response := <-pending.response:
		return decodeCallResult(written, response, result)
	case <-ctx.Done():
		if c.removePending(key, pending) {
			return &CallError{BytesWritten: written, Err: ctx.Err()}
		}
		response := <-pending.response
		return decodeCallResult(written, response, result)
	}
}

func (c *RPCClient) Notify(ctx context.Context, method string, params any) error {
	if method == "" {
		return &CallError{Err: ErrInvalidRPCMethod}
	}
	if err := ctx.Err(); err != nil {
		return &CallError{Err: err}
	}
	notification := struct {
		Method string `json:"method"`
		Params any    `json:"params,omitempty"`
	}{Method: method, Params: params}
	line, err := json.Marshal(notification)
	if err != nil {
		return &CallError{Err: ErrInvalidWireMessage}
	}
	if len(line) > MaxJSONLLineBytes {
		return &CallError{Err: ErrRPCMessageTooLarge}
	}
	written, err := c.writeJSONL(ctx, line, nil)
	if err != nil {
		if written > 0 || !isPreWriteContextError(err) {
			c.failAll(err)
		}
		return &CallError{BytesWritten: written, Err: err}
	}
	return nil
}

func (c *RPCClient) registerPending() (int64, string, *pendingCall, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.terminalErr != nil {
		return 0, "", nil, c.terminalErr
	}
	if c.nextID == int64(^uint64(0)>>1) {
		return 0, "", nil, ErrInvalidWireID
	}
	c.nextID++
	id := c.nextID
	key := fmt.Sprintf("n:%d", id)
	pending := &pendingCall{response: make(chan callResult, 1)}
	c.pending[key] = pending
	return id, key, pending, nil
}

func (c *RPCClient) removePending(key string, pending *pendingCall) bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.pending[key] != pending {
		return false
	}
	delete(c.pending, key)
	return true
}

func (c *RPCClient) finishPreWriteFailure(key string, pending *pendingCall, localErr error) error {
	if c.removePending(key, pending) {
		return &CallError{Err: localErr}
	}
	response := <-pending.response
	return decodeCallResult(0, response, nil)
}

func (c *RPCClient) beginPendingWrite(key string, pending *pendingCall) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.pending[key] != pending {
		if pending.phase == pendingRejected {
			return ErrUnknownResponseID
		}
		if c.terminalErr != nil {
			return c.terminalErr
		}
		return ErrRPCClosed
	}
	if pending.phase != pendingPreWrite {
		return ErrInvalidWireMessage
	}
	pending.phase = pendingInDirectWrite
	return nil
}

func (c *RPCClient) deliverResponse(message wireMessage) error {
	response := callResult{result: cloneRaw(message.result), rpcErr: cloneRPCError(message.rpcErr)}
	c.stateMu.Lock()
	pending := c.pending[message.idKey]
	if pending == nil {
		c.stateMu.Unlock()
		return ErrUnknownResponseID
	}
	switch pending.phase {
	case pendingPreWrite:
		pending.phase = pendingRejected
		delete(c.pending, message.idKey)
		c.stateMu.Unlock()
		pending.response <- callResult{err: ErrUnknownResponseID}
		return ErrUnknownResponseID
	case pendingInDirectWrite:
		if pending.quarantinedResponse != nil {
			c.stateMu.Unlock()
			return ErrUnknownResponseID
		}
		pending.quarantinedResponse = &response
		c.stateMu.Unlock()
		return nil
	case pendingWriteDone:
		delete(c.pending, message.idKey)
		bytesWritten := pending.bytesWritten
		c.stateMu.Unlock()
		if bytesWritten == 0 {
			pending.response <- callResult{err: ErrUnknownResponseID}
			return ErrUnknownResponseID
		}
		pending.response <- response
		return nil
	default:
		c.stateMu.Unlock()
		return ErrInvalidWireMessage
	}
}

func (c *RPCClient) recordWriteOutcome(key string, pending *pendingCall, written int) error {
	c.stateMu.Lock()
	if c.pending[key] != pending {
		c.stateMu.Unlock()
		return nil
	}
	pending.phase = pendingWriteDone
	pending.bytesWritten = written
	if pending.quarantinedResponse == nil {
		c.stateMu.Unlock()
		return nil
	}
	response := *pending.quarantinedResponse
	pending.quarantinedResponse = nil
	delete(c.pending, key)
	c.stateMu.Unlock()
	if written == 0 {
		pending.response <- callResult{err: ErrUnknownResponseID}
		return ErrUnknownResponseID
	}
	pending.response <- response
	return nil
}

func decodeCallResult(written int, response callResult, result any) error {
	if response.err != nil {
		return &CallError{BytesWritten: written, Err: response.err}
	}
	if response.rpcErr != nil {
		return &CallError{BytesWritten: written, Err: response.rpcErr}
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(response.result, result); err != nil {
		return &CallError{BytesWritten: written, Err: ErrResultDecode}
	}
	return nil
}

func (c *RPCClient) writeJSONL(ctx context.Context, payload []byte, beforeFirstWrite func() error) (int, error) {
	if len(payload) > MaxJSONLLineBytes {
		return 0, ErrRPCMessageTooLarge
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-c.terminalCh:
		return 0, c.firstTerminal(ErrRPCClosed)
	case <-c.writerGate:
	}
	defer func() { c.writerGate <- struct{}{} }()

	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-c.terminalCh:
		return 0, c.firstTerminal(ErrRPCClosed)
	default:
	}

	line := make([]byte, len(payload)+1)
	copy(line, payload)
	line[len(payload)] = '\n'
	total := 0
	for total < len(line) {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case <-c.terminalCh:
			return total, c.firstTerminal(ErrRPCClosed)
		default:
		}
		if beforeFirstWrite != nil {
			// This callback is the direct-Write invocation's linearization
			// point: whichever side wins stateMu decides whether a response
			// is pre-write-fatal or may be quarantined for the write outcome.
			if err := beforeFirstWrite(); err != nil {
				return total, err
			}
			beforeFirstWrite = nil
		}
		n, err := c.writer.Write(line[total:])
		if n < 0 {
			return total, io.ErrShortWrite
		}
		if n > len(line)-total {
			return len(line), io.ErrShortWrite
		}
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
	return total, nil
}

func (c *RPCClient) failAll(err error) {
	if err == nil {
		err = ErrRPCClosed
	}
	c.stateMu.Lock()
	if c.terminalErr != nil {
		c.stateMu.Unlock()
		return
	}
	c.terminalErr = err
	close(c.terminalCh)
	pending := c.pending
	c.pending = make(map[string]*pendingCall)
	cancel := c.runCancel
	c.stateMu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, call := range pending {
		call.response <- callResult{err: err}
	}
	c.closeTransport()
}

func (c *RPCClient) closeTransport() {
	c.closeOnce.Do(func() {
		if c.writerClose != nil {
			_ = c.writerClose.Close()
		}
		if c.readerClose != nil && !sameCloser(c.readerClose, c.writerClose) {
			_ = c.readerClose.Close()
		}
	})
}

func sameCloser(left, right io.Closer) bool {
	if left == nil || right == nil {
		return false
	}
	leftType := reflect.TypeOf(left)
	return leftType == reflect.TypeOf(right) && leftType.Comparable() && left == right
}

func (c *RPCClient) firstTerminal(fallback error) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.terminalErr != nil {
		return c.terminalErr
	}
	return fallback
}

type serverRequestWork struct {
	key        string
	request    ServerRequest
	responseID json.RawMessage
}

func prepareServerRequest(message wireMessage) serverRequestWork {
	return serverRequestWork{
		key: message.idKey,
		request: ServerRequest{
			ID: cloneRaw(message.id), Method: message.method,
			Params: cloneRaw(message.params), Trace: cloneRaw(message.trace),
		},
		responseID: cloneRaw(message.id),
	}
}

func (c *RPCClient) startServerRequest(ctx context.Context, work serverRequestWork) error {
	c.stateMu.Lock()
	err := c.terminalErr
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && !c.serverRequestActiveLocked(work.key) {
		err = ErrInvalidWireMessage
	}
	if err == nil {
		c.handlers.Add(1)
	}
	c.stateMu.Unlock()
	if err != nil {
		c.releaseServerRequest(work.key)
		return err
	}
	go func(work serverRequestWork) {
		defer c.handlers.Done()
		defer func() {
			c.releaseServerRequest(work.key)
		}()
		if ctx.Err() != nil {
			return
		}
		c.callbackMu.RLock()
		callback := c.callback
		c.callbackMu.RUnlock()
		if callback != nil {
			callback(ctx, work.request)
		}
		if ctx.Err() != nil {
			return
		}
		result, rpcErr := RejectServerRequest(work.request.Method)
		response, err := marshalServerResponse(work.responseID, result, rpcErr)
		if err != nil {
			c.failAll(err)
			return
		}
		if _, err := c.writeJSONL(ctx, response, nil); err != nil {
			c.failAll(err)
		}
	}(work)
	return nil
}

func (c *RPCClient) admitServerRequest(ctx context.Context, key string) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.terminalErr != nil {
		return c.terminalErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, exists := c.serverActive[key]; exists {
		return ErrDuplicateServerRequestID
	}
	c.serverActive[key] = struct{}{}
	select {
	case c.serverSlots <- struct{}{}:
		return nil
	default:
		delete(c.serverActive, key)
		return ErrServerRequestBusy
	}
}

func (c *RPCClient) releaseServerRequest(key string) {
	c.stateMu.Lock()
	select {
	case <-c.serverSlots:
	default:
	}
	delete(c.serverActive, key)
	c.stateMu.Unlock()
}

func (c *RPCClient) serverRequestActiveLocked(key string) bool {
	_, exists := c.serverActive[key]
	return exists && len(c.serverSlots) == 1
}

func RejectServerRequest(method string) (any, *RPCError) {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return map[string]string{"decision": "cancel"}, nil
	default:
		return nil, &RPCError{Code: -32090, Message: "agent_romm does not service App Server input requests"}
	}
}

func marshalServerResponse(id json.RawMessage, result any, rpcErr *RPCError) ([]byte, error) {
	const prefix = `{"id":`
	const suffix = `}`
	field := `,"result":`
	if rpcErr != nil {
		field = `,"error":`
	}
	fixedSize := len(prefix) + len(field) + len(suffix)
	if len(id) > MaxJSONLLineBytes-fixedSize {
		return nil, ErrRPCMessageTooLarge
	}
	body, err := json.Marshal(result)
	if rpcErr != nil {
		body, err = json.Marshal(rpcErr)
	}
	if err != nil {
		return nil, ErrInvalidWireMessage
	}
	if len(body) > MaxJSONLLineBytes-fixedSize-len(id) {
		return nil, ErrRPCMessageTooLarge
	}
	size := fixedSize + len(id) + len(body)
	response := make([]byte, 0, size)
	response = append(response, prefix...)
	response = append(response, id...)
	response = append(response, field...)
	response = append(response, body...)
	response = append(response, suffix...)
	return response, nil
}

func readJSONLLine(reader *bufio.Reader) ([]byte, error) {
	line := make([]byte, 0, 64<<10)
	for {
		fragment, err := reader.ReadSlice('\n')
		hasNewline := len(fragment) > 0 && fragment[len(fragment)-1] == '\n'
		payloadFragment := fragment
		if hasNewline {
			payloadFragment = fragment[:len(fragment)-1]
		}
		if len(line)+len(payloadFragment) > MaxJSONLLineBytes {
			return nil, ErrJSONLLineTooLong
		}
		line = append(line, payloadFragment...)
		if hasNewline {
			if len(bytes.TrimSpace(line)) == 0 {
				return nil, ErrEmptyJSONLLine
			}
			return line, nil
		}
		if err == nil {
			continue
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				return nil, io.EOF
			}
			return nil, ErrTruncatedJSONLLine
		}
		return nil, err
	}
}

type wireKind uint8

const (
	wireResponse wireKind = iota + 1
	wireNotification
	wireServerRequest
)

type wireMessage struct {
	kind        wireKind
	id          json.RawMessage
	idKey       string
	method      string
	params      json.RawMessage
	result      json.RawMessage
	rpcErr      *RPCError
	trace       json.RawMessage
	emittedAtMs *int64
}

func decodeWireMessage(data []byte) (wireMessage, error) {
	if !utf8.Valid(data) {
		return wireMessage{}, ErrInvalidWireMessage
	}
	if err := validateNoDuplicateKeys(data); err != nil {
		return wireMessage{}, ErrInvalidWireMessage
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return wireMessage{}, ErrInvalidWireMessage
	}
	allowed := map[string]bool{
		"id": true, "method": true, "params": true, "result": true,
		"error": true, "trace": true, "emittedAtMs": true,
	}
	for key := range fields {
		if !allowed[key] {
			return wireMessage{}, ErrInvalidWireMessage
		}
	}
	id, hasID := fields["id"]
	methodRaw, hasMethod := fields["method"]
	result, hasResult := fields["result"]
	errorRaw, hasError := fields["error"]
	params, hasParams := fields["params"]
	trace, hasTrace := fields["trace"]
	emittedRaw, hasEmitted := fields["emittedAtMs"]

	message := wireMessage{params: cloneRaw(params), result: cloneRaw(result), trace: cloneRaw(trace)}
	if hasID {
		key, err := wireIDKey(id)
		if err != nil {
			return wireMessage{}, err
		}
		message.id, message.idKey = cloneRaw(id), key
	}
	if hasMethod {
		if !decodeJSONString(methodRaw, &message.method) {
			return wireMessage{}, ErrInvalidWireMessage
		}
	}

	switch {
	case hasID && !hasMethod:
		if hasParams || hasTrace || hasEmitted || hasResult == hasError {
			return wireMessage{}, ErrInvalidWireMessage
		}
		message.kind = wireResponse
		if hasError {
			rpcErr, err := decodeRPCError(errorRaw)
			if err != nil {
				return wireMessage{}, err
			}
			message.rpcErr = rpcErr
		}
	case !hasID && hasMethod:
		if hasResult || hasError || hasTrace {
			return wireMessage{}, ErrInvalidWireMessage
		}
		message.kind = wireNotification
		if hasEmitted {
			var timestamp int64
			if !decodeJSONInt64(emittedRaw, &timestamp) {
				return wireMessage{}, ErrInvalidWireMessage
			}
			message.emittedAtMs = &timestamp
		}
	case hasID && hasMethod:
		if hasResult || hasError || hasEmitted {
			return wireMessage{}, ErrInvalidWireMessage
		}
		if hasTrace && !validTrace(trace) {
			return wireMessage{}, ErrInvalidWireMessage
		}
		message.kind = wireServerRequest
	default:
		return wireMessage{}, ErrInvalidWireMessage
	}
	return message, nil
}

func wireIDKey(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", ErrInvalidWireID
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		// A collision intentionally fails closed as a duplicate or unknown ID.
		digest := sha256.Sum256([]byte(text))
		return "s:" + string(digest[:]), nil
	}
	var number int64
	if err := json.Unmarshal(raw, &number); err == nil {
		return fmt.Sprintf("n:%d", number), nil
	}
	return "", ErrInvalidWireID
}

func decodeRPCError(raw json.RawMessage) (*RPCError, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, ErrInvalidWireMessage
	}
	for key := range fields {
		if key != "code" && key != "message" && key != "data" {
			return nil, ErrInvalidWireMessage
		}
	}
	codeRaw, hasCode := fields["code"]
	messageRaw, hasMessage := fields["message"]
	if !hasCode || !hasMessage {
		return nil, ErrInvalidWireMessage
	}
	var rpcErr RPCError
	if !decodeJSONInt64(codeRaw, &rpcErr.Code) {
		return nil, ErrInvalidWireMessage
	}
	if !decodeJSONString(messageRaw, &rpcErr.Message) {
		return nil, ErrInvalidWireMessage
	}
	rpcErr.Data = cloneRaw(fields["data"])
	return &rpcErr, nil
}

func validTrace(raw json.RawMessage) bool {
	if bytes.Equal(raw, []byte("null")) {
		return true
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return false
	}
	for key, value := range fields {
		if key != "traceparent" && key != "tracestate" {
			return false
		}
		if bytes.Equal(value, []byte("null")) {
			continue
		}
		var text string
		if !decodeJSONString(value, &text) {
			return false
		}
	}
	return true
}

func decodeJSONString(raw json.RawMessage, destination *string) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '"' {
		return false
	}
	return json.Unmarshal(trimmed, destination) == nil
}

func decodeJSONInt64(raw json.RawMessage, destination *int64) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	return json.Unmarshal(trimmed, destination) == nil
}

func validateNoDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return ErrInvalidWireMessage
		}
		return err
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrInvalidWireMessage
			}
			if _, duplicate := keys[key]; duplicate {
				return ErrInvalidWireMessage
			}
			keys[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return ErrInvalidWireMessage
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return ErrInvalidWireMessage
		}
	default:
		return ErrInvalidWireMessage
	}
	return nil
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneRPCError(value *RPCError) *RPCError {
	if value == nil {
		return nil
	}
	return &RPCError{Code: value.Code, Message: value.Message, Data: cloneRaw(value.Data)}
}

func isPreWriteContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
