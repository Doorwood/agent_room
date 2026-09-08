package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"agent_romm/internal/room"
)

// These integration tests use the actual transactional repository. The fake
// agent makes external call counts observable without running Codex.
type recoveryAgent struct {
	mu                               sync.Mutex
	events                           chan room.AgentEvent
	starts, steers, cancels, threads int
	unknown                          bool
	history                          room.ThreadSnapshot
}

func (a *recoveryAgent) StartThread(context.Context) (room.ThreadSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.threads++
	return a.history, nil
}
func (a *recoveryAgent) ReadThread(context.Context, room.ThreadID) (room.ThreadSnapshot, error) {
	return a.history, nil
}
func (a *recoveryAgent) ResumeThread(context.Context, room.ThreadID) (room.ThreadSnapshot, error) {
	return a.history, nil
}
func (a *recoveryAgent) StartTurn(context.Context, room.ThreadID, room.ClientMessageID, string) (room.TurnID, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.starts++
	if a.unknown {
		return "", &room.MutationError{Operation: "turn/start", Certainty: room.DeliveryUnknown, Err: errors.New("private transport detail")}
	}
	return room.TurnID(fmt.Sprintf("turn-%d", a.starts)), nil
}
func (a *recoveryAgent) SteerTurn(context.Context, room.ThreadID, room.TurnID, string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steers++
	return a.controlError()
}
func (a *recoveryAgent) InterruptTurn(context.Context, room.ThreadID, room.TurnID) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancels++
	return a.controlError()
}
func (a *recoveryAgent) controlError() error {
	if a.unknown {
		return &room.MutationError{Operation: "control", Certainty: room.DeliveryUnknown, Err: errors.New("private transport detail")}
	}
	return nil
}
func (a *recoveryAgent) Events() <-chan room.AgentEvent { return a.events }
func (a *recoveryAgent) setUnknown(value bool)          { a.mu.Lock(); defer a.mu.Unlock(); a.unknown = value }
func (a *recoveryAgent) counts() (int, int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.starts, a.steers, a.cancels
}

type recoverySink struct{}

func (recoverySink) PublishDurable(room.DurableEvent)     {}
func (recoverySink) PublishTransient(room.TransientEvent) {}
func (recoverySink) Now() time.Time                       { return time.Unix(1, 0) }
func integrationCoordinator(t *testing.T, s *Store, a *recoveryAgent) *room.Coordinator {
	t.Helper()
	c, err := room.NewCoordinator("team", "/srv/project", s, a, recoverySink{}, recoverySink{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	if err := c.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c
}
func newRecoveryAgent() *recoveryAgent {
	return &recoveryAgent{events: make(chan room.AgentEvent), history: room.ThreadSnapshot{ID: "thread-1", CWD: "/srv/project"}}
}

func TestRecoveryControlStaleRetryAndCrashDoNotRepeat(t *testing.T) {
	for _, kind := range []room.ControlKind{room.ControlSteer, room.ControlCancel} {
		t.Run(string(kind), func(t *testing.T) {
			s := openTestStore(t)
			seedRoom(t, s)
			a := newRecoveryAgent()
			c := integrationCoordinator(t, s, a)
			ctx := context.Background()
			_, _ = c.Submit(ctx, alice, room.SubmitInput{ClientMessageID: aliceID, Text: "first"})
			a.setUnknown(true)
			if kind == room.ControlSteer {
				_, _ = c.Steer(ctx, bob, room.SteerInput{ClientMessageID: bobID, ExpectedTurnID: "turn-1", Text: "change"})
			} else {
				_, _ = c.Cancel(ctx, bob, room.CancelInput{ClientMessageID: bobID, ExpectedTurnID: "turn-1"})
			}
			a.setUnknown(false)
			// This terminal turn is independent of the still ambiguous control.
			if _, err := s.FinishTurn(ctx, "team", room.FinishTurnInput{TurnID: "turn-1", State: room.RequestCompleted}); err != nil {
				t.Fatal(err)
			}
			in := room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: bobID, Action: room.RecoveryRetry}
			if err := c.Resolve(ctx, bob, in); !errors.Is(err, room.ErrStaleTurn) {
				t.Fatalf("stale=%v", err)
			}
			if err := c.Resolve(ctx, bob, in); err != nil {
				t.Fatal(err)
			}
			starts, steers, cancels := a.counts()
			if starts != 1 || steers+cancels != 1 {
				t.Fatalf("calls=%d/%d/%d", starts, steers, cancels)
			}
			state, _ := messageState(t, s, bobID)
			if state != room.RequestFailed {
				t.Fatal(state)
			}
		})
	}
}

func TestRecoveryCompletedHistoryIsImmutableAndIdempotent(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	ctx := context.Background()
	_, _ = s.BindThread(ctx, "team", room.ThreadSnapshot{ID: "thread-1", CWD: "/srv/project"})
	item := room.CompletedItem{ThreadID: "thread-1", TurnID: "unrecorded-turn", ItemID: "item-1", Payload: json.RawMessage(`{"text":"complete"}`)}
	event, err := s.RecordCompletedItem(ctx, "team", item)
	if err != nil {
		t.Fatal(err)
	}
	s.beforeCommit = func() error { return errors.New("duplicate must not write") }
	again, err := s.RecordCompletedItem(ctx, "team", item)
	if err != nil || again.Seq != event.Seq {
		t.Fatalf("again=%#v err=%v", again, err)
	}
	s.beforeCommit = nil
	item.Payload = json.RawMessage(`{"text":"conflict"}`)
	if _, err := s.RecordCompletedItem(ctx, "team", item); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("conflict=%v", err)
	}
	latest, _ := s.LatestSeq(ctx, "team")
	if latest != event.Seq {
		t.Fatalf("conflict allocated seq=%d want=%d", latest, event.Seq)
	}
	item.ItemID = "new-item"
	injected := errors.New("crash before item commit")
	s.beforeCommit = func() error { return injected }
	if _, err := s.RecordCompletedItem(ctx, "team", item); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	s.beforeCommit = nil
	latest, _ = s.LatestSeq(ctx, "team")
	if latest != event.Seq {
		t.Fatal("half-written completed item")
	}
}

func TestRecoveryRestartWithUncertainInitialThreadNeverCreatesReplacement(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	_, _ = s.SetRoomStatus(context.Background(), "team", room.RoomRecovering)
	a := newRecoveryAgent()
	c, err := room.NewCoordinator("team", "/srv/project", s, a, recoverySink{}, recoverySink{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	defer func() { cancel(); <-done }()
	for i := 0; i < 2; i++ {
		if err := c.Recover(context.Background()); !errors.Is(err, room.ErrDeliveryUnknown) {
			t.Fatalf("recover=%v", err)
		}
	}
	snap, _ := c.Snapshot(context.Background())
	if a.threads != 0 || snap.Status != room.RoomThreadNeedsRepair {
		t.Fatalf("threads=%d snap=%#v", a.threads, snap)
	}
}

func TestRecoveryInvalidContinueKeepsQueueFrozen(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	a := newRecoveryAgent()
	c := integrationCoordinator(t, s, a)
	ctx := context.Background()
	a.setUnknown(true)
	_, _ = c.Submit(ctx, alice, room.SubmitInput{ClientMessageID: aliceID, Text: "first"})
	_, _ = c.Submit(ctx, bob, room.SubmitInput{ClientMessageID: bobID, Text: "queued"})
	a.setUnknown(false)
	for _, replacement := range []room.ClientMessageID{"", bobID} {
		err := c.Resolve(ctx, bob, room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: aliceID, Action: room.RecoveryContinue, ReplacementMessageID: replacement, Instruction: "inspect"})
		if err == nil {
			t.Fatal("invalid replacement accepted")
		}
	}
	snap, _ := c.Snapshot(ctx)
	calls, _, _ := a.counts()
	if snap.Status != room.RoomRecovering || calls != 1 {
		t.Fatalf("snap=%#v calls=%d", snap, calls)
	}
}

func TestRecoveryControlActionsRollbackWithoutHalfUnfreeze(t *testing.T) {
	for _, kind := range []room.ControlKind{room.ControlSteer, room.ControlCancel} {
		for _, action := range []room.RecoveryAction{room.RecoveryRetry, room.RecoverySkip, room.RecoveryContinue} {
			t.Run(string(kind)+"/"+string(action), func(t *testing.T) {
				s := openTestStore(t)
				seedRoom(t, s)
				a := newRecoveryAgent()
				c := integrationCoordinator(t, s, a)
				ctx := context.Background()
				_, _ = c.Submit(ctx, alice, room.SubmitInput{ClientMessageID: aliceID, Text: "first"})
				a.setUnknown(true)
				if kind == room.ControlSteer {
					_, _ = c.Steer(ctx, bob, room.SteerInput{ClientMessageID: bobID, ExpectedTurnID: "turn-1", Text: "change"})
				} else {
					_, _ = c.Cancel(ctx, bob, room.CancelInput{ClientMessageID: bobID, ExpectedTurnID: "turn-1"})
				}
				a.setUnknown(false)
				in := room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: bobID, Action: action}
				if action == room.RecoveryContinue {
					in.ReplacementMessageID = "00000000000000000000000000000004"
					in.Instruction = "inspect"
				}
				injected := errors.New("control recovery commit failed")
				s.beforeCommit = func() error { return injected }
				if err := c.Resolve(ctx, bob, in); !errors.Is(err, injected) {
					t.Fatal(err)
				}
				s.beforeCommit = nil
				state, _ := messageState(t, s, bobID)
				starts, steers, cancels := a.counts()
				snap, _ := c.Snapshot(ctx)
				if state != room.RequestNeedsReview || starts != 1 || steers+cancels != 1 || snap.Status != room.RoomRecovering || tableCount(t, s, "recovery_results") != 0 {
					t.Fatalf("state=%s calls=%d/%d/%d snap=%#v", state, starts, steers, cancels, snap)
				}
				if err := c.Resolve(ctx, bob, in); err != nil {
					t.Fatal(err)
				}
				if err := c.Resolve(ctx, bob, in); err != nil {
					t.Fatal(err)
				}
				starts, steers, cancels = a.counts()
				want := 1
				if action == room.RecoveryRetry {
					want = 2
				}
				if starts != 1 || steers+cancels != want {
					t.Fatalf("calls=%d/%d/%d", starts, steers, cancels)
				}
			})
		}
	}
}

func TestRecoveryLiveCompletionCannotSettleReviewOrOverwriteUnsupportedFailure(t *testing.T) {
	for _, review := range []bool{false, true} {
		t.Run(fmt.Sprint(review), func(t *testing.T) {
			s := openTestStore(t)
			seedRoom(t, s)
			a := newRecoveryAgent()
			c := integrationCoordinator(t, s, a)
			ctx := context.Background()
			_, _ = c.Submit(ctx, alice, room.SubmitInput{ClientMessageID: aliceID, Text: "first"})
			event := room.AgentEvent{Kind: "turn-failed", ThreadID: "thread-1", TurnID: "turn-1", Error: errors.New("unsupported request private detail")}
			want := room.RequestFailed
			if review {
				event.Kind = "runtime-unavailable"
				want = room.RequestNeedsReview
			}
			a.events <- event
			until := time.Now().Add(time.Second)
			for {
				state, _ := messageState(t, s, aliceID)
				if state == want {
					break
				}
				if time.Now().After(until) {
					t.Fatalf("state=%s", state)
				}
				time.Sleep(time.Millisecond)
			}
			a.events <- room.AgentEvent{Kind: "turn-completed", ThreadID: "thread-1", TurnID: "turn-1"}
			// A following runtime-unavailable event is a loop barrier observable via the
			// snapshot status, including when the original already failed.
			a.events <- room.AgentEvent{Kind: "runtime-unavailable"}
			for {
				snap, _ := c.Snapshot(ctx)
				if snap.Status == room.RoomRecovering {
					break
				}
				if time.Now().After(until) {
					t.Fatal("barrier timed out")
				}
				time.Sleep(time.Millisecond)
			}
			state, _ := messageState(t, s, aliceID)
			if state != want {
				t.Fatalf("late state=%s want=%s", state, want)
			}
		})
	}
}

func TestRecoveryCoordinatorActionsAndLostResponses(t *testing.T) {
	for _, kind := range []string{"prompt", "steer", "cancel"} {
		for _, action := range []room.RecoveryAction{room.RecoveryRetry, room.RecoverySkip, room.RecoveryContinue} {
			t.Run(kind+"/"+string(action), func(t *testing.T) {
				s := openTestStore(t)
				seedRoom(t, s)
				a := newRecoveryAgent()
				c := integrationCoordinator(t, s, a)
				ctx := context.Background()
				target := aliceID
				if kind != "prompt" {
					if _, err := c.Submit(ctx, alice, room.SubmitInput{ClientMessageID: aliceID, Text: "first"}); err != nil {
						t.Fatal(err)
					}
					target = bobID
				}
				a.setUnknown(true)
				var err error
				switch kind {
				case "prompt":
					_, err = c.Submit(ctx, alice, room.SubmitInput{ClientMessageID: target, Text: "first"})
				case "steer":
					_, err = c.Steer(ctx, bob, room.SteerInput{ClientMessageID: target, ExpectedTurnID: "turn-1", Text: "change"})
				case "cancel":
					_, err = c.Cancel(ctx, bob, room.CancelInput{ClientMessageID: target, ExpectedTurnID: "turn-1"})
				}
				if !errors.Is(err, room.ErrDeliveryUnknown) {
					t.Fatalf("unknown=%v", err)
				}
				a.setUnknown(false)
				in := room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: target, Action: action}
				replacement := room.ClientMessageID("00000000000000000000000000000004")
				if action == room.RecoveryContinue {
					in.ReplacementMessageID = replacement
					in.Instruction = "inspect current state"
				}
				if err := c.Resolve(ctx, bob, in); err != nil {
					t.Fatal(err)
				}
				starts, steers, cancels := a.counts()
				wantStarts, wantSteers, wantCancels := 1, 0, 0
				if kind == "steer" {
					wantSteers = 1
				}
				if kind == "cancel" {
					wantCancels = 1
				}
				if action == room.RecoveryRetry {
					switch kind {
					case "prompt":
						wantStarts++
					case "steer":
						wantSteers++
					case "cancel":
						wantCancels++
					}
				}
				if action == room.RecoveryContinue && kind == "prompt" {
					wantStarts++
				}
				if starts != wantStarts || steers != wantSteers || cancels != wantCancels {
					t.Fatalf("calls=%d/%d/%d want=%d/%d/%d", starts, steers, cancels, wantStarts, wantSteers, wantCancels)
				}
				state, _ := messageState(t, s, target)
				want := room.RequestFailed
				if action == room.RecoveryRetry {
					want = room.RequestCompleted
					if kind == "prompt" {
						want = room.RequestRunning
					}
				}
				if state != want {
					t.Fatalf("state=%s want=%s", state, want)
				}
				if err := c.Resolve(ctx, bob, in); err != nil {
					t.Fatal(err)
				}
				gotStarts, gotSteers, gotCancels := a.counts()
				if gotStarts != starts || gotSteers != steers || gotCancels != cancels {
					t.Fatal("lost recovery response replayed external mutation")
				}
				if action == room.RecoveryRetry {
					events, err := s.Events(ctx, "team", 0, 0, 100)
					if err != nil {
						t.Fatal(err)
					}
					found := false
					for _, event := range events {
						if event.Kind == "recovery/retried" {
							var body struct {
								Risk bool `json:"duplicate_effects_possible"`
							}
							_ = json.Unmarshal(event.Payload, &body)
							found = body.Risk
						}
					}
					if !found {
						t.Fatal("retry warning not durable")
					}
				}
			})
		}
	}
}

func TestRecoveryCoordinatorConcurrentWinnerAndRollback(t *testing.T) {
	for _, action := range []room.RecoveryAction{room.RecoveryRetry, room.RecoverySkip, room.RecoveryContinue} {
		t.Run(string(action), func(t *testing.T) {
			s := openTestStore(t)
			seedRoom(t, s)
			a := newRecoveryAgent()
			c := integrationCoordinator(t, s, a)
			a.setUnknown(true)
			_, _ = c.Submit(context.Background(), alice, room.SubmitInput{ClientMessageID: aliceID, Text: "first"})
			a.setUnknown(false)
			in := room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: aliceID, Action: action}
			if action == room.RecoveryContinue {
				in.ReplacementMessageID = bobID
				in.Instruction = "inspect"
			}
			injected := errors.New("crash at transaction commit")
			s.beforeCommit = func() error { return injected }
			if err := c.Resolve(context.Background(), bob, in); !errors.Is(err, injected) {
				t.Fatalf("err=%v", err)
			}
			s.beforeCommit = nil
			snap, _ := c.Snapshot(context.Background())
			state, _ := messageState(t, s, aliceID)
			calls, _, _ := a.counts()
			if snap.Status != room.RoomRecovering || state != room.RequestNeedsReview || calls != 1 {
				t.Fatalf("snap=%#v state=%s calls=%d", snap, state, calls)
			}
			if err := c.Resolve(context.Background(), bob, in); err != nil {
				t.Fatal(err)
			}
		})
	}
	s := openTestStore(t)
	seedRoom(t, s)
	a := newRecoveryAgent()
	c := integrationCoordinator(t, s, a)
	a.setUnknown(true)
	_, _ = c.Submit(context.Background(), alice, room.SubmitInput{ClientMessageID: aliceID, Text: "first"})
	a.setUnknown(false)
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, action := range []room.RecoveryAction{room.RecoveryRetry, room.RecoverySkip} {
		go func(i int, action room.RecoveryAction) {
			<-start
			results <- c.Resolve(context.Background(), bob, room.RecoverInput{ClientMessageID: room.ClientMessageID(fmt.Sprintf("%032x", 10+i)), TargetMessageID: aliceID, Action: action})
		}(i, action)
	}
	close(start)
	success, stale := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, room.ErrStaleRecovery) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatalf("success=%d stale=%d", success, stale)
	}
}

func TestRecoveryHistoryTerminalizesReviewButNeverOverwritesTerminal(t *testing.T) {
	for _, state := range []room.RequestState{room.RequestCompleted, room.RequestFailed, room.RequestInterrupted} {
		t.Run(string(state), func(t *testing.T) {
			s := openTestStore(t)
			seedRoom(t, s)
			acceptedPrompt(t, s, aliceID)
			ctx := context.Background()
			_ = s.BeginDispatch(ctx, "team", aliceID)
			_, _ = s.BindRunningTurn(ctx, "team", aliceID, "turn-1")
			_, _ = s.MarkNeedsReview(ctx, "team", aliceID, room.ReviewReason{Code: "runtime-unavailable", DetailDigest: digest})
			_, _ = s.BindThread(ctx, "team", room.ThreadSnapshot{ID: "thread-1", CWD: "/srv/project"})
			a := newRecoveryAgent()
			a.history.Turns = []room.TurnSnapshot{{ID: "turn-1", State: state, Items: []room.CompletedItem{{ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Payload: json.RawMessage(`{"text":"final"}`)}}}}
			c := integrationCoordinator(t, s, a)
			snap, _ := c.Snapshot(ctx)
			got, _ := messageState(t, s, aliceID)
			if got != state || snap.Status != room.RoomReady || snap.Active != nil {
				t.Fatalf("state=%s snap=%#v", got, snap)
			}
			var itemCount int
			if err := s.db.QueryRow("SELECT count(*) FROM room_events WHERE kind = 'item/completed'").Scan(&itemCount); err != nil || itemCount != 1 {
				t.Fatalf("items=%d err=%v", itemCount, err)
			}
			if _, err := s.FinishTurn(ctx, "team", room.FinishTurnInput{TurnID: "turn-1", State: room.RequestCompleted}); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("late completion=%v", err)
			}
		})
	}
}

func TestLoadRecoveryImageSurvivesCloseAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "room.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	seedRoom(t, s)
	if _, err := s.BindThread(context.Background(), "team", room.ThreadSnapshot{ID: "thread-1", CWD: "/srv/project"}); err != nil {
		t.Fatal(err)
	}
	acceptedPrompt(t, s, aliceID)
	if err := s.BeginDispatch(context.Background(), "team", aliceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindRunningTurn(context.Background(), "team", aliceID, "turn-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptMessage(context.Background(), "team", bob, room.SubmitInput{ClientMessageID: bobID, Text: "next"}); err != nil {
		t.Fatal(err)
	}
	checkpoint := room.RuntimeCheckpoint{State: room.RuntimeProcessRunning, Generation: "generation-1", PID: 123, PGID: 123, ProcessStart: "boot:42", CodexVersion: "codex-cli 0.151.0-alpha.7.2", SchemaSHA256: digest}
	if err := s.SaveRuntimeCheckpoint(context.Background(), "team", checkpoint); err != nil {
		t.Fatal(err)
	}
	wantLatest, err := s.LatestSeq(context.Background(), "team")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	image, err := s.LoadRecoveryImage(context.Background(), "team")
	if err != nil {
		t.Fatal(err)
	}
	if image.Status != room.RoomReady || image.ThreadID != "thread-1" || image.Active == nil || image.Active.MessageID != aliceID || image.Active.TurnID != "turn-1" || image.Active.State != room.RequestRunning {
		t.Fatalf("image=%#v", image)
	}
	if len(image.Queue) != 1 || image.Queue[0].Input.ClientMessageID != bobID || image.Queue[0].Actor != bob || image.Queue[0].AcceptedSeq == 0 {
		t.Fatalf("queue=%#v", image.Queue)
	}
	if image.Checkpoint != checkpoint {
		t.Fatalf("checkpoint=%#v", image.Checkpoint)
	}
	if latest, err := s.LatestSeq(context.Background(), "team"); err != nil || latest != wantLatest {
		t.Fatalf("latest=%d want=%d err=%v", latest, wantLatest, err)
	}
}

func TestLoadRecoveryImageRestoresOrderedPendingControlsBesideActiveAndQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "room.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	seedRoom(t, s)
	if _, err := s.BindThread(context.Background(), "team", room.ThreadSnapshot{ID: "thread-1", CWD: "/srv/project"}); err != nil {
		t.Fatal(err)
	}
	acceptedPrompt(t, s, aliceID)
	if err := s.BeginDispatch(context.Background(), "team", aliceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindRunningTurn(context.Background(), "team", aliceID, "turn-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptMessage(context.Background(), "team", bob, room.SubmitInput{ClientMessageID: bobID, Text: "queued"}); err != nil {
		t.Fatal(err)
	}
	controls := []struct {
		input room.ControlInput
		state room.RequestState
	}{
		{room.ControlInput{ClientMessageID: "00000000000000000000000000000011", Kind: room.ControlSteer, ExpectedTurnID: "turn-1", Text: "steer dispatching"}, room.RequestDispatching},
		{room.ControlInput{ClientMessageID: "00000000000000000000000000000012", Kind: room.ControlCancel, ExpectedTurnID: "turn-1"}, room.RequestDispatching},
		{room.ControlInput{ClientMessageID: "00000000000000000000000000000013", Kind: room.ControlSteer, ExpectedTurnID: "turn-1", Text: "steer uncertain"}, room.RequestNeedsReview},
		{room.ControlInput{ClientMessageID: "00000000000000000000000000000014", Kind: room.ControlCancel, ExpectedTurnID: "turn-1"}, room.RequestNeedsReview},
	}
	for _, control := range controls {
		if _, err := s.AcceptControl(context.Background(), "team", bob, control.input); err != nil {
			t.Fatal(err)
		}
		if control.state == room.RequestNeedsReview {
			if _, err := s.FinishControl(context.Background(), "team", control.input.ClientMessageID, room.ControlOutcome{State: room.RequestNeedsReview, ErrorCode: "delivery-unknown", ErrorDigest: digest}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	image, err := s.LoadRecoveryImage(context.Background(), "team")
	if err != nil {
		t.Fatal(err)
	}
	if image.Active == nil || image.Active.MessageID != aliceID || image.Active.TurnID != "turn-1" || image.Active.State != room.RequestRunning {
		t.Fatalf("active=%#v", image.Active)
	}
	if len(image.Queue) != 1 || image.Queue[0].Input.ClientMessageID != bobID {
		t.Fatalf("queue=%#v", image.Queue)
	}
	if len(image.PendingControls) != len(controls) {
		t.Fatalf("pending controls=%#v", image.PendingControls)
	}
	for index, want := range controls {
		got := image.PendingControls[index]
		if got.MessageID != want.input.ClientMessageID || got.TurnID != want.input.ExpectedTurnID || got.State != want.state {
			t.Fatalf("index=%d got=%#v want=%#v", index, got, want)
		}
	}
}

func TestSaveRuntimeCheckpointUpsertsAndValidatesState(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	base := room.RuntimeCheckpoint{State: room.RuntimeProcessRunning, Generation: "g1", PID: 10, PGID: 10, ProcessStart: "p1", CodexVersion: "v1", SchemaSHA256: digest}
	if err := s.SaveRuntimeCheckpoint(context.Background(), "team", base); err != nil {
		t.Fatal(err)
	}
	stopped := base
	stopped.State = room.RuntimeProcessStopped
	stopped.Generation = "g2"
	stopped.PID = 0
	stopped.PGID = 0
	if err := s.SaveRuntimeCheckpoint(context.Background(), "team", stopped); err != nil {
		t.Fatal(err)
	}
	image, err := s.LoadRecoveryImage(context.Background(), "team")
	if err != nil {
		t.Fatal(err)
	}
	if image.Checkpoint != stopped {
		t.Fatalf("checkpoint=%#v", image.Checkpoint)
	}
	base.State = room.RuntimeProcessState("unknown")
	if err := s.SaveRuntimeCheckpoint(context.Background(), "team", base); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("state got %v", err)
	}
	base.State = room.RuntimeProcessRunning
	base.SchemaSHA256 = "NOT-A-DIGEST"
	if err := s.SaveRuntimeCheckpoint(context.Background(), "team", base); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("digest got %v", err)
	}
}

func TestStoppedCheckpointWithZeroProcessIDsSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "room.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	seedRoom(t, s)
	checkpoint := room.RuntimeCheckpoint{State: room.RuntimeProcessStopped, Generation: "stopped-generation", PID: 0, PGID: 0, ProcessStart: "boot:42", CodexVersion: "codex-cli 0.151.0-alpha.7.2", SchemaSHA256: digest}
	if err := s.SaveRuntimeCheckpoint(context.Background(), "team", checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	image, err := s.LoadRecoveryImage(context.Background(), "team")
	if err != nil {
		t.Fatal(err)
	}
	if image.Checkpoint != checkpoint {
		t.Fatalf("checkpoint=%#v", image.Checkpoint)
	}
}

func TestRuntimeCheckpointProcessIDsAreStrictForEachState(t *testing.T) {
	base := room.RuntimeCheckpoint{State: room.RuntimeProcessRunning, Generation: "g1", PID: 2, PGID: 2, ProcessStart: "p1", CodexVersion: "v1", SchemaSHA256: digest}
	cases := []struct {
		name string
		edit func(*room.RuntimeCheckpoint)
	}{
		{"running-pid-zero", func(cp *room.RuntimeCheckpoint) { cp.PID = 0 }},
		{"running-pid-one", func(cp *room.RuntimeCheckpoint) { cp.PID = 1 }},
		{"running-pid-negative", func(cp *room.RuntimeCheckpoint) { cp.PID = -1 }},
		{"running-pgid-zero", func(cp *room.RuntimeCheckpoint) { cp.PGID = 0 }},
		{"running-pgid-one", func(cp *room.RuntimeCheckpoint) { cp.PGID = 1 }},
		{"running-pgid-negative", func(cp *room.RuntimeCheckpoint) { cp.PGID = -1 }},
		{"stopped-pid-nonzero", func(cp *room.RuntimeCheckpoint) { cp.State = room.RuntimeProcessStopped; cp.PID = 2; cp.PGID = 0 }},
		{"stopped-pgid-nonzero", func(cp *room.RuntimeCheckpoint) { cp.State = room.RuntimeProcessStopped; cp.PID = 0; cp.PGID = 2 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			seedRoom(t, s)
			checkpoint := base
			tc.edit(&checkpoint)
			if err := s.SaveRuntimeCheckpoint(context.Background(), "team", checkpoint); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("checkpoint=%#v err=%v", checkpoint, err)
			}
			if got := tableCount(t, s, "runtime_checkpoints"); got != 0 {
				t.Fatalf("invalid checkpoint persisted: rows=%d", got)
			}
		})
	}
}

func TestMarkNeedsReviewAcceptsDispatchingAndRunningOnly(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(map[bool]string{false: "dispatching", true: "running"}[running], func(t *testing.T) {
			s := openTestStore(t)
			seedRoom(t, s)
			acceptedPrompt(t, s, aliceID)
			if err := s.BeginDispatch(context.Background(), "team", aliceID); err != nil {
				t.Fatal(err)
			}
			if running {
				if _, err := s.BindRunningTurn(context.Background(), "team", aliceID, "turn-1"); err != nil {
					t.Fatal(err)
				}
			}
			event, err := s.MarkNeedsReview(context.Background(), "team", aliceID, room.ReviewReason{Code: "delivery-unknown", DetailDigest: digest})
			if err != nil || event.Kind != "message/needs-review" {
				t.Fatalf("event=%#v err=%v", event, err)
			}
			if _, err := s.MarkNeedsReview(context.Background(), "team", aliceID, room.ReviewReason{Code: "delivery-unknown", DetailDigest: digest}); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("second mark=%v", err)
			}
		})
	}
}

func reviewedControl(t *testing.T, s *Store, kind room.ControlKind) room.ControlInput {
	t.Helper()
	in := room.ControlInput{ClientMessageID: aliceID, Kind: kind, ExpectedTurnID: "turn-1"}
	if kind == room.ControlSteer {
		in.Text = "inspect"
	}
	if _, err := s.AcceptControl(context.Background(), "team", alice, in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishControl(context.Background(), "team", aliceID, room.ControlOutcome{State: room.RequestNeedsReview, ErrorCode: "delivery-unknown", ErrorDigest: digest}); err != nil {
		t.Fatal(err)
	}
	return in
}

func TestResolveReviewActionsAreAtomicAndHaveDocumentedSemantics(t *testing.T) {
	cases := []struct {
		name        string
		action      room.RecoveryAction
		wantState   room.RequestState
		wantRetry   room.RetryKind
		replacement bool
	}{
		{name: "prompt-retry", action: room.RecoveryRetry, wantState: room.RequestQueued},
		{name: "skip", action: room.RecoverySkip, wantState: room.RequestFailed},
		{name: "continue", action: room.RecoveryContinue, wantState: room.RequestFailed, replacement: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			seedRoom(t, s)
			beginAndReview(t, s, aliceID)
			in := room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: aliceID, Action: tc.action}
			if tc.replacement {
				in.ReplacementMessageID = bobID
				in.Instruction = "inspect current state"
			}
			result, err := s.ResolveReview(context.Background(), "team", bob, in)
			if err != nil || result.Duplicate || result.Retry != nil || len(result.Events) == 0 {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			state, _ := messageState(t, s, aliceID)
			if state != tc.wantState {
				t.Fatalf("state=%s", state)
			}
			if tc.replacement {
				replacement, _ := messageState(t, s, bobID)
				if replacement != room.RequestQueued {
					t.Fatalf("replacement=%s", replacement)
				}
			}
		})
	}

	for _, kind := range []room.ControlKind{room.ControlSteer, room.ControlCancel} {
		t.Run("control-retry-"+string(kind), func(t *testing.T) {
			s := openTestStore(t)
			seedRoom(t, s)
			original := reviewedControl(t, s, kind)
			result, err := s.ResolveReview(context.Background(), "team", bob, room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: aliceID, Action: room.RecoveryRetry})
			if err != nil || result.Retry == nil {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			wantKind := room.RetrySteer
			if kind == room.ControlCancel {
				wantKind = room.RetryCancel
			}
			if result.Retry.Kind != wantKind || result.Retry.ClientMessageID != aliceID || result.Retry.Actor != alice || result.Retry.ExpectedTurnID != original.ExpectedTurnID || result.Retry.Text != original.Text {
				t.Fatalf("retry=%#v", result.Retry)
			}
			state, _ := messageState(t, s, aliceID)
			if state != room.RequestDispatching {
				t.Fatalf("state=%s", state)
			}
		})
	}
}

func TestDuplicateRecoveryReturnsStoredResultWithoutExecutableRetry(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	reviewedControl(t, s, room.ControlSteer)
	in := room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: aliceID, Action: room.RecoveryRetry}
	first, err := s.ResolveReview(context.Background(), "team", bob, in)
	if err != nil || first.Retry == nil {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := s.ResolveReview(context.Background(), "team", bob, in)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Duplicate || second.Retry != nil || len(second.Events) != len(first.Events) {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	for index := range first.Events {
		if first.Events[index].Seq != second.Events[index].Seq {
			t.Fatalf("event mismatch first=%#v second=%#v", first.Events, second.Events)
		}
	}
}

func TestEveryRecoveryActionIsIdempotent(t *testing.T) {
	for _, action := range []room.RecoveryAction{room.RecoveryRetry, room.RecoverySkip, room.RecoveryContinue} {
		t.Run(string(action), func(t *testing.T) {
			s := openTestStore(t)
			seedRoom(t, s)
			beginAndReview(t, s, aliceID)
			in := room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: aliceID, Action: action}
			if action == room.RecoveryContinue {
				in.ReplacementMessageID = bobID
				in.Instruction = "continue safely"
			}
			first, err := s.ResolveReview(context.Background(), "team", bob, in)
			if err != nil {
				t.Fatal(err)
			}
			second, err := s.ResolveReview(context.Background(), "team", bob, in)
			if err != nil {
				t.Fatal(err)
			}
			if first.Duplicate || !second.Duplicate || second.Retry != nil || len(second.Events) != len(first.Events) {
				t.Fatalf("first=%#v second=%#v", first, second)
			}
			for index := range first.Events {
				if second.Events[index].Seq != first.Events[index].Seq {
					t.Fatalf("first=%#v second=%#v", first.Events, second.Events)
				}
			}
		})
	}
}

func TestSteerAndCancelSupportSkipAndContinueWithoutExecutableRetry(t *testing.T) {
	for _, controlKind := range []room.ControlKind{room.ControlSteer, room.ControlCancel} {
		for _, action := range []room.RecoveryAction{room.RecoverySkip, room.RecoveryContinue} {
			t.Run(string(controlKind)+"-"+string(action), func(t *testing.T) {
				s := openTestStore(t)
				seedRoom(t, s)
				reviewedControl(t, s, controlKind)
				in := room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: aliceID, Action: action}
				if action == room.RecoveryContinue {
					in.ReplacementMessageID = bobID
					in.Instruction = "continue after control"
				}
				result, err := s.ResolveReview(context.Background(), "team", bob, in)
				if err != nil {
					t.Fatal(err)
				}
				if result.Retry != nil {
					t.Fatalf("unexpected executable retry: %#v", result.Retry)
				}
				state, _ := messageState(t, s, aliceID)
				if state != room.RequestFailed {
					t.Fatalf("control state=%s", state)
				}
				if action == room.RecoveryContinue {
					replacement, _ := messageState(t, s, bobID)
					if replacement != room.RequestQueued {
						t.Fatalf("replacement state=%s", replacement)
					}
				}
			})
		}
	}
}

func TestDuplicateRecoveryDoesNotInvokeWriteHook(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	beginAndReview(t, s, aliceID)
	in := room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: aliceID, Action: room.RecoverySkip}
	if _, err := s.ResolveReview(context.Background(), "team", bob, in); err != nil {
		t.Fatal(err)
	}
	s.beforeCommit = func() error { return errors.New("write hook must not run") }
	result, err := s.ResolveReview(context.Background(), "team", bob, in)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Duplicate || result.Retry != nil {
		t.Fatalf("result=%#v", result)
	}
}

func TestRecoveryIdentityConflictsOnEverySemanticField(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	beginAndReview(t, s, aliceID)
	base := room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: aliceID, Action: room.RecoveryContinue, ReplacementMessageID: bobID, Instruction: "continue safely"}
	if _, err := s.ResolveReview(context.Background(), "team", bob, base); err != nil {
		t.Fatal(err)
	}
	variants := []struct {
		actor room.Actor
		in    room.RecoverInput
	}{
		{alice, base},
		{bob, room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: bobID, Action: room.RecoveryContinue, ReplacementMessageID: aliceID, Instruction: "continue safely"}},
		{bob, room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: aliceID, Action: room.RecoverySkip}},
		{bob, room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: aliceID, Action: room.RecoveryContinue, ReplacementMessageID: "00000000000000000000000000000004", Instruction: "continue safely"}},
		{bob, room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: aliceID, Action: room.RecoveryContinue, ReplacementMessageID: bobID, Instruction: "changed"}},
	}
	for index, variant := range variants {
		_, err := s.ResolveReview(context.Background(), "team", variant.actor, variant.in)
		if !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("case %d got %v", index, err)
		}
	}
}

func TestConcurrentRecoveriesHaveOneWinner(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	beginAndReview(t, s, aliceID)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	inputs := []room.RecoverInput{
		{ClientMessageID: "00000000000000000000000000000011", TargetMessageID: aliceID, Action: room.RecoveryRetry},
		{ClientMessageID: "00000000000000000000000000000012", TargetMessageID: aliceID, Action: room.RecoverySkip},
	}
	for _, in := range inputs {
		wg.Add(1)
		go func(in room.RecoverInput) {
			defer wg.Done()
			<-start
			_, err := s.ResolveReview(context.Background(), "team", bob, in)
			errs <- err
		}(in)
	}
	close(start)
	wg.Wait()
	close(errs)
	var success, stale int
	for err := range errs {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrStaleRecovery):
			stale++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatalf("success=%d stale=%d", success, stale)
	}
}

func TestRecoveryBeforeCommitFailureLeavesTargetFrozenAndNoJournal(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	beginAndReview(t, s, aliceID)
	beforeMessages := tableCount(t, s, "messages")
	beforeEvents := tableCount(t, s, "room_events")
	injected := errors.New("crash before recovery commit")
	s.beforeCommit = func() error { return injected }
	_, err := s.ResolveReview(context.Background(), "team", bob, room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: aliceID, Action: room.RecoveryContinue, ReplacementMessageID: bobID, Instruction: "continue"})
	if !errors.Is(err, injected) {
		t.Fatalf("got %v", err)
	}
	s.beforeCommit = nil
	state, _ := messageState(t, s, aliceID)
	if state != room.RequestNeedsReview || tableCount(t, s, "messages") != beforeMessages || tableCount(t, s, "room_events") != beforeEvents || tableCount(t, s, "recovery_results") != 0 {
		t.Fatalf("state=%s messages=%d events=%d recoveries=%d", state, tableCount(t, s, "messages"), tableCount(t, s, "room_events"), tableCount(t, s, "recovery_results"))
	}
}

func TestEachRecoveryActionRollsBackAtTheBeforeCommitHook(t *testing.T) {
	for _, action := range []room.RecoveryAction{room.RecoveryRetry, room.RecoverySkip, room.RecoveryContinue} {
		t.Run(string(action), func(t *testing.T) {
			s := openTestStore(t)
			seedRoom(t, s)
			beginAndReview(t, s, aliceID)
			beforeMessages := tableCount(t, s, "messages")
			beforeBindings := tableCount(t, s, "turn_bindings")
			beforeEvents := tableCount(t, s, "room_events")
			in := room.RecoverInput{ClientMessageID: thirdID, TargetMessageID: aliceID, Action: action}
			if action == room.RecoveryContinue {
				in.ReplacementMessageID = bobID
				in.Instruction = "continue"
			}
			injected := errors.New("crash before commit")
			s.beforeCommit = func() error { return injected }
			if _, err := s.ResolveReview(context.Background(), "team", bob, in); !errors.Is(err, injected) {
				t.Fatalf("got %v", err)
			}
			s.beforeCommit = nil
			state, _ := messageState(t, s, aliceID)
			if state != room.RequestNeedsReview || tableCount(t, s, "messages") != beforeMessages || tableCount(t, s, "turn_bindings") != beforeBindings || tableCount(t, s, "room_events") != beforeEvents || tableCount(t, s, "recovery_results") != 0 {
				t.Fatalf("state=%s messages=%d bindings=%d events=%d recoveries=%d", state, tableCount(t, s, "messages"), tableCount(t, s, "turn_bindings"), tableCount(t, s, "room_events"), tableCount(t, s, "recovery_results"))
			}
		})
	}
}

func TestBindThreadRepairStatusAndCompletedItemValidationAreAtomic(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	if _, err := s.BindThread(context.Background(), "team", room.ThreadSnapshot{ID: "thread-1", CWD: "/wrong"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cwd got %v", err)
	}
	event, err := s.BindThread(context.Background(), "team", room.ThreadSnapshot{ID: "thread-1", CWD: "/srv/project"})
	if err != nil || event.Kind != "thread/bound" {
		t.Fatalf("event=%#v err=%v", event, err)
	}
	if _, err := s.MarkThreadNeedsRepair(context.Background(), "team", room.RepairReason{Code: "rollout-missing", DetailDigest: digest}); err != nil {
		t.Fatal(err)
	}
	image, err := s.LoadRecoveryImage(context.Background(), "team")
	if err != nil || image.Status != room.RoomThreadNeedsRepair || image.ThreadID != "thread-1" {
		t.Fatalf("image=%#v err=%v", image, err)
	}
	if _, err := s.MarkThreadNeedsRepair(context.Background(), "team", room.RepairReason{Code: "again", DetailDigest: digest}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("second repair got %v", err)
	}
}

func TestSetRoomStatusIsCompareAndSwapWithIdempotentNoop(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	event, err := s.SetRoomStatus(context.Background(), "team", room.RoomRecovering)
	if err != nil || event == nil || event.Kind != "room/status" {
		t.Fatalf("event=%#v err=%v", event, err)
	}
	latest, _ := s.LatestSeq(context.Background(), "team")
	noop, err := s.SetRoomStatus(context.Background(), "team", room.RoomRecovering)
	if err != nil || noop != nil {
		t.Fatalf("noop=%#v err=%v", noop, err)
	}
	if after, _ := s.LatestSeq(context.Background(), "team"); after != latest {
		t.Fatalf("noop allocated seq: before=%d after=%d", latest, after)
	}
	if _, err := s.SetRoomStatus(context.Background(), "team", room.RoomStatus("broken")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid status got %v", err)
	}
}
