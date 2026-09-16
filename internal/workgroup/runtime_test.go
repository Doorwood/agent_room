package workgroup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent_romm/internal/room"
	_ "modernc.org/sqlite"
)

type baseAgent struct {
	room.Agent
	events chan room.AgentEvent
}

func (b *baseAgent) Events() <-chan room.AgentEvent { return b.events }
func (b *baseAgent) ReadThread(_ context.Context, t room.ThreadID) (room.ThreadSnapshot, error) {
	return room.ThreadSnapshot{ID: t, CWD: "/project"}, nil
}
func (b *baseAgent) ResumeThread(ctx context.Context, t room.ThreadID) (room.ThreadSnapshot, error) {
	return b.ReadThread(ctx, t)
}

type recordingProvider struct {
	mu     sync.Mutex
	inputs []Assignment
	block  bool
}

func (p *recordingProvider) Run(ctx context.Context, m Member, a Assignment) (Result, error) {
	p.mu.Lock()
	p.inputs = append(p.inputs, a)
	p.mu.Unlock()
	if p.block {
		<-ctx.Done()
		return Result{}, ctx.Err()
	}
	return Result{AgentID: m.ID, Text: "result from " + m.ID}, nil
}
func configForTest() Config {
	c := Default()
	for _, id := range []string{"builder", "reviewer"} {
		c.Members = append(c.Members, Member{ID: id, Name: id, Provider: "exec", Command: []string{"/bin/true"}})
	}
	return c
}
func TestLeadingMentionsOnly(t *testing.T) {
	c := configForTest()
	for _, tt := range []struct {
		text string
		n    int
		bad  bool
	}{{"@agent:builder @agent:reviewer 修复", 2, false}, {"普通问题 @agent:builder", 0, false}, {"引用\n@agent:builder", 0, false}, {"@agent:missing 工作", 0, true}, {"@agent:builder @agent:builder 工作", 0, true}, {"@agent:builder", 0, true}, {"@agent:builder\n检查", 1, false}} {
		got, e := c.Route(tt.text)
		if (e != nil) != tt.bad || (!tt.bad && len(got) != tt.n) {
			t.Fatalf("%q: %+v %v", tt.text, got, e)
		}
	}
}
func TestSequentialHandoffAttributionAndRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	base := &baseAgent{events: make(chan room.AgentEvent, 8)}
	r, e := New(ctx, base, configForTest(), "/project", dir)
	if e != nil {
		t.Fatal(e)
	}
	p := &recordingProvider{}
	r.providers["exec"] = p
	in := room.SubmitInput{ClientMessageID: room.ClientMessageID(strings.Repeat("a", 32)), Text: `/team {"steps":[{"id":"build","agent":"builder","prompt":"work"},{"id":"review","agent":"reviewer","prompt":"review","dependsOn":["build"]}]}`, TaskID: 4}
	turn, e := r.StartTurnFor(room.AssignmentContext(ctx, in), "thread", in.ClientMessageID, room.Actor{UID: 42, Name: "human"}, "authorized prompt")
	if e != nil {
		t.Fatal(e)
	}
	var items []room.CompletedItem
	for {
		select {
		case ev := <-r.Events():
			if ev.Completed != nil {
				items = append(items, *ev.Completed)
			}
			if ev.Kind == "turn-completed" {
				goto done
			}
		case <-time.After(3 * time.Second):
			t.Fatal("stalled")
		}
	}
done:
	if len(items) != 5 {
		t.Fatal(items)
	}
	var payload map[string]any
	json.Unmarshal(items[3].Payload, &payload)
	if payload["agentId"] != "reviewer" {
		t.Fatal(payload)
	}
	p.mu.Lock()
	if len(p.inputs) != 2 || len(p.inputs[1].Prior) != 1 || p.inputs[1].Prior[0].AgentID != "builder" || p.inputs[1].HumanUID != 42 || p.inputs[1].TaskID != 4 {
		t.Fatal(p.inputs)
	}
	p.mu.Unlock()
	r.Close()
	recovered, e := New(ctx, base, configForTest(), "/project", dir)
	if e != nil {
		t.Fatal(e)
	}
	defer recovered.Close()
	h, e := recovered.ReadThread(ctx, "thread")
	if e != nil || len(h.Turns) != 1 || h.Turns[0].ID != turn || h.Turns[0].State != room.RequestCompleted || len(h.Turns[0].Items) != 5 {
		t.Fatal(h, e)
	}
	if _, e = recovered.StartTurnFor(room.AssignmentContext(ctx, in), "thread", in.ClientMessageID, room.Actor{UID: 42}, "again"); e == nil {
		t.Fatal("duplicate assignment ran again")
	}
}
func TestCancellationStopsBeforeNextWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, e := New(ctx, &baseAgent{events: make(chan room.AgentEvent)}, configForTest(), "/project", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	p := &recordingProvider{block: true}
	r.providers["exec"] = p
	in := room.SubmitInput{ClientMessageID: room.ClientMessageID(strings.Repeat("b", 32)), Text: `/team {"steps":[{"id":"build","agent":"builder","prompt":"work"},{"id":"review","agent":"reviewer","prompt":"review","dependsOn":["build"]}]}`}
	turn, e := r.StartTurnFor(room.AssignmentContext(ctx, in), "thread", in.ClientMessageID, room.Actor{UID: 1}, in.Text)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.InterruptTurn(ctx, "thread", turn); e != nil {
		t.Fatal(e)
	}
	for {
		select {
		case ev := <-r.Events():
			if ev.Kind == "turn-interrupted" {
				goto stopped
			}
		case <-time.After(time.Second):
			t.Fatal("cancel stuck")
		}
	}
stopped:
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.inputs) > 1 {
		t.Fatal("next worker started after cancellation")
	}
}
func TestRunningJournalRemainsUnprovenAfterRestart(t *testing.T) {
	ctx := context.Background()
	r, e := New(ctx, &baseAgent{events: make(chan room.AgentEvent)}, Default(), "/project", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	_, e = r.db.Exec(`INSERT INTO runs(id,thread,state)VALUES('workgroup-crashed','thread','running')`)
	if e != nil {
		t.Fatal(e)
	}
	h, e := r.ReadThread(ctx, "thread")
	if e != nil || h.Turns[0].State != room.RequestRunning {
		t.Fatal(h, e)
	}
}
func TestExecProtocolAndNoInheritedCredentials(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "not-for-workers")
	dir := t.TempDir()
	path := filepath.Join(dir, "worker")
	code := "#!/bin/sh\nif [ -n \"$GITHUB_TOKEN\" ]; then exit 1; fi\ncat >/dev/null\nprintf '%s' '{\"text\":\"ok\"}'\n"
	if e := os.WriteFile(path, []byte(code), 0700); e != nil {
		t.Fatal(e)
	}
	got, e := (ExecProvider{}).Run(context.Background(), Member{ID: "worker", Command: []string{path}}, Assignment{Version: 1, ProjectRoot: dir})
	if e != nil || got.Text != "ok" {
		t.Fatal(got, e)
	}
}

type isolatedCodex struct {
	baseAgent
	mu      sync.Mutex
	threads []room.ThreadID
	prompts []string
}

func (b *isolatedCodex) StartThread(context.Context) (room.ThreadSnapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := room.ThreadID("isolated-" + string(rune('a'+len(b.threads))))
	b.threads = append(b.threads, id)
	return room.ThreadSnapshot{ID: id, CWD: "/project"}, nil
}
func (b *isolatedCodex) StartTurn(_ context.Context, t room.ThreadID, _ room.ClientMessageID, text string) (room.TurnID, error) {
	b.mu.Lock()
	b.prompts = append(b.prompts, text)
	b.mu.Unlock()
	id := room.TurnID(string(t) + "-turn")
	payload, _ := json.Marshal(map[string]string{"type": "agentMessage", "phase": "final_answer", "text": "answer-" + string(t)})
	b.events <- room.AgentEvent{Kind: "item-completed", Completed: &room.CompletedItem{ThreadID: t, TurnID: id, ItemID: "final", Payload: payload}}
	b.events <- room.AgentEvent{Kind: "turn-completed", ThreadID: t, TurnID: id}
	return id, nil
}
func TestCodexMembersUseSeparateThreadsAndExplicitHandoff(t *testing.T) {
	ctx := context.Background()
	base := &isolatedCodex{baseAgent: baseAgent{events: make(chan room.AgentEvent, 16)}}
	c := Default()
	c.Members = append(c.Members, Member{ID: "review", Name: "Review", Provider: "codex"})
	r, e := New(ctx, base, c, "/project", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	in := room.SubmitInput{ClientMessageID: room.ClientMessageID(strings.Repeat("c", 32)), Text: `/team {"steps":[{"id":"build","agent":"codex","prompt":"work"},{"id":"review","agent":"review","prompt":"review","dependsOn":["build"]}]}`}
	_, e = r.StartTurnFor(room.AssignmentContext(ctx, in), "main", in.ClientMessageID, room.Actor{UID: 42}, "original human request")
	if e != nil {
		t.Fatal(e)
	}
	for {
		select {
		case ev := <-r.Events():
			if ev.Kind == "turn-completed" {
				goto done
			}
			if ev.Kind == "turn-failed" || ev.Kind == "protocol-error" {
				t.Fatal(ev)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("stalled")
		}
	}
done:
	base.mu.Lock()
	defer base.mu.Unlock()
	if len(base.threads) != 2 || base.threads[0] == base.threads[1] || base.threads[0] == "main" {
		t.Fatal(base.threads)
	}
	if !strings.Contains(base.prompts[1], "answer-isolated-a") || strings.Contains(base.prompts[0], "answer-isolated-a") {
		t.Fatal("incorrect handoff")
	}
}
func TestProviderRegistrationIsExplicit(t *testing.T) {
	c := Default()
	c.Members = append(c.Members, Member{ID: "remote", Name: "Remote", Provider: "custom"})
	base := &baseAgent{events: make(chan room.AgentEvent)}
	if _, e := New(context.Background(), base, c, "/project", t.TempDir()); e == nil {
		t.Fatal("unknown provider accepted")
	}
	r, e := NewWithProviders(context.Background(), base, c, "/project", t.TempDir(), map[string]Provider{"custom": &recordingProvider{}})
	if e != nil {
		t.Fatal(e)
	}
	r.Close()
}
func TestConfigurationRoundTripAndPrivacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	c := configForTest()
	if e := Save(path, c); e != nil {
		t.Fatal(e)
	}
	loaded, e := Load(path)
	if e != nil || len(loaded.Members) != 3 {
		t.Fatal(loaded, e)
	}
	os.Chmod(path, 0644)
	if _, e = Load(path); e == nil {
		t.Fatal("public config accepted")
	}
}

func TestExecCancellationKillsBackgroundProcessGroup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker")
	marker := filepath.Join(dir, "escaped")
	// No shell interpolation of the marker: pass it as a fixed positional argument.
	code := "#!/bin/sh\n(sleep 1; touch \"$1\") &\nwait\n"
	if e := os.WriteFile(path, []byte(code), 0700); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, e := (ExecProvider{}).Run(ctx, Member{ID: "worker", Command: []string{path, marker}}, Assignment{ProjectRoot: dir})
	if e == nil {
		t.Fatal("cancelled worker reported success")
	}
	time.Sleep(1100 * time.Millisecond)
	if _, e = os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("background worker survived cancellation")
	}
}

type providerFunc func(context.Context, Member, Assignment) (Result, error)

func (f providerFunc) Run(ctx context.Context, m Member, a Assignment) (Result, error) {
	return f(ctx, m, a)
}
func TestUncertainWorkerDoesNotCompleteOrStartNextMember(t *testing.T) {
	ctx := context.Background()
	calls := 0
	c := configForTest()
	r, e := NewWithProviders(ctx, &baseAgent{events: make(chan room.AgentEvent)}, c, "/project", t.TempDir(), map[string]Provider{"exec": providerFunc(func(context.Context, Member, Assignment) (Result, error) {
		calls++
		return Result{}, room.ErrDeliveryUnknown
	})})
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	in := room.SubmitInput{ClientMessageID: room.ClientMessageID(strings.Repeat("e", 32)), Text: `/team {"steps":[{"id":"build","agent":"builder","prompt":"work"},{"id":"review","agent":"reviewer","prompt":"review","dependsOn":["build"]}]}`}
	_, e = r.StartTurnFor(room.AssignmentContext(ctx, in), "thread", in.ClientMessageID, room.Actor{UID: 42}, in.Text)
	if e != nil {
		t.Fatal(e)
	}
	for {
		select {
		case ev := <-r.Events():
			if ev.Kind == "protocol-error" {
				goto done
			}
			if strings.HasPrefix(ev.Kind, "turn-") {
				t.Fatal("uncertain work reported terminal", ev)
			}
		case <-time.After(time.Second):
			t.Fatal("stalled")
		}
	}
done:
	if calls != 1 {
		t.Fatal("next worker ran after uncertainty")
	}
	h, e := r.ReadThread(ctx, "thread")
	if e != nil || h.Turns[0].State != room.RequestRunning {
		t.Fatal(h, e)
	}
}
