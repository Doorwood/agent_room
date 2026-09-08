package room

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

type questionRepo struct {
	*testRepository
	muQ      sync.Mutex
	hidden   map[TurnID]bool
	recorded []CompletedItem
}

func (r *questionRepo) BeginPrivateTurn(context.Context, RoomID, ThreadID, []TurnID) error {
	return nil
}
func (r *questionRepo) EndPrivateTurn(_ context.Context, _ RoomID, _ ThreadID, turns []TurnID) error {
	r.muQ.Lock()
	defer r.muQ.Unlock()
	for _, id := range turns {
		r.hidden[id] = true
	}
	return nil
}
func (r *questionRepo) PrivateTurns(context.Context, RoomID, ThreadID, []TurnSnapshot) (map[TurnID]bool, error) {
	r.muQ.Lock()
	defer r.muQ.Unlock()
	out := map[TurnID]bool{}
	for id := range r.hidden {
		out[id] = true
	}
	return out, nil
}
func (r *questionRepo) RecordCompletedItem(_ context.Context, _ RoomID, item CompletedItem) (DurableEvent, error) {
	r.muQ.Lock()
	defer r.muQ.Unlock()
	r.recorded = append(r.recorded, item)
	return DurableEvent{}, nil
}

type questionAgent struct {
	*testAgent
	started          chan startCall
	startQuestionErr error
}

func (a *questionAgent) StartReadOnlyTurn(_ context.Context, thread ThreadID, id ClientMessageID, text string) (TurnID, error) {
	a.started <- startCall{thread, id, text}
	return "private-1", a.startQuestionErr
}
func TestQuestionSerializedAndHidden(t *testing.T) {
	repo := &questionRepo{testRepository: newTestRepository(), hidden: map[TurnID]bool{}}
	agent := &questionAgent{testAgent: newTestAgent(), started: make(chan startCall, 1)}
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()
	result := make(chan QuestionAnswer, 1)
	go func() {
		answer, err := c.Question(context.Background(), Actor{UID: 2, Name: "asker"}, testID(1), "explain project")
		if err != nil {
			t.Error(err)
		}
		result <- answer
	}()
	call := <-agent.started
	if call.thread != "thread-1" || !strings.Contains(call.text, "禁止修改") {
		t.Fatal(call)
	}
	if _, err := c.Submit(context.Background(), Actor{UID: 1, Name: "owner"}, SubmitInput{ClientMessageID: testID(2), Text: "main work"}); err != nil {
		t.Fatal(err)
	}
	if len(agent.startsCopy()) != 0 {
		t.Fatal("main dispatched during question")
	}
	item := CompletedItem{ThreadID: "thread-1", TurnID: "private-1", ItemID: "answer", Payload: json.RawMessage(`{"type":"agentMessage","text":"private answer"}`)}
	agent.emit(AgentEvent{Kind: "item-completed", Completed: &item})
	agent.emit(AgentEvent{Kind: "turn-completed", ThreadID: "thread-1", TurnID: "private-1"})
	answer := <-result
	if answer.Session != "thread-1" || answer.Text != "private answer" {
		t.Fatal(answer)
	}
	eventually(t, func() bool { return len(agent.startsCopy()) == 1 })
	repo.muQ.Lock()
	defer repo.muQ.Unlock()
	if len(repo.recorded) != 0 || !repo.hidden["private-1"] {
		t.Fatal("private output published")
	}
}
func TestQuestionCancellationDoesNotReleaseSlotUntilTerminal(t *testing.T) {
	repo := &questionRepo{testRepository: newTestRepository(), hidden: map[TurnID]bool{}}
	agent := &questionAgent{testAgent: newTestAgent(), started: make(chan startCall, 1)}
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.Question(ctx, Actor{UID: 2, Name: "asker"}, testID(3), "question"); done <- err }()
	<-agent.started
	cancel()
	if !errors.Is(<-done, context.Canceled) {
		t.Fatal("cancellation lost")
	}
	_, _ = c.Submit(context.Background(), Actor{UID: 1, Name: "owner"}, SubmitInput{ClientMessageID: testID(4), Text: "main"})
	if len(agent.startsCopy()) != 0 {
		t.Fatal("cancellation prematurely released slot")
	}
	agent.emit(AgentEvent{Kind: "turn-interrupted", ThreadID: "thread-1", TurnID: "private-1"})
	eventually(t, func() bool { return len(agent.startsCopy()) == 1 })
}
func TestRecoverySkipsPrivateTurnBeforeRecordingAnyItems(t *testing.T) {
	repo := &questionRepo{testRepository: newTestRepository(), hidden: map[TurnID]bool{"private": true}}
	c, _ := NewCoordinator("team", "/project", repo, newTestAgent(), discardSink{}, testClock{})
	c.state.threadID = "thread-1"
	snapshot := ThreadSnapshot{ID: "thread-1", Turns: []TurnSnapshot{{ID: "private", State: RequestCompleted, Items: []CompletedItem{{TurnID: "private"}}}, {ID: "main", State: RequestCompleted, Items: []CompletedItem{{TurnID: "main"}}}}}
	if err := c.reconcileHistory(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if len(repo.recorded) != 1 || repo.recorded[0].TurnID != "main" {
		t.Fatal(repo.recorded)
	}
}

func TestQuestionNotSentDoesNotFreezeFormalWork(t *testing.T) {
	repo := &questionRepo{testRepository: newTestRepository(), hidden: map[TurnID]bool{}}
	agent := &questionAgent{testAgent: newTestAgent(), started: make(chan startCall, 1), startQuestionErr: &MutationError{Operation: "turn/start", Certainty: DeliveryNotSent, Err: errors.New("unsafe extension")}}
	c, stop := startTestCoordinator(t, repo, agent)
	defer stop()
	if _, err := c.Question(context.Background(), Actor{UID: 2, Name: "asker"}, testID(1), "explain"); err == nil {
		t.Fatal("unsafe turn accepted")
	}
	if _, err := c.Submit(context.Background(), Actor{UID: 1, Name: "owner"}, SubmitInput{ClientMessageID: testID(2), Text: "formal work"}); err != nil {
		t.Fatal(err)
	}
	if len(agent.startsCopy()) != 1 {
		t.Fatal("rejected question froze queue")
	}
}
