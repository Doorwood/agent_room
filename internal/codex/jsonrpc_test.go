package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReadJSONLExactBoundaryAndFailures(t *testing.T) {
	prefix := `{"method":"event","params":"`
	suffix := `"}`
	exact := prefix + strings.Repeat("a", MaxJSONLLineBytes-len(prefix)-len(suffix)) + suffix
	line, err := readJSONLLine(bufio.NewReader(strings.NewReader(exact + "\n")))
	if err != nil || len(line) != MaxJSONLLineBytes {
		t.Fatalf("exact boundary len=%d err=%v", len(line), err)
	}

	tests := []struct {
		name  string
		input []byte
		want  error
	}{
		{"overlong", []byte(exact + "x\n"), ErrJSONLLineTooLong},
		{"empty", []byte("\n"), ErrEmptyJSONLLine},
		{"whitespace", []byte(" \t\n"), ErrEmptyJSONLLine},
		{"truncated", []byte(`{"method":"event"}`), ErrTruncatedJSONLLine},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readJSONLLine(bufio.NewReader(bytes.NewReader(tc.input)))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
		})
	}
}

func runWire(t *testing.T, input []byte) error {
	t.Helper()
	rpc := NewRPCClient(bytes.NewReader(input), io.Discard)
	return rpc.Run(context.Background())
}

func TestRPCRejectsMalformedOrWrongShapeMessages(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{"invalid utf8", []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}', '\n'}},
		{"recursive duplicate", []byte("{\"method\":\"event\",\"params\":{\"nested\":{\"x\":1,\"x\":2}}}\n")},
		{"unknown top level", []byte("{\"method\":\"event\",\"extra\":1}\n")},
		{"jsonrpc is unknown", []byte("{\"jsonrpc\":\"2.0\",\"method\":\"event\"}\n")},
		{"response result and error", []byte("{\"id\":1,\"result\":{},\"error\":{\"code\":1,\"message\":\"bad\"}}\n")},
		{"response neither result nor error", []byte("{\"id\":1}\n")},
		{"response with method", []byte("{\"id\":1,\"method\":\"x\",\"result\":{}}\n")},
		{"response trace misplaced", []byte("{\"id\":1,\"result\":{},\"trace\":null}\n")},
		{"notification id null", []byte("{\"id\":null,\"method\":\"x\"}\n")},
		{"notification trace misplaced", []byte("{\"method\":\"x\",\"trace\":null}\n")},
		{"notification null method", []byte("{\"method\":null}\n")},
		{"notification null timestamp", []byte("{\"method\":\"x\",\"emittedAtMs\":null}\n")},
		{"request timestamp misplaced", []byte("{\"id\":\"s\",\"method\":\"x\",\"emittedAtMs\":1}\n")},
		{"request null method", []byte("{\"id\":\"s\",\"method\":null}\n")},
		{"invalid float id", []byte("{\"id\":1.5,\"result\":{}}\n")},
		{"invalid bool id", []byte("{\"id\":true,\"result\":{}}\n")},
		{"invalid overflow id", []byte("{\"id\":9223372036854775808,\"result\":{}}\n")},
		{"error missing message", []byte("{\"id\":1,\"error\":{\"code\":1}}\n")},
		{"error null code", []byte("{\"id\":1,\"error\":{\"code\":null,\"message\":\"bad\"}}\n")},
		{"error null message", []byte("{\"id\":1,\"error\":{\"code\":1,\"message\":null}}\n")},
		{"trailing json", []byte("{\"method\":\"x\"}{}\n")},
		{"empty line", []byte("\n")},
		{"truncated line", []byte("{\"method\":\"x\"}")},
		{"unknown response", []byte("{\"id\":99,\"result\":{}}\n")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := runWire(t, tc.input); err == nil || errors.Is(err, io.EOF) {
				t.Fatalf("expected protocol error before EOF, got %v", err)
			}
		})
	}
}

func TestWireIDKeyCanonicalizesEscapesWithFixedSize(t *testing.T) {
	plain, err := wireIDKey(json.RawMessage(`"a"`))
	if err != nil {
		t.Fatal(err)
	}
	escaped, err := wireIDKey(json.RawMessage(`"\u0061"`))
	if err != nil {
		t.Fatal(err)
	}
	if plain != escaped {
		t.Fatalf("equivalent JSON string IDs produced different keys")
	}
	longRaw := json.RawMessage(`"` + strings.Repeat("a", MaxJSONLLineBytes-2) + `"`)
	longKey, err := wireIDKey(longRaw)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(longKey), 34; got != want {
		t.Fatalf("string ID key length=%d want=%d", got, want)
	}
}

func TestRPCAcceptsNotificationTimestampAndCopiesParams(t *testing.T) {
	input := []byte("{\"method\":\"event\",\"params\":{\"value\":1},\"emittedAtMs\":42}\n")
	rpc := NewRPCClient(bytes.NewReader(input), io.Discard)
	done := make(chan error, 1)
	go func() { done <- rpc.Run(context.Background()) }()
	notification := <-rpc.Events()
	if notification.Method != "event" || notification.EmittedAtMs == nil || *notification.EmittedAtMs != 42 {
		t.Fatalf("notification=%#v", notification)
	}
	input[0] = 'x'
	if string(notification.Params) != `{"value":1}` {
		t.Fatalf("params=%s", notification.Params)
	}
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatalf("run err=%v", err)
	}
}

func TestRPCAcceptsSchemaDefinedTraceOnNumericServerRequest(t *testing.T) {
	serverReads, serverWrites := io.Pipe()
	output := newLineCaptureWriter()
	rpc := NewRPCClient(serverReads, output)
	done := make(chan error, 1)
	go func() { done <- rpc.Run(context.Background()) }()
	if _, err := io.WriteString(serverWrites, "{\"id\":7,\"method\":\"unknown\",\"trace\":{\"traceparent\":\"00-a-b-01\",\"tracestate\":null}}\n"); err != nil {
		t.Fatal(err)
	}
	<-output.wrote
	_ = serverWrites.Close()
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatalf("run err=%v", err)
	}
	if got := strings.TrimSpace(output.String()); got != `{"id":7,"error":{"code":-32090,"message":"agent_romm does not service App Server input requests"}}` {
		t.Fatalf("response=%s", got)
	}
}

type lineCaptureWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	wrote chan struct{}
	once  sync.Once
}

func newLineCaptureWriter() *lineCaptureWriter {
	return &lineCaptureWriter{wrote: make(chan struct{})}
}

func (w *lineCaptureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buf.Write(p)
	complete := bytes.Contains(w.buf.Bytes(), []byte{'\n'})
	w.mu.Unlock()
	if complete {
		w.once.Do(func() { close(w.wrote) })
	}
	return n, err
}

func (w *lineCaptureWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

type shortWriter struct {
	mu       sync.Mutex
	maxChunk int
	buf      bytes.Buffer
	zero     bool
}

func (w *shortWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.zero {
		return 0, nil
	}
	if len(p) > w.maxChunk {
		p = p[:w.maxChunk]
	}
	return w.buf.Write(p)
}

func (w *shortWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestRPCWriteLoopHandlesZeroAndShortWrites(t *testing.T) {
	zero := &shortWriter{zero: true}
	rpc := NewRPCClient(bytes.NewReader(nil), zero)
	err := rpc.Notify(context.Background(), "event", map[string]int{"x": 1})
	var callErr *CallError
	if !errors.As(err, &callErr) || callErr.BytesWritten != 0 || !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("zero write err=%v", err)
	}

	short := &shortWriter{maxChunk: 2}
	rpc = NewRPCClient(bytes.NewReader(nil), short)
	if err := rpc.Notify(context.Background(), "event", map[string]int{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if got := short.String(); got != "{\"method\":\"event\",\"params\":{\"x\":1}}\n" {
		t.Fatalf("wire=%q", got)
	}
}

func TestRPCConcurrentCallsNeverInterleave(t *testing.T) {
	writer := &shortWriter{maxChunk: 3}
	rpc := NewRPCClient(bytes.NewReader(nil), writer)
	const calls = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			_ = rpc.Call(ctx, "call", map[string]int{"n": i}, nil)
		}(i)
	}
	close(start)
	wg.Wait()
	lines := strings.Split(strings.TrimSpace(writer.String()), "\n")
	if len(lines) != calls {
		t.Fatalf("lines=%d want=%d\n%s", len(lines), calls, writer.String())
	}
	seen := make(map[int64]bool)
	for _, line := range lines {
		var request struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			t.Fatalf("interleaved line %q: %v", line, err)
		}
		if seen[request.ID] {
			t.Fatalf("duplicate id %d", request.ID)
		}
		seen[request.ID] = true
	}
}

func TestRPCConcurrentCallsNotificationsAndReverseResponsesNeverInterleave(t *testing.T) {
	serverReads, serverWrites := io.Pipe()
	writer := &shortWriter{maxChunk: 2}
	rpc := NewRPCClient(serverReads, writer)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- rpc.Run(ctx) }()
	go func() {
		_, _ = io.WriteString(serverWrites, `{"id":"server-1","method":"unknown","params":{}}`+"\n")
	}()

	const calls = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			<-start
			callCtx, callCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer callCancel()
			_ = rpc.Call(callCtx, "call", map[string]int{"n": i}, nil)
		}(i)
		go func(i int) {
			defer wg.Done()
			<-start
			if err := rpc.Notify(context.Background(), "note", map[string]int{"n": i}); err != nil {
				t.Errorf("Notify: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	deadline := time.Now().Add(time.Second)
	for strings.Count(writer.String(), "\n") != calls*2+1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	_ = serverWrites.Close()
	<-runDone
	lines := strings.Split(strings.TrimSpace(writer.String()), "\n")
	if len(lines) != calls*2+1 {
		t.Fatalf("lines=%d want=%d\n%s", len(lines), calls*2+1, writer.String())
	}
	for _, line := range lines {
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &object); err != nil {
			t.Fatalf("interleaved line %q: %v", line, err)
		}
		if _, hasMethod := object["method"]; !hasMethod {
			if string(object["id"]) != `"server-1"` {
				t.Fatalf("unexpected response line %s", line)
			}
		}
	}
}

type gateWriter struct {
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	buf     bytes.Buffer
}

type closableGateWriter struct {
	entered   chan struct{}
	release   chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	closed    bool
	writes    int
	buf       bytes.Buffer
}

func newClosableGateWriter() *closableGateWriter {
	return &closableGateWriter{entered: make(chan struct{}), release: make(chan struct{})}
}

func (w *closableGateWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	w.mu.Unlock()
	select {
	case <-w.entered:
	default:
		close(w.entered)
	}
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	return w.buf.Write(p)
}

func (w *closableGateWriter) Close() error {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	w.closeOnce.Do(func() { close(w.release) })
	return nil
}

func (w *closableGateWriter) releaseWrite() { w.closeOnce.Do(func() { close(w.release) }) }

func (w *closableGateWriter) writeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes
}

func waitForPendingCount(t *testing.T, rpc *RPCClient, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rpc.stateMu.Lock()
		got := len(rpc.pending)
		rpc.stateMu.Unlock()
		if got == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("pending count never reached %d", count)
}

func waitForQuarantinedResponse(t *testing.T, rpc *RPCClient, key string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rpc.stateMu.Lock()
		pending := rpc.pending[key]
		observed := pending != nil && pending.quarantinedResponse != nil
		rpc.stateMu.Unlock()
		if observed {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("response for %s was not isolated before write outcome", key)
}

func TestRPCPreResponseWhileWaitingForWriterGateIsImmediatelyFatal(t *testing.T) {
	serverReads, serverWrites := io.Pipe()
	writer := newClosableGateWriter()
	rpc := NewRPCClient(serverReads, writer)
	runCtx, stopRun := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- rpc.Run(runCtx) }()

	firstDone := make(chan error, 1)
	go func() { firstDone <- rpc.Call(context.Background(), "first", nil, nil) }()
	<-writer.entered

	secondDone := make(chan error, 1)
	go func() { secondDone <- rpc.Call(context.Background(), "second", nil, &struct{}{}) }()
	waitForPendingCount(t, rpc, 2)
	if _, err := io.WriteString(serverWrites, `{"id":2,"result":{}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-runDone:
		if !errors.Is(err, ErrUnknownResponseID) {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(100 * time.Millisecond):
		_ = writer.Close()
		stopRun()
		_ = serverWrites.Close()
		<-runDone
		<-firstDone
		<-secondDone
		t.Fatal("pre-response did not terminate while second call waited for the writer gate")
	}
	_ = serverWrites.Close()
	stopRun()
	for i, done := range []<-chan error{firstDone, secondDone} {
		err := <-done
		var callErr *CallError
		if !errors.As(err, &callErr) || callErr.BytesWritten != 0 || !errors.Is(err, ErrUnknownResponseID) {
			t.Fatalf("call %d err=%v", i+1, err)
		}
	}
	if got := writer.writeCount(); got != 1 {
		t.Fatalf("direct writer was entered %d times; waiting call must never write", got)
	}
}

func TestRPCRejectedPreWriteResponsePreventsWriteBeforeRunPublishesTerminal(t *testing.T) {
	rpc := NewRPCClient(bytes.NewReader(nil), io.Discard)
	_, key, pending, err := rpc.registerPending()
	if err != nil {
		t.Fatal(err)
	}
	response := wireMessage{idKey: key, result: json.RawMessage(`{}`)}
	if err := rpc.deliverResponse(response); !errors.Is(err, ErrUnknownResponseID) {
		t.Fatalf("deliverResponse err=%v", err)
	}
	if err := rpc.beginPendingWrite(key, pending); !errors.Is(err, ErrUnknownResponseID) {
		t.Fatalf("beginPendingWrite err=%v", err)
	}
}

type blockingJSONMarshaler struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	result  []byte
	err     error
}

func (m *blockingJSONMarshaler) MarshalJSON() ([]byte, error) {
	close(m.entered)
	<-m.release
	return m.result, m.err
}

func (m *blockingJSONMarshaler) unblock() { m.once.Do(func() { close(m.release) }) }

func TestRPCPreResponseDuringMarshalRemainsFatalAfterLocalMarshalExit(t *testing.T) {
	oversize := make([]byte, MaxJSONLLineBytes+1)
	oversize[0] = '"'
	for i := 1; i < len(oversize)-1; i++ {
		oversize[i] = 'a'
	}
	oversize[len(oversize)-1] = '"'
	for _, tc := range []struct {
		name   string
		result []byte
		err    error
	}{
		{"marshal error", nil, errors.New("marshal failed")},
		{"oversize result", oversize, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverReads, serverWrites := io.Pipe()
			rpc := NewRPCClient(serverReads, io.Discard)
			runCtx, cancelRun := context.WithCancel(context.Background())
			runDone := make(chan error, 1)
			go func() { runDone <- rpc.Run(runCtx) }()
			marshaler := &blockingJSONMarshaler{
				entered: make(chan struct{}), release: make(chan struct{}), result: tc.result, err: tc.err,
			}
			callDone := make(chan error, 1)
			go func() { callDone <- rpc.Call(context.Background(), "blocked", marshaler, nil) }()
			<-marshaler.entered
			if _, err := io.WriteString(serverWrites, `{"id":1,"result":{}}`+"\n"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-runDone:
				if !errors.Is(err, ErrUnknownResponseID) {
					t.Fatalf("Run err=%v", err)
				}
			case <-time.After(100 * time.Millisecond):
				marshaler.unblock()
				cancelRun()
				_ = serverWrites.Close()
				<-callDone
				<-runDone
				t.Fatal("pre-write response was quarantined during marshaling")
			}
			marshaler.unblock()
			_ = serverWrites.Close()
			cancelRun()
			err := <-callDone
			var callErr *CallError
			if !errors.As(err, &callErr) || callErr.BytesWritten != 0 || !errors.Is(err, ErrUnknownResponseID) {
				t.Fatalf("Call err=%v", err)
			}
		})
	}
}

type responseRaceWriter struct {
	entered chan struct{}
	release chan struct{}
	n       int
	err     error
}

func (w *responseRaceWriter) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	if w.n < 0 || w.n > len(p) {
		return len(p), w.err
	}
	return w.n, w.err
}

func TestRPCPreResponseIsDeliveredAfterPartialOrFullWrite(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		err  error
	}{
		{"partial", 1, io.ErrUnexpectedEOF},
		{"full", -1, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverReads, serverWrites := io.Pipe()
			writer := &responseRaceWriter{entered: make(chan struct{}), release: make(chan struct{}), n: tc.n, err: tc.err}
			rpc := NewRPCClient(serverReads, writer)
			runCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runDone := make(chan error, 1)
			go func() { runDone <- rpc.Run(runCtx) }()
			callDone := make(chan error, 1)
			go func() { callDone <- rpc.Call(context.Background(), "call", nil, &struct{}{}) }()
			<-writer.entered
			if _, err := io.WriteString(serverWrites, `{"id":1,"result":{}}`+"\n"); err != nil {
				t.Fatal(err)
			}
			waitForQuarantinedResponse(t, rpc, "n:1")
			close(writer.release)
			if err := <-callDone; err != nil {
				t.Fatalf("call err=%v", err)
			}
			cancel()
			_ = serverWrites.Close()
			<-runDone
		})
	}
}

func TestRPCInWriteResponseWithZeroAcceptedBytesIsFatal(t *testing.T) {
	serverReads, serverWrites := io.Pipe()
	writer := &responseRaceWriter{
		entered: make(chan struct{}), release: make(chan struct{}), n: 0, err: io.ErrNoProgress,
	}
	rpc := NewRPCClient(serverReads, writer)
	runDone := make(chan error, 1)
	go func() { runDone <- rpc.Run(context.Background()) }()
	callDone := make(chan error, 1)
	go func() { callDone <- rpc.Call(context.Background(), "call", nil, &struct{}{}) }()
	<-writer.entered
	if _, err := io.WriteString(serverWrites, `{"id":1,"result":{}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	waitForQuarantinedResponse(t, rpc, "n:1")
	close(writer.release)
	err := <-callDone
	var callErr *CallError
	if !errors.As(err, &callErr) || callErr.BytesWritten != 0 || !errors.Is(err, ErrUnknownResponseID) {
		t.Fatalf("Call err=%v", err)
	}
	if err := <-runDone; !errors.Is(err, ErrUnknownResponseID) {
		t.Fatalf("Run err=%v", err)
	}
	_ = serverWrites.Close()
}

func (w *gateWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	first := w.buf.Len() == 0
	w.mu.Unlock()
	if first {
		select {
		case <-w.entered:
		default:
			close(w.entered)
		}
		<-w.release
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func TestRPCCancelWhileWaitingForWriterWritesZeroBytes(t *testing.T) {
	writer := &gateWriter{entered: make(chan struct{}), release: make(chan struct{})}
	rpc := NewRPCClient(bytes.NewReader(nil), writer)
	firstCtx, firstCancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- rpc.Call(firstCtx, "first", nil, nil) }()
	<-writer.entered

	secondCtx, secondCancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() { secondDone <- rpc.Call(secondCtx, "second", nil, nil) }()
	secondCancel()
	var callErr *CallError
	if err := <-secondDone; !errors.As(err, &callErr) || callErr.BytesWritten != 0 {
		t.Fatalf("second err=%v", err)
	}
	firstCancel()
	close(writer.release)
	<-firstDone
	if strings.Contains(writer.buf.String(), `"second"`) {
		t.Fatalf("second request was written: %q", writer.buf.String())
	}
}

func TestRPCPartialWriteCancellationReportsExactBytes(t *testing.T) {
	writer := &partialBlockingWriter{wrote: make(chan struct{}), release: make(chan struct{})}
	rpc := NewRPCClient(bytes.NewReader(nil), writer)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rpc.Call(ctx, "call", nil, nil) }()
	<-writer.wrote
	cancel()
	close(writer.release)
	err := <-done
	var callErr *CallError
	if !errors.As(err, &callErr) || callErr.BytesWritten != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestRPCTimeoutAfterCompleteWriteReportsJSONAndNewlineBytes(t *testing.T) {
	writer := &shortWriter{maxChunk: 1024}
	rpc := NewRPCClient(bytes.NewReader(nil), writer)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	err := rpc.Call(ctx, "call", map[string]int{"x": 1}, nil)
	var callErr *CallError
	wantWire := "{\"id\":1,\"method\":\"call\",\"params\":{\"x\":1}}\n"
	if !errors.As(err, &callErr) || callErr.BytesWritten != len(wantWire) {
		t.Fatalf("err=%v bytes=%d want=%d", err, callErr.BytesWritten, len(wantWire))
	}
	if writer.String() != wantWire {
		t.Fatalf("wire=%q want=%q", writer.String(), wantWire)
	}
}

func TestRPCEOFCompletesAllPendingCalls(t *testing.T) {
	writer := &shortWriter{maxChunk: 1024}
	rpc := NewRPCClient(bytes.NewReader(nil), writer)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- rpc.Call(ctx, "call", nil, nil) }()
	}
	deadline := time.Now().Add(time.Second)
	for strings.Count(writer.String(), "\n") != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := rpc.Run(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("run err=%v", err)
	}
	for i := 0; i < 2; i++ {
		err := <-results
		var callErr *CallError
		if !errors.As(err, &callErr) || callErr.BytesWritten == 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("call err=%v", err)
		}
	}
}

func TestRPCRunCancellationClosesAClosableBlockedReader(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	rpc := NewRPCClient(reader, io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rpc.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run err=%v", err)
		}
	case <-time.After(100 * time.Millisecond):
		_ = reader.Close()
		t.Fatal("Run did not unblock after context cancellation")
	}
}

type sharedClosingTransport struct {
	mu     sync.Mutex
	closes int
}

func (*sharedClosingTransport) Read([]byte) (int, error)    { return 0, io.EOF }
func (*sharedClosingTransport) Write(p []byte) (int, error) { return len(p), nil }

func (t *sharedClosingTransport) Close() error {
	t.mu.Lock()
	t.closes++
	t.mu.Unlock()
	return nil
}

func (t *sharedClosingTransport) closeCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closes
}

func TestRPCClosesSharedReaderWriterOnce(t *testing.T) {
	transport := &sharedClosingTransport{}
	rpc := NewRPCClient(transport, transport)
	if err := rpc.Run(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Run err=%v", err)
	}
	if got := transport.closeCount(); got != 1 {
		t.Fatalf("shared transport closed %d times", got)
	}
}

func TestRPCReverseRequestsAreAlwaysRejectedSafely(t *testing.T) {
	methods := []struct {
		method string
		want   string
	}{
		{"item/commandExecution/requestApproval", `{"id":"id-1","result":{"decision":"cancel"}}`},
		{"item/fileChange/requestApproval", `{"id":"id-1","result":{"decision":"cancel"}}`},
		{"item/tool/requestUserInput", `{"id":"id-1","error":{"code":-32090,"message":"agent_romm does not service App Server input requests"}}`},
		{"item/permissions/requestApproval", `{"id":"id-1","error":{"code":-32090,"message":"agent_romm does not service App Server input requests"}}`},
		{"mcpServer/elicitation/request", `{"id":"id-1","error":{"code":-32090,"message":"agent_romm does not service App Server input requests"}}`},
		{"item/tool/call", `{"id":"id-1","error":{"code":-32090,"message":"agent_romm does not service App Server input requests"}}`},
		{"unknown", `{"id":"id-1","error":{"code":-32090,"message":"agent_romm does not service App Server input requests"}}`},
	}
	for _, tc := range methods {
		t.Run(tc.method, func(t *testing.T) {
			serverReads, serverWrites := io.Pipe()
			output := newLineCaptureWriter()
			rpc := NewRPCClient(serverReads, output)
			seen := make(chan ServerRequest, 1)
			if err := rpc.SetServerRequestCallback(func(_ context.Context, request ServerRequest) { seen <- request }); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- rpc.Run(context.Background()) }()
			if _, err := io.WriteString(serverWrites, `{"id":"id-1","method":"`+tc.method+`","params":{}}`+"\n"); err != nil {
				t.Fatal(err)
			}
			<-output.wrote
			_ = serverWrites.Close()
			if err := <-done; !errors.Is(err, io.EOF) {
				t.Fatalf("run err=%v", err)
			}
			if got := strings.TrimSpace(output.String()); got != tc.want {
				t.Fatalf("response=%s want=%s", got, tc.want)
			}
			if request := <-seen; request.Method != tc.method {
				t.Fatalf("request=%#v", request)
			}
		})
	}
}

func TestRPCReverseRequestCallbackMutationCannotChangeObservationOrResponseID(t *testing.T) {
	serverReads, serverWrites := io.Pipe()
	output := newLineCaptureWriter()
	rpc := NewRPCClient(serverReads, output)
	callbackDone := make(chan struct{})
	if err := rpc.SetServerRequestCallback(func(_ context.Context, request ServerRequest) {
		request.ID[1] = 'X'
		request.Params[10] = 'X'
		request.Trace[2] = 'X'
		close(callbackDone)
	}); err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- rpc.Run(context.Background()) }()
	input := `{"id":"request-1","method":"unknown","params":{"value":"safe"},"trace":{"traceparent":"safe"}}` + "\n"
	if _, err := io.WriteString(serverWrites, input); err != nil {
		t.Fatal(err)
	}
	observation := <-rpc.Events()
	<-callbackDone
	<-output.wrote
	if string(observation.Params) != `{"value":"safe"}` {
		t.Fatalf("observation params=%s", observation.Params)
	}
	want := `{"id":"request-1","error":{"code":-32090,"message":"agent_romm does not service App Server input requests"}}`
	if got := strings.TrimSpace(output.String()); got != want {
		t.Fatalf("response=%s want=%s", got, want)
	}
	_ = serverWrites.Close()
	if err := <-runDone; !errors.Is(err, io.EOF) {
		t.Fatalf("Run err=%v", err)
	}
}

func TestRPCReverseObservationAndCallbackParamsHaveIndependentBacking(t *testing.T) {
	serverReads, serverWrites := io.Pipe()
	output := newLineCaptureWriter()
	rpc := NewRPCClient(serverReads, output)
	ready := make(chan struct{})
	start := make(chan struct{})
	callbackByte := make(chan byte, 1)
	if err := rpc.SetServerRequestCallback(func(_ context.Context, request ServerRequest) {
		close(ready)
		<-start
		request.Params[10] = 'H'
		callbackByte <- request.Params[10]
	}); err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- rpc.Run(context.Background()) }()
	if _, err := io.WriteString(serverWrites, `{"id":1,"method":"unknown","params":{"value":"safe"}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	observation := <-rpc.Events()
	<-ready
	close(start)
	observation.Params[10] = 'E'
	if got := <-callbackByte; got != 'H' || observation.Params[10] != 'E' {
		t.Fatalf("callback byte=%q observation byte=%q", got, observation.Params[10])
	}
	<-output.wrote
	_ = serverWrites.Close()
	if err := <-runDone; !errors.Is(err, io.EOF) {
		t.Fatalf("Run err=%v", err)
	}
}

func TestRPCFirstReverseObservationIsPublishedBeforeCallback(t *testing.T) {
	serverReads, serverWrites := io.Pipe()
	rpc := NewRPCClient(serverReads, io.Discard)
	callbackResult := make(chan error, 1)
	if err := rpc.SetServerRequestCallback(func(_ context.Context, _ ServerRequest) {
		select {
		case observation := <-rpc.Events():
			if !observation.ServerRequest || observation.Method != "unknown" {
				callbackResult <- errors.New("callback saw the wrong observation")
				return
			}
			callbackResult <- nil
		default:
			callbackResult <- errors.New("callback ran before observation publication")
		}
	}); err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- rpc.Run(context.Background()) }()
	if _, err := io.WriteString(serverWrites, `{"id":1,"method":"unknown"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	if err := <-callbackResult; err != nil {
		t.Fatal(err)
	}
	_ = serverWrites.Close()
	if err := <-runDone; !errors.Is(err, io.EOF) {
		t.Fatalf("Run err=%v", err)
	}
}

func TestRPCDuplicateActiveServerRequestIDIsFatal(t *testing.T) {
	input := strings.NewReader("{\"id\":1,\"method\":\"one\"}\n{\"id\":1,\"method\":\"two\"}\n")
	rpc := NewRPCClient(input, io.Discard)
	entered := make(chan struct{})
	release := make(chan struct{})
	if err := rpc.SetServerRequestCallback(func(ctx context.Context, _ ServerRequest) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
	}); err != nil {
		t.Fatal(err)
	}
	err := rpc.Run(context.Background())
	close(release)
	if !errors.Is(err, ErrDuplicateServerRequestID) {
		t.Fatalf("err=%v", err)
	}
}

func TestRPCEOFFailsBeforeWaitingForCallback(t *testing.T) {
	serverReads, serverWrites := io.Pipe()
	rpc := NewRPCClient(serverReads, io.Discard)
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	if err := rpc.SetServerRequestCallback(func(ctx context.Context, _ ServerRequest) {
		close(entered)
		select {
		case <-ctx.Done():
		case <-release:
		}
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- rpc.Run(context.Background()) }()
	if _, err := io.WriteString(serverWrites, "{\"id\":1,\"method\":\"unknown\"}\n"); err != nil {
		t.Fatal(err)
	}
	<-entered
	_ = serverWrites.Close()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Run waited for callback before publishing EOF")
	}
}

func TestRPCBusyReverseRequestFailsWithoutBlockingReadLoop(t *testing.T) {
	serverReads, serverWrites := io.Pipe()
	writer := newCloseBlockingWriter()
	rpc := NewRPCClient(serverReads, writer)
	callbacks := make(chan ServerRequest, 2)
	if err := rpc.SetServerRequestCallback(func(_ context.Context, request ServerRequest) {
		callbacks <- request
	}); err != nil {
		t.Fatal(err)
	}
	eventsDone := make(chan []Notification, 1)
	go func() {
		var events []Notification
		for event := range rpc.Events() {
			events = append(events, event)
		}
		eventsDone <- events
	}()
	runDone := make(chan error, 1)
	go func() { runDone <- rpc.Run(context.Background()) }()
	if _, err := io.WriteString(serverWrites, `{"id":1,"method":"unknown"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	<-writer.entered
	callDone := make(chan error, 1)
	go func() { callDone <- rpc.Call(context.Background(), "call", nil, nil) }()
	waitForPendingCount(t, rpc, 1)
	if _, err := io.WriteString(serverWrites, `{"id":2,"method":"unknown"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	_ = serverWrites.Close()
	var terminal error
	select {
	case terminal = <-runDone:
		if !errors.Is(terminal, ErrServerRequestBusy) {
			t.Fatalf("Run terminal=%v", terminal)
		}
	case <-time.After(100 * time.Millisecond):
		_ = writer.Close()
		<-runDone
		<-callDone
		<-eventsDone
		t.Fatal("second reverse request blocked the read loop from reaching terminal state")
	}
	callErr := <-callDone
	var typed *CallError
	if !errors.As(callErr, &typed) || typed.BytesWritten != 0 || !errors.Is(callErr, terminal) {
		t.Fatalf("pending call err=%v terminal=%v", callErr, terminal)
	}
	if got := len(callbacks); got != 1 {
		t.Fatalf("callbacks=%d want=1", got)
	}
	events := <-eventsDone
	if len(events) != 1 || !events[0].ServerRequest {
		t.Fatalf("events=%#v", events)
	}
	select {
	case <-rpc.Done():
	default:
		t.Fatal("Done was not closed")
	}
	if writer.closeCount() != 1 {
		t.Fatalf("writer closed %d times", writer.closeCount())
	}
}

func TestRPCCanceledContextAfterReverseAdmissionSkipsCallback(t *testing.T) {
	rpc := NewRPCClient(bytes.NewReader(nil), io.Discard)
	called := make(chan struct{}, 1)
	if err := rpc.SetServerRequestCallback(func(context.Context, ServerRequest) {
		called <- struct{}{}
	}); err != nil {
		t.Fatal(err)
	}
	message := wireMessage{
		id: json.RawMessage(`1`), idKey: "n:1", method: "unknown", params: json.RawMessage(`{}`),
	}
	if err := rpc.admitServerRequest(context.Background(), message.idKey); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := rpc.startServerRequest(ctx, prepareServerRequest(message)); !errors.Is(err, context.Canceled) {
		t.Fatalf("startServerRequest err=%v", err)
	}
	select {
	case <-called:
		t.Fatal("callback ran after admitted context was canceled")
	default:
	}
	if len(rpc.serverSlots) != 0 {
		t.Fatal("server slot was not released")
	}
	rpc.stateMu.Lock()
	_, active := rpc.serverActive[message.idKey]
	rpc.stateMu.Unlock()
	if active {
		t.Fatal("server request ID remained active")
	}
}

func TestRPCReverseAdmissionAfterTerminalCannotLaunchCallback(t *testing.T) {
	rpc := NewRPCClient(bytes.NewReader(nil), io.Discard)
	called := make(chan struct{}, 1)
	if err := rpc.SetServerRequestCallback(func(context.Context, ServerRequest) {
		called <- struct{}{}
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4096; i++ {
		if _, _, _, err := rpc.registerPending(); err != nil {
			t.Fatal(err)
		}
	}
	terminal := errors.New("terminal before reverse admission")
	failDone := make(chan struct{})
	go func() {
		rpc.failAll(terminal)
		close(failDone)
	}()
	<-rpc.terminalCh

	message := wireMessage{
		id: json.RawMessage(`"request-1"`), idKey: "s:request-1", method: "unknown", params: json.RawMessage(`{}`),
	}
	admitErr := rpc.admitServerRequest(context.Background(), message.idKey)
	if admitErr == nil {
		_ = rpc.startServerRequest(context.Background(), prepareServerRequest(message))
	}
	<-failDone
	rpc.handlers.Wait()
	if !errors.Is(admitErr, terminal) {
		t.Errorf("admit error=%v want terminal error", admitErr)
	}
	if got := len(called); got != 0 {
		t.Errorf("callbacks=%d want=0", got)
	}
	if len(rpc.serverSlots) != 0 {
		t.Fatal("server slot remained claimed")
	}
	rpc.stateMu.Lock()
	active := len(rpc.serverActive)
	rpc.stateMu.Unlock()
	if active != 0 {
		t.Fatalf("active reverse IDs=%d want=0", active)
	}
}

type closeBlockingWriter struct {
	entered   chan struct{}
	closed    chan struct{}
	enterOnce sync.Once
	closeOnce sync.Once
	mu        sync.Mutex
	closes    int
}

type rejectingCountingCloser struct {
	mu     sync.Mutex
	writes int
	bytes  int
	closes int
}

func (w *rejectingCountingCloser) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	w.bytes += len(p)
	w.mu.Unlock()
	return 0, errors.New("unexpected direct write")
}

func (w *rejectingCountingCloser) Close() error {
	w.mu.Lock()
	w.closes++
	w.mu.Unlock()
	return nil
}

func (w *rejectingCountingCloser) counts() (writes, bytes, closes int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes, w.bytes, w.closes
}

func newCloseBlockingWriter() *closeBlockingWriter {
	return &closeBlockingWriter{entered: make(chan struct{}), closed: make(chan struct{})}
}

func (w *closeBlockingWriter) Write([]byte) (int, error) {
	w.enterOnce.Do(func() { close(w.entered) })
	<-w.closed
	return 0, io.ErrClosedPipe
}

func (w *closeBlockingWriter) Close() error {
	w.mu.Lock()
	w.closes++
	w.mu.Unlock()
	w.closeOnce.Do(func() { close(w.closed) })
	return nil
}

func (w *closeBlockingWriter) closeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closes
}

func TestRPCEOFClosesBlockedReverseResponseWriterBeforeWaiting(t *testing.T) {
	writer := newCloseBlockingWriter()
	serverReads, serverWrites := io.Pipe()
	rpc := NewRPCClient(serverReads, writer)
	done := make(chan error, 1)
	go func() { done <- rpc.Run(context.Background()) }()
	if _, err := io.WriteString(serverWrites, "{\"id\":1,\"method\":\"unknown\"}\n"); err != nil {
		t.Fatal(err)
	}
	<-writer.entered
	_ = serverWrites.Close()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(100 * time.Millisecond):
		_ = writer.Close()
		t.Fatal("Run did not close a blocked reverse response writer")
	}
	if writer.closeCount() != 1 {
		t.Fatalf("writer closed %d times", writer.closeCount())
	}
	select {
	case <-rpc.Done():
	default:
		t.Fatal("Done was not closed")
	}
	for range rpc.Events() {
	}
}

func TestRPCOversizedReverseResponseFailsBeforeWriterEntry(t *testing.T) {
	const prefix = `{"id":"`
	const suffix = `","method":"unknown"}`
	request := prefix + strings.Repeat("a", MaxJSONLLineBytes-len(prefix)-len(suffix)) + suffix
	if len(request) != MaxJSONLLineBytes {
		t.Fatalf("request len=%d", len(request))
	}
	serverReads, serverWrites := io.Pipe()
	writer := &rejectingCountingCloser{}
	rpc := NewRPCClient(serverReads, writer)
	runDone := make(chan error, 1)
	go func() { runDone <- rpc.Run(context.Background()) }()
	if _, err := io.WriteString(serverWrites, request+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-runDone:
		if !errors.Is(err, ErrRPCMessageTooLarge) {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(5 * time.Second):
		_ = serverWrites.Close()
		t.Fatal("oversized reverse response did not terminate the session")
	}
	_ = serverWrites.Close()
	writes, bytes, closes := writer.counts()
	if writes != 0 || bytes != 0 || closes != 1 {
		t.Fatalf("writer writes=%d bytes=%d closes=%d", writes, bytes, closes)
	}
	select {
	case <-rpc.Done():
	default:
		t.Fatal("Done was not closed")
	}
}

func TestMarshalServerResponseAcceptsExactBoundary(t *testing.T) {
	const prefix = `{"id":"`
	const suffix = `","error":{"code":-32090,"message":"agent_romm does not service App Server input requests"}}`
	idContent := strings.Repeat("a", MaxJSONLLineBytes-len(prefix)-len(suffix))
	rpcErr := &RPCError{Code: -32090, Message: "agent_romm does not service App Server input requests"}
	response, err := marshalServerResponse(json.RawMessage(`"`+idContent+`"`), nil, rpcErr)
	if err != nil || len(response) != MaxJSONLLineBytes {
		t.Fatalf("response len=%d err=%v", len(response), err)
	}
	if !bytes.HasPrefix(response, []byte(prefix)) || !bytes.HasSuffix(response, []byte(suffix)) {
		t.Fatal("exact-boundary response shape changed")
	}
	response, err = marshalServerResponse(json.RawMessage(`"`+idContent+`a"`), nil, rpcErr)
	if !errors.Is(err, ErrRPCMessageTooLarge) || response != nil {
		t.Fatalf("over-boundary response len=%d err=%v", len(response), err)
	}
}

func TestRPCWriteJSONLRejectsOversizedPayloadBeforeWriterEntry(t *testing.T) {
	writer := &rejectingCountingCloser{}
	rpc := NewRPCClient(bytes.NewReader(nil), writer)
	written, err := rpc.writeJSONL(context.Background(), make([]byte, MaxJSONLLineBytes+1), nil)
	if !errors.Is(err, ErrRPCMessageTooLarge) || written != 0 {
		t.Fatalf("written=%d err=%v", written, err)
	}
	writes, bytes, _ := writer.counts()
	if writes != 0 || bytes != 0 {
		t.Fatalf("writer writes=%d bytes=%d", writes, bytes)
	}
}

func TestRPCCancelClosesBlockedReverseWriterAndFailsPendingCall(t *testing.T) {
	serverReads, serverWrites := io.Pipe()
	writer := newCloseBlockingWriter()
	rpc := NewRPCClient(serverReads, writer)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- rpc.Run(ctx) }()
	go func() { _, _ = io.WriteString(serverWrites, `{"id":1,"method":"unknown"}`+"\n") }()
	<-writer.entered
	callDone := make(chan error, 1)
	go func() { callDone <- rpc.Call(context.Background(), "call", nil, nil) }()
	waitForPendingCount(t, rpc, 1)
	cancel()
	select {
	case err := <-callDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pending call err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending call did not fail")
	}
	select {
	case err := <-runDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(100 * time.Millisecond):
		_ = writer.Close()
		t.Fatal("Run did not converge after cancellation")
	}
	_ = serverWrites.Close()
	if writer.closeCount() != 1 {
		t.Fatalf("writer closed %d times", writer.closeCount())
	}
}

type failWriter struct{ err error }

func (w failWriter) Write([]byte) (int, error) { return 0, w.err }

func TestRPCReverseResponseWriteFailureIsFatal(t *testing.T) {
	want := errors.New("write failed")
	serverReads, serverWrites := io.Pipe()
	defer serverWrites.Close()
	rpc := NewRPCClient(serverReads, failWriter{err: want})
	done := make(chan error, 1)
	go func() { done <- rpc.Run(context.Background()) }()
	if _, err := io.WriteString(serverWrites, "{\"id\":1,\"method\":\"unknown\"}\n"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}

func TestRejectServerRequestResult(t *testing.T) {
	result, rpcErr := RejectServerRequest("item/fileChange/requestApproval")
	if rpcErr != nil || result.(map[string]string)["decision"] != "cancel" {
		t.Fatalf("result=%#v err=%v", result, rpcErr)
	}
	result, rpcErr = RejectServerRequest("item/tool/call")
	if result != nil || rpcErr == nil || rpcErr.Code != -32090 {
		t.Fatalf("result=%#v err=%v", result, rpcErr)
	}
}
