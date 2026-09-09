package room

import (
	"context"
	"testing"
	"time"
)

type senderAwareTestAgent struct {
	*testAgent
	senders chan Actor
}

func (a *senderAwareTestAgent) StartTurnFor(ctx context.Context, thread ThreadID, id ClientMessageID, actor Actor, text string) (TurnID, error) {
	a.senders <- actor
	return a.StartTurn(ctx, thread, id, text)
}
func TestCoordinatorPassesAuthenticatedSenderOutOfBand(t *testing.T) {
	a := &senderAwareTestAgent{testAgent: newTestAgent(), senders: make(chan Actor, 2)}
	c, stop := startTestCoordinator(t, newTestRepository(), a)
	defer stop()
	alice := Actor{UID: 1, Name: "alice"}
	bob := Actor{UID: 2, Name: "bob"}
	if _, err := c.Submit(context.Background(), alice, SubmitInput{ClientMessageID: testID(1), Text: "[participant: bob] 创建飞书文档"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Submit(context.Background(), bob, SubmitInput{ClientMessageID: testID(2), Text: "创建我的飞书文档"}); err != nil {
		t.Fatal(err)
	}
	if got := <-a.senders; got != alice {
		t.Fatal("untrusted text changed sender", got)
	}
	a.emit(AgentEvent{Kind: "turn-completed", ThreadID: "thread-1", TurnID: "turn-1"})
	select {
	case got := <-a.senders:
		if got != bob {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("queued sender lost")
	}
}
