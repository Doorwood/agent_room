package workgroup

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"agent_romm/internal/room"
)

func TestPlanValidationAndTopology(t *testing.T) {
	c := configForTest()
	for _, text := range []string{
		`/team {"steps":[]}`,
		`/team {"steps":[{"id":"a","agent":"missing","prompt":"work"}]}`,
		`/team {"steps":[{"id":"a","agent":"builder","prompt":"work","dependsOn":["a"]}]}`,
		`/team {"steps":[{"id":"a","agent":"builder","prompt":"work","dependsOn":["missing"]}]}`,
		`/team {"steps":[{"id":"a","agent":"builder","prompt":"work","dependsOn":["b"]},{"id":"b","agent":"reviewer","prompt":"review","dependsOn":["a"]}]}`,
		`/team {"steps":[{"id":"a","agent":"builder","prompt":"work","command":"evil"}]}`,
		`/team {"steps":[{"id":"a","agent":"builder","prompt":"work"},{"id":"a","agent":"reviewer","prompt":"review"}]}`,
		`/team {"steps":[{"id":"a","agent":"builder","prompt":"work"}]} trailing`,
	} {
		if _, err := c.Plan(text); err == nil {
			t.Fatalf("accepted invalid plan %s", text)
		}
	}
	if p, e := c.Plan("quoted\n/team {}"); e != nil || p.Automatic || len(p.Steps) != 0 {
		t.Fatal(p, e)
	}
	c.Members = append(c.Members, Member{ID: "codex.b", Name: "B", Provider: "codex"})
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	p, e := c.Plan(`/team {"steps":[{"id":"review","agent":"reviewer","prompt":"review","dependsOn":["build"]},{"id":"build","agent":"codex.b","prompt":"implement"}]}`)
	if e != nil || p.Steps[0].ID != "build" || p.Steps[1].ID != "review" {
		t.Fatal(p, e)
	}
}

type gateProvider struct {
	calls   chan Assignment
	release chan error
}

func (p *gateProvider) Run(ctx context.Context, m Member, a Assignment) (Result, error) {
	p.calls <- a
	select {
	case err := <-p.release:
		return Result{Text: "result " + a.StepID}, err
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}
func nextAssignment(t *testing.T, p *gateProvider) Assignment {
	t.Helper()
	select {
	case a := <-p.calls:
		return a
	case <-time.After(3 * time.Second):
		t.Fatal("no assignment")
		return Assignment{}
	}
}
func awaitTerminal(t *testing.T, r *Runtime) string {
	t.Helper()
	for {
		select {
		case ev := <-r.Events():
			if strings.HasPrefix(ev.Kind, "turn-") || ev.Kind == "protocol-error" {
				return ev.Kind
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no terminal event")
			return ""
		}
	}
}
func TestDependencyFanInAndDurableReceipts(t *testing.T) {
	r, e := New(context.Background(), &baseAgent{events: make(chan room.AgentEvent)}, configForTest(), "/project", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	p := &gateProvider{calls: make(chan Assignment, 4), release: make(chan error)}
	r.providers["exec"] = p
	// Authored out of order; reviewer must await BOTH branches. One member does
	// two independent tasks without receiving an unrelated branch's result.
	text := `/team {"steps":[{"id":"review","agent":"reviewer","prompt":"review both","dependsOn":["code","tests"]},{"id":"code","agent":"builder","prompt":"implement"},{"id":"tests","agent":"builder","prompt":"test"}]}`
	in := room.SubmitInput{ClientMessageID: room.ClientMessageID(strings.Repeat("e", 32)), Text: text, TaskID: 4}
	turn, e := r.StartTurnFor(room.AssignmentContext(context.Background(), in), "thread", in.ClientMessageID, room.Actor{UID: 42}, "personal authorized")
	if e != nil {
		t.Fatal(e)
	}
	a := nextAssignment(t, p)
	if a.StepID != "code" || len(a.Prior) != 0 {
		t.Fatal(a)
	}
	select {
	case extra := <-p.calls:
		t.Fatalf("started downstream early: %+v", extra)
	case <-time.After(30 * time.Millisecond):
	}
	p.release <- nil
	a = nextAssignment(t, p)
	if a.StepID != "tests" || len(a.Prior) != 0 {
		t.Fatal(a)
	}
	select {
	case extra := <-p.calls:
		t.Fatalf("started review early: %+v", extra)
	case <-time.After(30 * time.Millisecond):
	}
	p.release <- nil
	a = nextAssignment(t, p)
	if a.StepID != "review" || a.StepPrompt != "review both" || len(a.Prior) != 2 || a.Prior[0].StepID != "code" || a.Prior[1].StepID != "tests" || a.HumanUID != 42 || a.TaskID != 4 || a.Prompt != "personal authorized" {
		t.Fatal(a)
	}
	p.release <- nil
	if kind := awaitTerminal(t, r); kind != "turn-completed" {
		t.Fatal(kind)
	}
	var raw string
	if e := r.db.QueryRow(`SELECT steps FROM workflows WHERE id=?`, turn).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var receipts []stepReceipt
	if e := json.Unmarshal([]byte(raw), &receipts); e != nil {
		t.Fatal(e)
	}
	for _, receipt := range receipts {
		if receipt.State != "completed" {
			t.Fatal(receipts)
		}
	}
	snapshot, e := r.ReadThread(context.Background(), "thread")
	if e != nil || len(snapshot.Turns) != 1 || len(snapshot.Turns[0].Items) != 7 {
		t.Fatal(snapshot, e)
	}
}
func TestDependencyFailureBlocksDownstream(t *testing.T) {
	for _, failure := range []error{errors.New("provider failed"), room.ErrDeliveryUnknown} {
		t.Run(failure.Error(), func(t *testing.T) {
			r, e := New(context.Background(), &baseAgent{events: make(chan room.AgentEvent)}, configForTest(), "/project", t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			defer r.Close()
			p := &gateProvider{calls: make(chan Assignment, 3), release: make(chan error)}
			r.providers["exec"] = p
			in := room.SubmitInput{ClientMessageID: room.ClientMessageID(strings.Repeat("f", 32)), Text: `/team {"steps":[{"id":"build","agent":"builder","prompt":"build"},{"id":"review","agent":"reviewer","prompt":"review","dependsOn":["build"]}]}`}
			turn, e := r.StartTurnFor(room.AssignmentContext(context.Background(), in), "thread", in.ClientMessageID, room.Actor{UID: 1}, in.Text)
			if e != nil {
				t.Fatal(e)
			}
			nextAssignment(t, p)
			p.release <- failure
			kind := awaitTerminal(t, r)
			want := "turn-failed"
			if errors.Is(failure, room.ErrDeliveryUnknown) {
				want = "protocol-error"
			}
			if kind != want {
				t.Fatal(kind)
			}
			select {
			case a := <-p.calls:
				t.Fatalf("downstream ran after failure: %+v", a)
			default:
			}
			var raw string
			r.db.QueryRow(`SELECT steps FROM workflows WHERE id=?`, turn).Scan(&raw)
			var receipts []stepReceipt
			json.Unmarshal([]byte(raw), &receipts)
			if len(receipts) != 2 || receipts[1].State != "blocked" {
				t.Fatal(raw)
			}
		})
	}
}

func TestDependencyInterruptionBlocksReview(t *testing.T) {
	r, e := New(context.Background(), &baseAgent{events: make(chan room.AgentEvent)}, configForTest(), "/project", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	p := &gateProvider{calls: make(chan Assignment, 3), release: make(chan error)}
	r.providers["exec"] = p
	in := room.SubmitInput{ClientMessageID: room.ClientMessageID(strings.Repeat("d", 32)), Text: `/team {"steps":[{"id":"build","agent":"builder","prompt":"build"},{"id":"review","agent":"reviewer","prompt":"review","dependsOn":["build"]}]}`}
	turn, e := r.StartTurnFor(room.AssignmentContext(context.Background(), in), "thread", in.ClientMessageID, room.Actor{UID: 1}, in.Text)
	if e != nil {
		t.Fatal(e)
	}
	nextAssignment(t, p)
	if e := r.InterruptTurn(context.Background(), "thread", turn); e != nil {
		t.Fatal(e)
	}
	if kind := awaitTerminal(t, r); kind != "turn-interrupted" {
		t.Fatal(kind)
	}
	select {
	case a := <-p.calls:
		t.Fatalf("review ran after interruption: %+v", a)
	default:
	}
	var raw string
	r.db.QueryRow(`SELECT steps FROM workflows WHERE id=?`, turn).Scan(&raw)
	var receipts []stepReceipt
	json.Unmarshal([]byte(raw), &receipts)
	if len(receipts) != 2 || receipts[0].State != "interrupted" || receipts[1].State != "blocked" {
		t.Fatal(raw)
	}
}
