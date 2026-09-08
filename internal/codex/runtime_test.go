package codex

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"agent_romm/internal/room"
)

func TestRuntimeKeepsStableEventsAcrossReplacement(t *testing.T) {
	pm := &fakeProcessManager{}
	supervisor := mustSupervisor(t, pm)
	sleeper := newControlledSleeper()
	runtime := mustRuntime(t, supervisor, sleeper)
	events := runtime.Events()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	if err := pm.server(0).notify("item/agentMessage/delta", map[string]any{
		"threadId": "thread-1", "turnId": "turn-1", "itemId": "item-1", "delta": "first",
	}); err != nil {
		t.Fatal(err)
	}
	first := receiveEvent(t, events)
	if first.Kind != "item-delta" || first.Delta != "first" {
		t.Fatalf("first event=%+v", first)
	}

	pm.server(0).crash(errors.New("crash secret"))
	unavailable := receiveEvent(t, events)
	if unavailable.Kind != "runtime-unavailable" || unavailable.Error == nil {
		t.Fatalf("unavailable=%+v", unavailable)
	}
	if runtime.Events() != events {
		t.Fatal("Events channel identity changed while unavailable")
	}
	if delay := sleeper.nextDelay(t); delay != 250*time.Millisecond {
		t.Fatalf("first restart delay=%v", delay)
	}
	sleeper.release()

	ready := receiveEvent(t, events)
	if ready.Kind != "runtime-ready" {
		t.Fatalf("ready=%+v", ready)
	}
	if runtime.Events() != events {
		t.Fatal("Events channel identity changed after replacement")
	}
	if err := pm.server(1).notify("item/agentMessage/delta", map[string]any{
		"threadId": "thread-1", "turnId": "turn-2", "itemId": "item-2", "delta": "second",
	}); err != nil {
		t.Fatal(err)
	}
	second := receiveEvent(t, events)
	if second.Kind != "item-delta" || second.Delta != "second" {
		t.Fatalf("second event=%+v", second)
	}
}

func TestRuntimeDeliversClaimedEventBeforeHandlingSessionExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := &Runtime{ctx: ctx, events: make(chan room.AgentEvent, 1)}
	adapterEvents := make(chan room.AgentEvent)
	sessionDone := make(chan struct{})
	session := &Session{Adapter: &Adapter{events: adapterEvents}, Done: sessionDone}
	result := make(chan [2]bool, 1)
	go func() {
		failed, stop := runtime.forwardSession(session)
		result <- [2]bool{failed, stop}
	}()

	first := room.AgentEvent{Kind: "item-delta", Delta: "first"}
	second := room.AgentEvent{Kind: "item-delta", Delta: "second"}
	adapterEvents <- first
	claimed := make(chan struct{})
	go func() {
		adapterEvents <- second
		close(claimed)
	}()
	awaitClosed(t, claimed)
	close(sessionDone)
	select {
	case got := <-result:
		t.Fatalf("forwarder returned before delivering claimed event: %v", got)
	case <-time.After(25 * time.Millisecond):
	}

	if got := receiveEvent(t, runtime.events); got.Delta != "first" {
		t.Fatalf("first event=%+v", got)
	}
	if got := receiveEvent(t, runtime.events); got.Delta != "second" {
		t.Fatalf("second event=%+v", got)
	}
	close(adapterEvents)
	select {
	case got := <-result:
		if got != [2]bool{true, false} {
			t.Fatalf("result=%v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forwarder did not finish after adapter events drained")
	}
}

func TestRuntimeSessionExitReturnsBeforeAdapterEOFAndRemovesAdapter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	adapterEvents := make(chan room.AgentEvent)
	sessionDone := make(chan struct{})
	close(sessionDone)
	adapter := &Adapter{events: adapterEvents}
	session := &Session{Adapter: adapter, Done: sessionDone}
	runtime := &Runtime{
		ctx: ctx, events: make(chan room.AgentEvent, 1),
		session: session, adapter: adapter,
	}
	result := make(chan [2]bool, 1)
	go func() {
		failed, stop := runtime.forwardSession(session)
		result <- [2]bool{failed, stop}
	}()

	select {
	case got := <-result:
		if got != [2]bool{true, false} {
			t.Fatalf("result=%v", got)
		}
	case <-time.After(100 * time.Millisecond):
		close(adapterEvents)
		<-result
		t.Fatal("session exit waited indefinitely for Adapter.Events EOF")
	}
	close(adapterEvents)
	if _, err := runtime.snapshotAdapter(); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("adapter remained callable after session exit: %v", err)
	}
}

func TestRuntimeBoundsChildExitDrainWhenStdoutRemainsOpen(t *testing.T) {
	pm := &fakeProcessManager{}
	supervisor := mustSupervisor(t, pm)
	sleeper := newControlledSleeper()
	runtime := mustRuntime(t, supervisor, sleeper)
	drainDeadline := make(chan time.Time, 1)
	drainRequested := make(chan struct{})
	var requestedOnce sync.Once
	runtime.exitDrainAfter = func(time.Duration) <-chan time.Time {
		requestedOnce.Do(func() { close(drainRequested) })
		return drainDeadline
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := pm.server(0).notify("item/agentMessage/delta", map[string]any{
		"threadId": "thread-1", "turnId": "turn-1", "itemId": "item-1", "delta": "final",
	}); err != nil {
		t.Fatal(err)
	}
	pm.server(0).exitKeepingStdoutOpen(errors.New("normal child exit"))
	awaitRuntimeAdapterUnavailable(t, runtime)
	if event := receiveEvent(t, runtime.Events()); event.Kind != "item-delta" || event.Delta != "final" {
		t.Fatalf("final event=%+v", event)
	}
	awaitClosed(t, drainRequested)
	select {
	case event := <-runtime.Events():
		t.Fatalf("event before bounded drain expired: %+v", event)
	default:
	}
	drainDeadline <- time.Now()
	if event := receiveEvent(t, runtime.Events()); event.Kind != "runtime-unavailable" {
		t.Fatalf("event after drain timeout=%+v", event)
	}
	if delay := sleeper.nextDelay(t); delay != 250*time.Millisecond {
		t.Fatalf("restart delay=%v", delay)
	}
	if got := pm.stopCallCount(); got != 1 {
		t.Fatalf("cleanup calls=%d want=1", got)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeNormalChildExitDeliversFinalEventBeforeUnavailable(t *testing.T) {
	pm := &fakeProcessManager{}
	supervisor := mustSupervisor(t, pm)
	sleeper := newControlledSleeper()
	runtime := mustRuntime(t, supervisor, sleeper)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := pm.server(0).notify("item/agentMessage/delta", map[string]any{
		"threadId": "thread-1", "turnId": "turn-1", "itemId": "item-1", "delta": "final",
	}); err != nil {
		t.Fatal(err)
	}
	pm.server(0).crash(errors.New("normal child exit"))
	if event := receiveEvent(t, runtime.Events()); event.Kind != "item-delta" || event.Delta != "final" {
		t.Fatalf("first event=%+v", event)
	}
	if event := receiveEvent(t, runtime.Events()); event.Kind != "runtime-unavailable" {
		t.Fatalf("second event=%+v", event)
	}
	if delay := sleeper.nextDelay(t); delay != 250*time.Millisecond {
		t.Fatalf("restart delay=%v", delay)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func awaitRuntimeAdapterUnavailable(t *testing.T, runtime *Runtime) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := runtime.snapshotAdapter(); errors.Is(err, ErrRuntimeUnavailable) {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("failed adapter remained installed")
		}
	}
}

func TestRuntimeDrainsBufferedAdapterEventsBeforeSessionUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := &Runtime{
		ctx: ctx, events: make(chan room.AgentEvent, 2),
		exitDrainAfter: func(time.Duration) <-chan time.Time { return make(chan time.Time) },
	}
	adapterEvents := make(chan room.AgentEvent, 2)
	adapterEvents <- room.AgentEvent{Kind: "item-delta", Delta: "first"}
	adapterEvents <- room.AgentEvent{Kind: "item-delta", Delta: "second"}
	close(adapterEvents)
	sessionDone := make(chan struct{})
	close(sessionDone)
	session := &Session{Adapter: &Adapter{events: adapterEvents}, Done: sessionDone}

	failed, stop := runtime.forwardSession(session)
	if !failed || stop {
		t.Fatalf("failed=%v stop=%v", failed, stop)
	}
	if !runtime.drainFailedSession(session) {
		t.Fatal("bounded failed-session drain was canceled")
	}
	for index, want := range []string{"first", "second"} {
		select {
		case got := <-runtime.events:
			if got.Delta != want {
				t.Fatalf("event %d=%+v want delta %q", index, got, want)
			}
		default:
			t.Fatalf("event %d with delta %q was abandoned", index, want)
		}
	}
}

func TestRuntimeRestartBackoffStartsAt250MillisecondsAndCapsAt10Seconds(t *testing.T) {
	startErrors := make(map[int]error)
	for call := 2; call <= 8; call++ {
		startErrors[call] = errors.New("start secret")
	}
	pm := &fakeProcessManager{startErrors: startErrors}
	supervisor := mustSupervisor(t, pm)
	sleeper := &recordingSleeper{recorded: make(chan time.Duration, 16)}
	runtime := mustRuntime(t, supervisor, sleeper)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	pm.server(0).crash(errors.New("boom"))
	if event := receiveEvent(t, runtime.Events()); event.Kind != "runtime-unavailable" {
		t.Fatalf("event=%+v", event)
	}
	want := []time.Duration{
		250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second,
		4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second,
	}
	got := make([]time.Duration, 0, len(want))
	for range want {
		select {
		case delay := <-sleeper.recorded:
			got = append(got, delay)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after delays %v", got)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delays=%v want=%v", got, want)
	}
	if event := receiveEvent(t, runtime.Events()); event.Kind != "runtime-ready" {
		t.Fatalf("event=%+v", event)
	}
}

func TestRuntimeDoesNotStartReplacementWhenStopFails(t *testing.T) {
	pm := &fakeProcessManager{stopErrors: []error{errors.New("cleanup secret")}}
	supervisor := mustSupervisor(t, pm)
	sleeper := &recordingSleeper{recorded: make(chan time.Duration, 4), block: true}
	runtime := mustRuntime(t, supervisor, sleeper)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	pm.server(0).crash(errors.New("boom"))
	if event := receiveEvent(t, runtime.Events()); event.Kind != "runtime-unavailable" {
		t.Fatalf("event=%+v", event)
	}
	select {
	case delay := <-sleeper.recorded:
		if delay != 250*time.Millisecond {
			t.Fatalf("delay=%v", delay)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not enter cleanup-failure backoff")
	}
	time.Sleep(20 * time.Millisecond)
	if got := pm.startCallCount(); got != 1 {
		t.Fatalf("start calls=%d want=1", got)
	}
	if err := runtime.Close(context.Background()); !errors.Is(err, ErrProcessCleanup) {
		t.Fatalf("Close err=%v", err)
	}
}

func TestRuntimeRetriesStoppedCheckpointBeforeReplacement(t *testing.T) {
	pm := &fakeProcessManager{}
	supervisor := mustSupervisor(t, pm)
	sleeper := newControlledSleeper()
	var mu sync.Mutex
	stoppedAttempts := 0
	sink := func(_ context.Context, checkpoint room.RuntimeCheckpoint) error {
		mu.Lock()
		defer mu.Unlock()
		if checkpoint.State == room.RuntimeProcessStopped {
			stoppedAttempts++
			if stoppedAttempts == 1 {
				return errors.New("temporary checkpoint secret")
			}
		}
		return nil
	}
	runtime, err := NewRuntime(supervisor, RuntimeConfig{ProjectRoot: "/srv/project"}, sink, sleeper)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	pm.server(0).crash(errors.New("boom"))
	if event := receiveEvent(t, runtime.Events()); event.Kind != "runtime-unavailable" {
		t.Fatalf("event=%+v", event)
	}
	if delay := sleeper.nextDelay(t); delay != 250*time.Millisecond {
		t.Fatalf("retry delay=%v", delay)
	}
	if got := pm.startCallCount(); got != 1 {
		t.Fatalf("replacement started before stopped persistence: %d", got)
	}
	sleeper.release()
	if event := receiveEvent(t, runtime.Events()); event.Kind != "runtime-ready" {
		t.Fatalf("event=%+v", event)
	}
	mu.Lock()
	gotAttempts := stoppedAttempts
	mu.Unlock()
	if gotAttempts != 2 {
		t.Fatalf("stopped checkpoint attempts=%d want=2", gotAttempts)
	}
	if got := pm.stopCallCount(); got != 1 {
		t.Fatalf("old child cleanup calls=%d want=1", got)
	}
}

func TestRuntimeCloseDuringStoppedCheckpointRetryReturnsPersistenceError(t *testing.T) {
	pm := &fakeProcessManager{}
	supervisor := mustSupervisor(t, pm)
	sleeper := newControlledSleeper()
	sink := func(_ context.Context, checkpoint room.RuntimeCheckpoint) error {
		if checkpoint.State == room.RuntimeProcessStopped {
			return errors.New("persistent checkpoint failure")
		}
		return nil
	}
	runtime, err := NewRuntime(supervisor, RuntimeConfig{ProjectRoot: "/srv/project"}, sink, sleeper)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	pm.server(0).crash(errors.New("boom"))
	if event := receiveEvent(t, runtime.Events()); event.Kind != "runtime-unavailable" {
		t.Fatalf("event=%+v", event)
	}
	if delay := sleeper.nextDelay(t); delay != 250*time.Millisecond {
		t.Fatalf("retry delay=%v", delay)
	}

	if err := runtime.Close(context.Background()); !errors.Is(err, ErrCheckpointPersistence) {
		t.Fatalf("Close err=%v want ErrCheckpointPersistence", err)
	}
	if got := pm.startCallCount(); got != 1 {
		t.Fatalf("replacement start calls=%d want=1", got)
	}
}

func TestRuntimeNeverRetriesMutationThatCrashesAfterReceive(t *testing.T) {
	pm := &fakeProcessManager{configs: []fakeAppConfig{{crashMethod: "turn/start"}, {}}}
	supervisor := mustSupervisor(t, pm)
	sleeper := newControlledSleeper()
	runtime := mustRuntime(t, supervisor, sleeper)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	_, err := runtime.StartTurn(context.Background(), "thread-1", "0123456789abcdef0123456789abcdef", "hello")
	var mutation *room.MutationError
	if !errors.As(err, &mutation) || mutation.Certainty != room.DeliveryUnknown {
		t.Fatalf("err=%v", err)
	}
	if event := receiveEvent(t, runtime.Events()); event.Kind != "runtime-unavailable" {
		t.Fatalf("event=%+v", event)
	}
	if delay := sleeper.nextDelay(t); delay != 250*time.Millisecond {
		t.Fatalf("delay=%v", delay)
	}
	sleeper.release()
	if event := receiveEvent(t, runtime.Events()); event.Kind != "runtime-ready" {
		t.Fatalf("event=%+v", event)
	}
	if got := countMethod(pm.requestMethods(), "turn/start"); got != 1 {
		t.Fatalf("turn/start requests=%d want=1", got)
	}
}

func TestRuntimeUnavailableOperationsFailWithoutRPC(t *testing.T) {
	pm := &fakeProcessManager{}
	supervisor := mustSupervisor(t, pm)
	sleeper := newControlledSleeper()
	runtime := mustRuntime(t, supervisor, sleeper)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	pm.server(0).crash(errors.New("boom"))
	if event := receiveEvent(t, runtime.Events()); event.Kind != "runtime-unavailable" {
		t.Fatalf("event=%+v", event)
	}
	before := len(pm.requestMethods())

	if _, err := runtime.ReadThread(context.Background(), "thread-1"); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("ReadThread err=%v", err)
	}
	if _, err := runtime.ResumeThread(context.Background(), "thread-1"); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("ResumeThread err=%v", err)
	}
	assertNotSentMutation(t, "thread/start", func() error {
		_, err := runtime.StartThread(context.Background())
		return err
	})
	assertNotSentMutation(t, "turn/start", func() error {
		_, err := runtime.StartTurn(context.Background(), "thread-1", "0123456789abcdef0123456789abcdef", "hello")
		return err
	})
	assertNotSentMutation(t, "turn/steer", func() error {
		return runtime.SteerTurn(context.Background(), "thread-1", "turn-1", "hello")
	})
	assertNotSentMutation(t, "turn/interrupt", func() error {
		return runtime.InterruptTurn(context.Background(), "thread-1", "turn-1")
	})
	if after := len(pm.requestMethods()); after != before {
		t.Fatalf("RPC requests while unavailable: before=%d after=%d", before, after)
	}
}

func TestRuntimeCloseCancelsBlockedEventForwardAndClosesEventsOnce(t *testing.T) {
	pm := &fakeProcessManager{}
	supervisor := mustSupervisor(t, pm)
	runtime := mustRuntime(t, supervisor, &recordingSleeper{recorded: make(chan time.Duration, 1)})
	events := runtime.Events()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, delta := range []string{"one", "two", "three", "four"} {
		if err := pm.server(0).notify("item/agentMessage/delta", map[string]any{
			"threadId": "thread-1", "turnId": "turn-1", "itemId": "item-1", "delta": delta,
		}); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan error, 1)
	go func() { done <- runtime.Close(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close deadlocked behind event backpressure")
	}
	for range events {
	}
	if _, ok := <-events; ok {
		t.Fatal("events reopened after close")
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCloseCancelsRestartSleep(t *testing.T) {
	pm := &fakeProcessManager{}
	supervisor := mustSupervisor(t, pm)
	sleeper := &recordingSleeper{recorded: make(chan time.Duration, 1), block: true}
	runtime := mustRuntime(t, supervisor, sleeper)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	pm.server(0).crash(errors.New("boom"))
	if event := receiveEvent(t, runtime.Events()); event.Kind != "runtime-unavailable" {
		t.Fatalf("event=%+v", event)
	}
	select {
	case <-sleeper.recorded:
	case <-time.After(2 * time.Second):
		t.Fatal("restart sleep not reached")
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := pm.startCallCount(); got != 1 {
		t.Fatalf("start calls=%d want=1", got)
	}
}

func mustRuntime(t *testing.T, supervisor *Supervisor, sleeper Sleeper) *Runtime {
	t.Helper()
	runtime, err := NewRuntime(supervisor, RuntimeConfig{ProjectRoot: "/srv/project"}, discardCheckpoint, sleeper)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func receiveEvent(t *testing.T, events <-chan room.AgentEvent) room.AgentEvent {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("events closed early")
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runtime event")
		return room.AgentEvent{}
	}
}

func assertNotSentMutation(t *testing.T, operation string, call func() error) {
	t.Helper()
	err := call()
	var mutation *room.MutationError
	if !errors.As(err, &mutation) || mutation.Operation != operation || mutation.Certainty != room.DeliveryNotSent || !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("%s err=%v", operation, err)
	}
}

func countMethod(methods []string, want string) int {
	count := 0
	for _, method := range methods {
		if method == want {
			count++
		}
	}
	return count
}

type controlledSleeper struct {
	delays  chan time.Duration
	permits chan struct{}
}

func newControlledSleeper() *controlledSleeper {
	return &controlledSleeper{delays: make(chan time.Duration, 4), permits: make(chan struct{}, 4)}
}

func (s *controlledSleeper) Sleep(ctx context.Context, delay time.Duration) error {
	select {
	case s.delays <- delay:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.permits:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *controlledSleeper) nextDelay(t *testing.T) time.Duration {
	t.Helper()
	select {
	case delay := <-s.delays:
		return delay
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for restart sleep")
		return 0
	}
}

func (s *controlledSleeper) release() { s.permits <- struct{}{} }

type recordingSleeper struct {
	recorded chan time.Duration
	block    bool
	once     sync.Once
}

func (s *recordingSleeper) Sleep(ctx context.Context, delay time.Duration) error {
	select {
	case s.recorded <- delay:
	case <-ctx.Done():
		return ctx.Err()
	}
	if !s.block {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}
