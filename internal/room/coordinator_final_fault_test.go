package room

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type oneShotFinalFault struct {
	Repository
	item, terminal bool
	err            error
}

func (r *oneShotFinalFault) RecordCompletedItem(ctx context.Context, id RoomID, item CompletedItem) (DurableEvent, error) {
	if r.item {
		r.item = false
		return DurableEvent{}, r.err
	}
	return r.Repository.RecordCompletedItem(ctx, id, item)
}
func (r *oneShotFinalFault) FinishTurn(ctx context.Context, id RoomID, in FinishTurnInput) ([]DurableEvent, error) {
	if r.terminal {
		r.terminal = false
		return nil, r.err
	}
	return r.Repository.FinishTurn(ctx, id, in)
}

func TestCoordinatorDurabilityFailureStopsBeforeSuccessor(t *testing.T) {
	for _, itemFault := range []bool{true, false} {
		t.Run(map[bool]string{true: "item", false: "terminal"}[itemFault], func(t *testing.T) {
			fault := errors.New("one-shot write failure")
			r := &oneShotFinalFault{Repository: newTestRepository(), item: itemFault, terminal: !itemFault, err: fault}
			a := newTestAgent()
			c, err := NewCoordinator("team", "/project", r, a, discardSink{}, testClock{})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- c.Run(ctx) }()
			if err := c.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= 2; i++ {
				if _, err := c.Submit(ctx, Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(i), Text: "work"}); err != nil {
					t.Fatal(err)
				}
			}
			if itemFault {
				a.emit(AgentEvent{Kind: "item-completed", Completed: &CompletedItem{ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Payload: json.RawMessage(`{}`)}})
			}
			a.emit(AgentEvent{Kind: "turn-completed", ThreadID: "thread-1", TurnID: "turn-1"})
			select {
			case err := <-done:
				if !errors.Is(err, fault) {
					t.Fatalf("lost failure: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("coordinator swallowed durability failure")
			}
			if len(a.startsCopy()) != 1 {
				t.Fatal("successor ran after missing durable evidence")
			}
		})
	}
}

func TestUnsupportedAndMalformedEventsInterruptAndWaitForTerminal(t *testing.T) {
	for _, kind := range []string{"unsupported-server-request", "protocol-error"} {
		t.Run(kind, func(t *testing.T) {
			r := newTestRepository()
			a := newTestAgent()
			c, stop := startTestCoordinator(t, r, a)
			defer stop()
			for i := 1; i <= 2; i++ {
				if _, err := c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(i), Text: "work"}); err != nil {
					t.Fatal(err)
				}
			}
			a.emit(AgentEvent{Kind: kind, Error: errors.New("invalid upstream")})
			eventually(t, func() bool { return r.reviewCount() == 1 })
			snap, _ := c.Snapshot(context.Background())
			if snap.Status != RoomRecovering || len(a.startsCopy()) != 1 {
				t.Fatal("unsupported event did not freeze")
			}
			a.mu.Lock()
			interrupted := len(a.interrupts)
			a.mu.Unlock()
			if interrupted != 1 {
				t.Fatal("active turn was not interrupted")
			}
			if err := c.Resolve(context.Background(), Actor{UID: 1, Name: "alice"}, RecoverInput{ClientMessageID: testID(9), TargetMessageID: testID(1), Action: RecoverySkip}); !errors.Is(err, ErrDeliveryUnknown) {
				t.Fatalf("recovery bypassed unproven live termination: %v", err)
			}
			a.emit(AgentEvent{Kind: "turn-interrupted", ThreadID: "thread-1", TurnID: "turn-1"})
			eventually(t, func() bool { return len(a.startsCopy()) == 2 })
		})
	}
}
