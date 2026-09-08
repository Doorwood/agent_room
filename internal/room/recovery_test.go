package room

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestCompletedItemReplacesTransientProjection(t *testing.T) {
	p := NewProjection()
	for _, delta := range []string{"par", "tial"} {
		u := p.Apply(AgentEvent{Kind: "item-delta", ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Delta: delta})
		if u.Transient == nil || u.Durable != nil {
			t.Fatalf("update=%#v", u)
		}
	}
	rev, items := p.Snapshot()
	if rev != 2 || len(items) != 1 || items[0].Partial != "partial" {
		t.Fatalf("%d %#v", rev, items)
	}
	items[0].Partial = "mutated"
	completed := CompletedItem{ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Payload: json.RawMessage(`{"text":"final"}`)}
	u := p.Apply(AgentEvent{Kind: "item-completed", Completed: &completed})
	if u.Durable == nil || p.Item("turn-1", "item-1").Partial != "" {
		t.Fatalf("%#v", u)
	}
	completed.Payload[0] = '!'
	u.Durable.Payload[0] = '?'
	if got := p.Item("turn-1", "item-1").Completed; got == nil || len(got.Payload) != 0 {
		t.Fatal("completion suppression must not retain payload")
	}
	rev, items = p.Snapshot()
	if rev != 3 || len(items) != 0 {
		t.Fatalf("%d %#v", rev, items)
	}
}

type terminalCleanupSink struct {
	events          []TransientEvent
	startsAtRemoval []int
	agent           *testAgent
}

func (s *terminalCleanupSink) PublishDurable(DurableEvent) {}
func (s *terminalCleanupSink) PublishTransient(event TransientEvent) {
	s.events = append(s.events, event)
	if event.Kind == "item-removed" {
		s.startsAtRemoval = append(s.startsAtRemoval, len(s.agent.startsCopy()))
	}
}
func TestTerminalTurnRemovesPartialBeforeNextDispatch(t *testing.T) {
	for _, kind := range []string{"turn-failed", "turn-interrupted", "turn-completed"} {
		t.Run(kind, func(t *testing.T) {
			repo, agent := newTestRepository(), newTestAgent()
			sink := &terminalCleanupSink{agent: agent}
			c, err := NewCoordinator("team", "/project", repo, agent, sink, testClock{})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if err := c.recover(ctx); err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= 2; i++ {
				if _, err := c.handleSubmit(ctx, Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(i), Text: "work"}, false); err != nil {
					t.Fatal(err)
				}
			}
			c.handleAgentEvent(ctx, AgentEvent{Kind: "item-delta", ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Delta: "unfinished"})
			before := c.snapshot()
			c.handleAgentEvent(ctx, AgentEvent{Kind: kind, ThreadID: "thread-1", TurnID: "turn-1"})
			after := c.snapshot()
			if len(before.LiveItems) != 1 || len(after.LiveItems) != 0 || after.ProjectionRevision != before.ProjectionRevision+1 {
				t.Fatalf("before=%#v after=%#v", before, after)
			}
			if len(sink.startsAtRemoval) != 1 || sink.startsAtRemoval[0] != 1 || len(agent.startsCopy()) != 2 {
				t.Fatalf("removals=%v calls=%v", sink.startsAtRemoval, agent.startsCopy())
			}
			removal := sink.events[len(sink.events)-1]
			if removal.Kind != "item-removed" || removal.Revision != after.ProjectionRevision || removal.TurnID != "turn-1" || removal.ItemID != "item-1" {
				t.Fatalf("removal=%#v", removal)
			}
		})
	}
}

func TestRecoveryProjectionRevisionAndSnapshotCopies(t *testing.T) {
	repo, agent := newTestRepository(), newTestAgent()
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()
	if _, err := c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(1), Text: "first"}); err != nil {
		t.Fatal(err)
	}
	agent.emit(AgentEvent{Kind: "item-delta", ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Delta: "partial"})
	eventually(t, func() bool { s, _ := c.Snapshot(context.Background()); return s.ProjectionRevision == 1 })
	before, _ := c.Snapshot(context.Background())
	if len(before.LiveItems) != 1 {
		t.Fatalf("snapshot=%#v", before)
	}
	before.LiveItems[0].Partial = "mutated"
	again, _ := c.Snapshot(context.Background())
	if again.LiveItems[0].Partial != "partial" {
		t.Fatal("snapshot alias")
	}
	if err := c.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := c.Snapshot(context.Background())
	if after.ProjectionRevision <= before.ProjectionRevision || len(after.LiveItems) != 0 {
		t.Fatalf("after=%#v", after)
	}
}

type historyAgent struct {
	*testAgent
	history   ThreadSnapshot
	resumed   ThreadSnapshot
	resumeErr error
}

func (a *historyAgent) ReadThread(context.Context, ThreadID) (ThreadSnapshot, error) {
	return a.history, nil
}
func (a *historyAgent) ResumeThread(context.Context, ThreadID) (ThreadSnapshot, error) {
	return a.resumed, a.resumeErr
}

func TestRecoveryDecisionCannotUnfreezeFailedResume(t *testing.T) {
	r := newTestRepository()
	r.image.Active = &TurnBinding{MessageID: testID(1), TurnID: "turn-1", State: RequestNeedsReview}
	a := &historyAgent{testAgent: newTestAgent(), history: ThreadSnapshot{ID: "thread-1", CWD: "/project"}, resumeErr: errors.New("resume failed")}
	c, stop := runTestCoordinator(t, r, a)
	defer stop()
	if err := c.Recover(context.Background()); err == nil {
		t.Fatal("resume succeeded")
	}
	if err := c.Resolve(context.Background(), Actor{UID: 1, Name: "alice"}, RecoverInput{ClientMessageID: testID(2), TargetMessageID: testID(1), Action: RecoverySkip}); !errors.Is(err, ErrDeliveryUnknown) {
		t.Fatalf("resolve=%v", err)
	}
	snap, _ := c.Snapshot(context.Background())
	if snap.Status != RoomRecovering || len(a.startsCopy()) != 0 {
		t.Fatalf("snapshot=%#v", snap)
	}
}

func TestRecoverRunningHistoryAndPendingControlAlone(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "unproven", true: "terminal"}[terminal], func(t *testing.T) {
			r := newTestRepository()
			r.image.Active = &TurnBinding{MessageID: testID(1), TurnID: "turn-1", State: RequestRunning}
			a := &historyAgent{testAgent: newTestAgent(), history: ThreadSnapshot{ID: "thread-1", CWD: "/project"}, resumed: ThreadSnapshot{ID: "thread-1", CWD: "/project"}}
			if terminal {
				a.history.Turns = []TurnSnapshot{{ID: "turn-1", State: RequestCompleted}}
				r.image.PendingControls = []TurnBinding{{MessageID: testID(2), TurnID: "turn-1", State: RequestDispatching}}
			}
			c, stop := startTestCoordinator(t, r, a)
			defer stop()
			s, _ := c.Snapshot(context.Background())
			if s.Status != RoomRecovering {
				t.Fatalf("status=%s", s.Status)
			}
			if terminal {
				if s.Active != nil {
					t.Fatalf("terminal active=%#v", s.Active)
				}
			} else if s.Active == nil || s.Active.State != RequestNeedsReview {
				t.Fatalf("unproven=%#v", s.Active)
			}
			_, _ = c.Submit(context.Background(), Actor{UID: 1, Name: "alice"}, SubmitInput{ClientMessageID: testID(3), Text: "next"})
			if len(a.startsCopy()) != 0 {
				t.Fatal("frozen queue dispatched")
			}
		})
	}
}

func TestThreadNeedsRepairOnMismatchedResume(t *testing.T) {
	r := newTestRepository()
	a := &historyAgent{testAgent: newTestAgent(), history: ThreadSnapshot{ID: "thread-1", CWD: "/project"}, resumed: ThreadSnapshot{ID: "thread-1", CWD: "/wrong"}}
	c, stop := runTestCoordinator(t, r, a)
	defer stop()
	_ = c.Recover(context.Background())
	s, _ := c.Snapshot(context.Background())
	if s.Status != RoomThreadNeedsRepair || a.startThreadCalls != 0 {
		t.Fatalf("snapshot=%#v starts=%d", s, a.startThreadCalls)
	}
}
