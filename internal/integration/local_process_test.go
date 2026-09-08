package integration

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"agent_romm/internal/admin"
	"agent_romm/internal/cli"
	"agent_romm/internal/client"
	"agent_romm/internal/codex"
	"agent_romm/internal/config"
	"agent_romm/internal/identity"
	"agent_romm/internal/protocol"
	"agent_romm/internal/room"
	"agent_romm/internal/store"
)

type fixture struct {
	Root, State, Control, Log, Binary, App, SSH, Home, PIDFile, Cursor, Version string
	PriorLive                                                                   bool
	PeerOffsets                                                                 []int
}
type peerOrder struct {
	mu      sync.Mutex
	next    int
	offsets []int
}

func (p *peerOrder) Resolve(net.Conn) (identity.Peer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := p.next
	p.next++
	if n < len(p.offsets) {
		n = p.offsets[n]
	} else if n > 2 {
		n = 0
	}
	return identity.Peer{UID: room.UID(os.Geteuid() + n)}, nil
}
func adminDeps() admin.Dependencies {
	d := admin.DefaultDependencies()
	d.LookupUser = func(name string) (*user.User, error) {
		for i, n := range []string{"alice", "bob", "carol"} {
			if n == name {
				return &user.User{Uid: strconv.Itoa(os.Geteuid() + i), Gid: strconv.Itoa(os.Getgid()), Username: n}, nil
			}
		}
		return nil, fmt.Errorf("unknown fixture account")
	}
	d.LookupGroup = func(string) (*user.Group, error) {
		return &user.Group{Gid: strconv.Itoa(os.Getgid()), Name: "team"}, nil
	}
	d.GroupIDs = func(*user.User) ([]string, error) { return []string{strconv.Itoa(os.Getgid())}, nil }
	d.ProcessIdentity = func(pid int) (string, error) {
		if err := syscall.Kill(pid, 0); err != nil {
			return "", err
		}
		return fmt.Sprintf("fixture-%d", pid), nil
	}
	d.Probe = func(context.Context, string, string) (room.RuntimeCheckpoint, error) {
		return room.RuntimeCheckpoint{State: room.RuntimeProcessStopped, Generation: "fixture-generation", ProcessStart: "fixture-stopped", CodexVersion: codex.SupportedCLIOutput, SchemaSHA256: codex.SupportedSchemaSHA256}, nil
	}
	return d
}

type processes struct {
	f        fixture
	mu       sync.Mutex
	children map[*codex.Child]*exec.Cmd
}

func (p *processes) Version(context.Context, string) (string, error) {
	if p.f.Version != "" {
		return p.f.Version, nil
	}
	return codex.SupportedCLIOutput, nil
}
func (p *processes) Match(context.Context, codex.ProcessIdentity) (codex.ProcessMatch, error) {
	if p.f.PriorLive {
		return codex.ProcessMatches, nil
	}
	return codex.ProcessAbsent, nil
}
func (p *processes) Start(ctx context.Context, s codex.ProcessSpec) (*codex.Child, error) {
	if s.Executable != "codex" {
		return nil, fmt.Errorf("unexpected executable")
	}
	cmd := exec.Command(p.f.App, append(s.Args, "--record", p.f.Log, "--control-dir", p.f.Control)...)
	cmd.Dir = s.Dir
	inR, inW, e := os.Pipe()
	if e != nil {
		return nil, e
	}
	outR, outW, e := os.Pipe()
	if e != nil {
		return nil, e
	}
	errR, errW, e := os.Pipe()
	if e != nil {
		return nil, e
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	if e = cmd.Start(); e != nil {
		return nil, e
	}
	inR.Close()
	outW.Close()
	errW.Close()
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	child := &codex.Child{Identity: codex.ProcessIdentity{Generation: s.Generation, PID: cmd.Process.Pid, PGID: cmd.Process.Pid, StartIdentity: fmt.Sprintf("fixture-%d", cmd.Process.Pid)}, Stdin: inW, Stdout: outR, Stderr: errR, Done: done, WaitErr: func() error { <-done; return waitErr }}
	p.mu.Lock()
	if p.children == nil {
		p.children = map[*codex.Child]*exec.Cmd{}
	}
	p.children[child] = cmd
	p.mu.Unlock()
	return child, nil
}
func (p *processes) StopCurrent(ctx context.Context, c *codex.Child, _ time.Duration) error {
	p.mu.Lock()
	cmd := p.children[c]
	p.mu.Unlock()
	if cmd == nil {
		return errors.New("foreign fixture child")
	}
	select {
	case <-c.Done:
		return nil
	default:
	}
	_ = cmd.Process.Kill()
	select {
	case <-c.Done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type sshLauncher struct{ f fixture }

func (s sshLauncher) Start(ctx context.Context, target string) (*client.Connection, error) {
	cmd := exec.CommandContext(ctx, s.f.SSH, s.f.Binary, s.f.Home, s.f.PIDFile)
	in, e := cmd.StdinPipe()
	if e != nil {
		return nil, e
	}
	out, childOut, e := os.Pipe()
	if e != nil {
		return nil, e
	}
	cmd.Stdout = childOut
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		return nil, e
	}
	childOut.Close()
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	return &client.Connection{Reader: out, Writer: in, CloseFunc: func() error {
		select {
		case <-done:
		default:
			_ = cmd.Process.Kill()
		}
		<-done
		return nil
	}, WaitFunc: func() error { <-done; return waitErr }}, nil
}
func TestAgentRommHelperProcess(t *testing.T) {
	at := -1
	for i, arg := range os.Args {
		if arg == "--" {
			at = i
			break
		}
	}
	if at < 0 {
		return
	}
	if len(os.Args) != at+3 {
		os.Exit(2)
	}
	role, path := os.Args[at+1], os.Args[at+2]
	var f fixture
	data, e := os.ReadFile(path)
	if e != nil {
		os.Exit(2)
	}
	if json.Unmarshal(data, &f) != nil {
		os.Exit(2)
	}
	d := cli.ProductionDependencies()
	d.Admin = adminDeps()
	d.CheckPlatform = func() error { return nil }
	d.Processes = &processes{f: f}
	d.Peers = &peerOrder{offsets: f.PeerOffsets}
	d.Client = client.Deps{Launcher: sshLauncher{f}, Cursors: client.FileCursors{Directory: f.Cursor}}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var args []string
	switch role {
	case "room-server":
		args = []string{"serve", "--state-dir", f.State}
	case "room-client":
		args = []string{"connect", "fixture-host"}
	default:
		os.Exit(2)
	}
	os.Exit(cli.Run(ctx, args, os.Stdout, os.Stderr, d))
}

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *safeBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

type childProcess struct {
	cmd              *exec.Cmd
	input            io.WriteCloser
	out, diagnostics *safeBuffer
	done             chan error
}

func startHelper(t *testing.T, role string, f fixture) *childProcess {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.json")
	writeJSON(t, path, f)
	cmd := exec.Command(os.Args[0], "-test.run=^TestAgentRommHelperProcess$", "--", role, path)
	input, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	p := &childProcess{cmd: cmd, input: input, out: &safeBuffer{}, diagnostics: &safeBuffer{}, done: make(chan error, 1)}
	cmd.Stdout = p.out
	cmd.Stderr = p.diagnostics
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = input.Close()
		_ = cmd.Process.Kill()
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			t.Error("helper cleanup timeout")
		}
	})
	return p
}
func (p *childProcess) send(t *testing.T, line string) {
	t.Helper()
	if _, e := fmt.Fprintln(p.input, line); e != nil {
		t.Fatal(e)
	}
}
func (p *childProcess) stop(t *testing.T) {
	t.Helper()
	if e := p.cmd.Process.Signal(syscall.SIGTERM); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-p.done:
		p.done <- e
		if e != nil {
			t.Fatalf("stop: %v %s", e, p.diagnostics.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("stop timeout %s", p.diagnostics.String())
	}
}
func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
}
func eventually(t *testing.T, desc string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout: " + desc)
}
func build(t *testing.T, pkg, output string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", output, pkg)
	cmd.Dir = "../.."
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("build: %v %s", e, out)
	}
}
func setup(t *testing.T) fixture {
	t.Helper()
	short, e := os.MkdirTemp("/tmp", "ar-process-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	root, e := filepath.EvalSymlinks(short)
	if e != nil {
		t.Fatal(e)
	}
	f := fixture{Root: filepath.Join(root, "project"), State: filepath.Join(root, "state"), Control: filepath.Join(root, "control"), Log: filepath.Join(root, "requests.jsonl"), Binary: filepath.Join(root, "agent_romm"), App: filepath.Join(root, "fakeappserver"), SSH: filepath.Join(root, "fakessh")}
	for _, dir := range []string{f.Root, f.Control} {
		if e = os.Mkdir(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	cmd := exec.Command("git", "init", f.Root)
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("git: %v %s", e, out)
	}
	build(t, "./cmd/agent_romm", f.Binary)
	build(t, "./internal/codex/testdata/fakeappserver", f.App)
	build(t, "./internal/integration/testdata/fakessh", f.SSH)
	var out, diagnostics bytes.Buffer
	d := cli.ProductionDependencies()
	d.Admin = adminDeps()
	args := []string{"init", "--state-dir", f.State, "--project", f.Root, "--room", "demo", "--execution-owner", "alice", "--shared-group", "team", "--members", "alice,bob,carol", "--full-owner-access"}
	if code := cli.Run(context.Background(), args, &out, &diagnostics, d); code != 0 {
		_, cause := admin.InitWithDependencies(context.Background(), d.Admin, admin.InitOptions{Project: f.Root, StateDir: f.State, Room: "demo", ExecutionOwner: "alice", SharedGroup: "team", Members: []string{"alice", "bob", "carol"}, FullOwnerAccess: true})
		t.Fatalf("init %d %s (diagnostic retry: %v)", code, diagnostics.String(), cause)
	}
	return f
}
func cfg(t *testing.T, f fixture) config.RuntimeConfig {
	t.Helper()
	c, e := config.Read(filepath.Join(f.State, "private", "config.json"))
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func image(t *testing.T, f fixture) room.RecoveryImage {
	t.Helper()
	c := cfg(t, f)
	s, e := store.Open(context.Background(), c.DatabasePath)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	im, e := s.LoadRecoveryImage(context.Background(), c.RoomID)
	if e != nil {
		t.Fatal(e)
	}
	return im
}
func latest(t *testing.T, f fixture) room.Seq {
	t.Helper()
	c := cfg(t, f)
	s, e := store.Open(context.Background(), c.DatabasePath)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	seq, e := s.LatestSeq(context.Background(), c.RoomID)
	if e != nil {
		t.Fatal(e)
	}
	return seq
}
func events(t *testing.T, f fixture) []room.DurableEvent {
	t.Helper()
	c := cfg(t, f)
	s, e := store.Open(context.Background(), c.DatabasePath)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	seq, e := s.LatestSeq(context.Background(), c.RoomID)
	if e != nil {
		t.Fatal(e)
	}
	es, e := s.Events(context.Background(), c.RoomID, 0, seq, 1000)
	if e != nil {
		t.Fatal(e)
	}
	return es
}
func calls(t *testing.T, f fixture, method string) int {
	t.Helper()
	data, e := os.ReadFile(f.Log)
	if os.IsNotExist(e) {
		return 0
	}
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var v struct{ Method string }
		if json.Unmarshal(line, &v) == nil && v.Method == method {
			count++
		}
	}
	return count
}
func control(t *testing.T, f fixture, name string) {
	t.Helper()
	if e := os.WriteFile(filepath.Join(f.Control, name), nil, 0600); e != nil {
		t.Fatal(e)
	}
}
func startServer(t *testing.T, f fixture) *childProcess {
	t.Helper()
	p := startHelper(t, "room-server", f)
	eventually(t, "server socket "+f.State, func() bool {
		select {
		case e := <-p.done:
			p.done <- e
			data, _ := os.ReadFile(f.Log)
			t.Fatalf("server exit %v: %s requests=%s image=%+v", e, p.diagnostics.String(), data, image(t, f))
		default:
		}
		_, e := os.Stat(filepath.Join(f.State, "room.sock"))
		return e == nil
	})
	return p
}
func startClient(t *testing.T, f fixture, name string) (*childProcess, fixture) {
	t.Helper()
	f.Home = filepath.Join(t.TempDir(), name)
	f.PIDFile = filepath.Join(f.Home, "bridge.pid")
	f.Cursor = filepath.Join(f.Home, "cursors")
	dir := filepath.Join(f.Home, ".config")
	if runtime.GOOS == "darwin" {
		dir = filepath.Join(f.Home, "Library", "Application Support")
	}
	dir = filepath.Join(dir, "agent_romm")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	writeJSON(t, filepath.Join(dir, "bridge.json"), map[string]string{"socketPath": filepath.Join(f.State, "room.sock")})
	p := startHelper(t, "room-client", f)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("client %s output=%.3000s diagnostic=%.3000s", name, p.out.String(), p.diagnostics.String())
		}
	})
	eventually(t, "client welcome "+name, func() bool { return strings.Contains(p.out.String(), "Room: ") })
	return p, f
}

var sequenceLine = regexp.MustCompile(`(?m)^\[([0-9]+) [^\n]+`)

func sequences(s string) []string {
	matches := sequenceLine.FindAllStringSubmatch(s, -1)
	out := []string{}
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

func TestLocalThreeClientProcesses(t *testing.T) {
	f := setup(t)
	// Alice, Bob, Carol, a stale-control probe, then Carol's bridge reconnect.
	f.PeerOffsets = []int{0, 1, 2, 0, 2}
	server := startServer(t, f)
	alice, _ := startClient(t, f, "alice")
	bob, _ := startClient(t, f, "bob")
	carol, cf := startClient(t, f, "carol")
	alice.send(t, "alice prompt")
	eventually(t, "first active", func() bool { im := image(t, f); return im.Active != nil && im.Active.State == room.RequestRunning })
	bob.send(t, "bob prompt")
	eventually(t, "bob queued", func() bool { return len(image(t, f).Queue) == 1 })
	carol.send(t, "carol prompt")
	eventually(t, "carol queued", func() bool { return len(image(t, f).Queue) == 2 })
	im := image(t, f)
	if im.Queue[0].Actor.Name != "bob" || im.Queue[1].Actor.Name != "carol" {
		t.Fatalf("FIFO: %+v", im.Queue)
	}
	if calls(t, f, "turn/start") != 1 {
		t.Fatal("multiple active turns")
	}
	bob.send(t, "/note not sent to model")
	eventually(t, "note broadcast", func() bool {
		return strings.Contains(alice.out.String(), "not sent to model") && strings.Contains(bob.out.String(), "not sent to model") && strings.Contains(carol.out.String(), "not sent to model")
	})
	data, _ := os.ReadFile(f.Log)
	if bytes.Contains(data, []byte("not sent to model")) {
		t.Fatal("note sent to App Server")
	}
	bob.send(t, "/steer change direction")
	carol.send(t, "/cancel")
	eventually(t, "current controls", func() bool { return calls(t, f, "turn/steer") == 1 && calls(t, f, "turn/interrupt") == 1 })
	stale := dialProtocol(t, f)
	request(t, stale, "steer", protocol.SteerRequest{ClientMessageID: strings.Repeat("a", 32), ExpectedTurnID: "turn-stale", Text: "stale"})
	request(t, stale, "cancel", protocol.CancelRequest{ClientMessageID: strings.Repeat("b", 32), ExpectedTurnID: "turn-stale"})
	stale.Close()
	if calls(t, f, "turn/steer") != 1 || calls(t, f, "turn/interrupt") != 1 {
		t.Fatal("stale control reached model")
	}
	for _, p := range []*childProcess{alice, bob, carol} {
		eventually(t, "transient", func() bool { return strings.Contains(p.out.String(), "[partial item-1]") })
	}
	seq := latest(t, f)
	for _, p := range []*childProcess{alice, bob, carol} {
		eventually(t, "same durable sequence", func() bool { ss := sequences(p.out.String()); return len(ss) > 0 && ss[len(ss)-1] == fmt.Sprint(seq) })
	}
	if !reflect.DeepEqual(sequences(alice.out.String()), sequences(bob.out.String())) || !reflect.DeepEqual(sequences(alice.out.String()), sequences(carol.out.String())) {
		t.Fatal("durable streams differ")
	}
	pidData, e := os.ReadFile(cf.PIDFile)
	if e != nil {
		t.Fatal(e)
	}
	pid, e := strconv.Atoi(string(pidData))
	if e != nil {
		t.Fatal(e)
	}
	if e = syscall.Kill(pid, syscall.SIGKILL); e != nil {
		t.Fatal(e)
	}
	control(t, f, "finish")
	eventually(t, "next FIFO active", func() bool { im := image(t, f); return im.Active != nil && im.Active.TurnID == "turn-2" })
	eventually(t, "bridge reconnect replay", func() bool { return strings.Contains(carol.out.String(), "completed turn-1") })
	eventually(t, "gap fully replayed", func() bool { return reflect.DeepEqual(sequences(alice.out.String()), sequences(carol.out.String())) })
	carol.send(t, "/note carol after reconnect")
	eventually(t, "reconnected Carol attribution", func() bool {
		for _, e := range events(t, f) {
			if bytes.Contains(e.Payload, []byte("carol after reconnect")) {
				return e.ActorUID == room.UID(os.Geteuid()+2)
			}
		}
		return false
	})
	for _, p := range []*childProcess{alice, bob, carol} {
		eventually(t, "one completed projection", func() bool { return strings.Count(p.out.String(), " item/completed ") == 1 })
	}
	before := image(t, f)
	beforeSeq := latest(t, f)
	for _, p := range []*childProcess{alice, bob, carol} {
		p.send(t, "/quit")
	}
	server.stop(t)
	stopped := image(t, f)
	if stopped.Checkpoint.State != room.RuntimeProcessStopped || stopped.Checkpoint.PID != 0 || stopped.Checkpoint.PGID != 0 {
		t.Fatalf("checkpoint %+v", stopped.Checkpoint)
	}
	restarted := startServer(t, f)
	after := image(t, f)
	if after.ThreadID != before.ThreadID || len(after.Queue) != len(before.Queue) || latest(t, f) < beforeSeq {
		t.Fatalf("restart changed state %+v", after)
	}
	found := false
	for _, ev := range events(t, f) {
		if ev.Kind == "item/completed" {
			found = true
		}
	}
	if !found {
		t.Fatal("lost completed history")
	}
	if calls(t, f, "turn/start") != 2 || calls(t, f, "thread/start") != 1 {
		t.Fatal("restart repeated a mutation")
	}
	restarted.stop(t)
}

func TestProcessAcceptedMetadata(t *testing.T) {
	f := setup(t)
	server := startServer(t, f)
	alice, _ := startClient(t, f, "alice")
	alice.send(t, "/note SECRET_ACCEPTED_METADATA_SENTINEL")
	eventually(t, "accepted note", func() bool { return strings.Contains(alice.out.String(), "SECRET_ACCEPTED_METADATA_SENTINEL") })
	logs := server.diagnostics.String()
	if !strings.Contains(logs, `"msg":"message-accepted"`) || !strings.Contains(logs, `"client_message_id"`) {
		t.Fatalf("accepted operational metadata missing: %s", logs)
	}
	if strings.Contains(logs, "SECRET_ACCEPTED_METADATA_SENTINEL") {
		t.Fatal("raw accepted text in logs")
	}
	server.stop(t)
}

func TestProcessMalformedTerminalConverges(t *testing.T) {
	f := setup(t)
	server := startServer(t, f)
	alice, _ := startClient(t, f, "alice")
	control(t, f, "malformed-terminal")
	alice.send(t, "bad terminal turn")
	eventually(t, "protocol failure interrupts", func() bool { return calls(t, f, "turn/interrupt") == 1 })
	eventually(t, "malformed terminal converges", func() bool { im := image(t, f); return im.Active == nil && im.Status == room.RoomReady })
	found := false
	for _, e := range events(t, f) {
		if e.Kind == "turn/failed" && bytes.Contains(e.Payload, []byte("protocol-error")) {
			found = true
		}
	}
	if !found {
		t.Fatal("protocol failure was not durably recorded")
	}
	server.stop(t)
}

func TestProcessShutdownUnblocksRPC(t *testing.T) {
	for _, stage := range []string{"startup", "command"} {
		t.Run(stage, func(t *testing.T) {
			f := setup(t)
			var server *childProcess
			if stage == "startup" {
				control(t, f, "hang-init")
				server = startHelper(t, "room-server", f)
				eventually(t, "blocked initialization", func() bool { return calls(t, f, "initialize") == 1 })
			} else {
				server = startServer(t, f)
				alice, _ := startClient(t, f, "alice")
				control(t, f, "hang-turn")
				alice.send(t, "blocked mutation")
				eventually(t, "blocked turn RPC", func() bool { return calls(t, f, "turn/start") == 1 })
			}
			_ = server.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case e := <-server.done:
				server.done <- e
				if stage == "command" && e != nil {
					t.Fatalf("shutdown %v %s", e, server.diagnostics.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("shutdown waited on coordinator before closing runtime")
			}
			cp := image(t, f).Checkpoint
			if cp.State != room.RuntimeProcessStopped || cp.PID != 0 || cp.PGID != 0 {
				t.Fatalf("unreaped checkpoint %+v", cp)
			}
			if _, e := os.Stat(filepath.Join(f.State, "room.sock")); !os.IsNotExist(e) {
				t.Fatalf("shutdown left socket: %v", e)
			}
		})
	}
}

func dialProtocol(t *testing.T, f fixture) net.Conn {
	t.Helper()
	c, e := net.Dial("unix", filepath.Join(f.State, "room.sock"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	request(t, c, "hello", protocol.Hello{MinVersion: 1, MaxVersion: 1})
	return c
}
func request(t *testing.T, c net.Conn, method string, body any) protocol.Envelope {
	t.Helper()
	data, e := json.Marshal(body)
	if e != nil {
		t.Fatal(e)
	}
	id := strings.Repeat("e", 32)
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if e = protocol.NewWriter(c).Write(protocol.Envelope{Version: 1, Kind: protocol.KindRequest, ID: id, Method: method, Body: data}); e != nil {
		t.Fatal(e)
	}
	r := protocol.NewReader(c, protocol.MaxFrameBytes)
	for {
		env, e := r.Read()
		if e != nil {
			t.Fatal(e)
		}
		if env.ID == id {
			return env
		}
	}
}

func TestProcessRecoveryActions(t *testing.T) {
	for _, action := range []string{"retry", "skip", "continue"} {
		t.Run(action, func(t *testing.T) {
			f := setup(t)
			server := startServer(t, f)
			alice, _ := startClient(t, f, "alice")
			bob, _ := startClient(t, f, "bob")
			carol, _ := startClient(t, f, "carol")
			control(t, f, "crash-next")
			alice.send(t, "ambiguous prompt SECRET_PROMPT_SENTINEL")
			eventually(t, "stable runtime reconciled", func() bool {
				im := image(t, f)
				return im.Active != nil && im.Active.State == room.RequestNeedsReview && calls(t, f, "thread/resume") >= 1
			})
			im := image(t, f)
			if im.Status != room.RoomRecovering || im.ThreadID != "thread-1" {
				t.Fatalf("recovery %+v", im)
			}
			if calls(t, f, "turn/start") != 1 || calls(t, f, "thread/start") != 1 {
				t.Fatal("automatic mutation retry")
			}
			target := string(im.Active.MessageID)
			line := "/recover " + action + " " + target
			if action == "continue" {
				line += " owner continuation"
			}
			var wg sync.WaitGroup
			for _, p := range []*childProcess{bob, carol} {
				wg.Add(1)
				go func(p *childProcess) { defer wg.Done(); _, _ = fmt.Fprintln(p.input, line) }(p)
			}
			wg.Wait()
			for _, participant := range []*childProcess{bob, carol} {
				eventually(t, "both recovery decisions answered", func() bool {
					output := participant.out.String()
					return strings.Contains(output, "[recover]") || strings.Contains(output, "[error]")
				})
			}
			eventually(t, "one recovery winner", func() bool {
				for _, ev := range events(t, f) {
					if ev.Kind == "recovery/accepted" {
						return true
					}
				}
				return false
			})
			count := 0
			for _, ev := range events(t, f) {
				if ev.Kind == "recovery/accepted" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("winners=%d", count)
			}
			if strings.Count(bob.out.String()+carol.out.String(), "[recover]") != 1 || strings.Count(bob.out.String()+carol.out.String(), "[error]") != 1 {
				t.Fatal("expected one accepted decision and one rejected stale decision")
			}
			expected := 2
			if action == "skip" {
				expected = 1
			}
			eventually(t, "expected mutations", func() bool { return calls(t, f, "turn/start") == expected })
			if action != "skip" {
				control(t, f, "finish")
				eventually(t, "resolved completion", func() bool { return image(t, f).Active == nil })
			}
			for _, p := range []*childProcess{alice, bob, carol} {
				p.send(t, "/quit")
			}
			server.stop(t)
			if calls(t, f, "turn/start") != expected {
				t.Fatal("excess mutation calls")
			}
			logs := server.diagnostics.String()
			if !strings.Contains(logs, `"code":"rpc-failure"`) {
				t.Fatal("runtime failure never reached operational logger")
			}
			for _, sentinel := range []string{"SECRET_STDERR_SENTINEL", "SECRET_RPC_SENTINEL", "SECRET_PROMPT_SENTINEL"} {
				if strings.Contains(logs, sentinel) {
					t.Fatal("private diagnostic reached logger")
				}
			}
		})
	}
}

func TestProductionBinaryBoundary(t *testing.T) {
	f := setup(t)
	out, e := exec.Command(f.Binary, "help").CombinedOutput()
	if e != nil {
		t.Fatal(e)
	}
	for _, command := range []string{"dashboard", "host", "join", "answers", "doctor", "version"} {
		if !bytes.Contains(out, []byte(command)) {
			t.Fatal(command)
		}
	}
	for _, command := range []string{"", "init", "serve", "connect", "bridge", "repair-thread"} {
		for _, flag := range []string{"--as-user", "--fake", "--local"} {
			args := []string{flag}
			if command != "" {
				args = append([]string{command}, args...)
			}
			if output, e := exec.Command(f.Binary, args...).CombinedOutput(); e == nil {
				t.Fatalf("accepted %v: %s", args, output)
			}
		}
	}
	if runtime.GOOS != "linux" {
		cmd := exec.Command(f.Binary, "serve", "--state-dir", f.State)
		out, e := cmd.CombinedOutput()
		if e == nil || !bytes.Contains(out, []byte("unsupported-platform")) {
			t.Fatalf("platform gate: %s %v", out, e)
		}
	}
	for _, root := range []string{"../../cmd/agent_romm", "../../internal"} {
		e = filepath.WalkDir(root, func(path string, entry os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			for _, forbidden := range []string{"TestAgentRommHelperProcess", "room-server", "room-client", "fakessh", "control-dir", "PriorLive"} {
				if bytes.Contains(data, []byte(forbidden)) {
					t.Errorf("production source %s contains %s", path, forbidden)
				}
			}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	if calls(t, f, "turn/start") != 0 {
		t.Fatal("production parsing started model")
	}
}

func TestProcessStartupFaults(t *testing.T) {
	for _, mode := range []string{"owner-mismatch", "cli-version", "user-agent", "thread-uncertain", "prior-live"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			switch mode {
			case "owner-mismatch":
				c := cfg(t, f)
				c.ExecutionOwnerUID++
				writeJSON(t, filepath.Join(f.State, "private", "config.json"), c)
			case "cli-version":
				f.Version = "codex-cli 0.0.0"
			case "user-agent":
				control(t, f, "wrong-agent")
			case "thread-uncertain":
				control(t, f, "crash-thread")
			case "prior-live":
				f.PriorLive = true
				c := cfg(t, f)
				st, e := store.Open(context.Background(), c.DatabasePath)
				if e != nil {
					t.Fatal(e)
				}
				e = st.SaveRuntimeCheckpoint(context.Background(), c.RoomID, room.RuntimeCheckpoint{State: room.RuntimeProcessRunning, Generation: "prior", PID: os.Getpid(), PGID: os.Getpid(), ProcessStart: "prior-process", CodexVersion: codex.SupportedCLIOutput, SchemaSHA256: codex.SupportedSchemaSHA256})
				st.Close()
				if e != nil {
					t.Fatal(e)
				}
			}
			p := startHelper(t, "room-server", f)
			select {
			case e := <-p.done:
				p.done <- e
				if e == nil {
					t.Fatal("startup fault accepted")
				}
			case <-time.After(15 * time.Second):
				t.Fatal("startup fault hung")
			}
			if _, e := os.Stat(filepath.Join(f.State, "room.sock")); !os.IsNotExist(e) {
				t.Fatalf("bound socket on failure: %v", e)
			}
			if mode == "owner-mismatch" || mode == "cli-version" || mode == "prior-live" {
				if calls(t, f, "initialize") != 0 {
					t.Fatal("started App Server before validation")
				}
			}
			if mode == "user-agent" && calls(t, f, "thread/start") != 0 {
				t.Fatal("thread access before userAgent validation")
			}
			if mode == "thread-uncertain" {
				if image(t, f).Status != room.RoomThreadNeedsRepair || calls(t, f, "thread/start") != 1 {
					t.Fatal("uncertain thread not held for repair")
				}
			}
		})
	}
}

func TestProcessConnectionFaults(t *testing.T) {
	f := setup(t)
	server := startServer(t, f)
	alice, _ := startClient(t, f, "alice")
	t.Run("oversized frame isolated", func(t *testing.T) {
		c, e := net.Dial("unix", filepath.Join(f.State, "room.sock"))
		if e != nil {
			t.Fatal(e)
		}
		defer c.Close()
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], protocol.MaxFrameBytes+1)
		if _, e = c.Write(prefix[:]); e != nil {
			t.Fatal(e)
		}
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.ReadAll(c)
		alice.send(t, "/note after oversized frame")
		eventually(t, "healthy client survived", func() bool { return strings.Contains(alice.out.String(), "after oversized frame") })
	})
	t.Run("unsupported reverse request cancels", func(t *testing.T) {
		control(t, f, "reverse")
		alice.send(t, "reverse request turn")
		eventually(t, "unsupported reverse responses", func() bool {
			data, _ := os.ReadFile(f.Log)
			return bytes.Contains(data, []byte(`"decision":"cancel"`)) && bytes.Contains(data, []byte(`"code":-32090`))
		})
		eventually(t, "reverse turn finished", func() bool { return image(t, f).Active == nil })
		found := false
		for _, e := range events(t, f) {
			if e.Kind == "turn/failed" && bytes.Contains(e.Payload, []byte("unsupported-server-request")) {
				found = true
			}
		}
		if !found {
			t.Fatal("unsupported request lacks durable failed outcome")
		}
	})
	t.Run("slow client eviction", func(t *testing.T) {
		slow := dialProtocol(t, f)
		defer slow.Close()
		author := dialProtocol(t, f)
		defer author.Close()
		for i := 0; i < 300; i++ {
			env := request(t, author, "note", protocol.SubmitRequest{ClientMessageID: fmt.Sprintf("%032x", i+1000), Text: strings.Repeat("z", 20000)})
			if env.Kind != protocol.KindResponse {
				t.Fatalf("note failed %s", env.Body)
			}
		}
		_ = slow.SetReadDeadline(time.Now().Add(10 * time.Second))
		r := protocol.NewReader(slow, protocol.MaxFrameBytes)
		closed := false
		for {
			_, e := r.Read()
			if e != nil {
				var timeout net.Error
				if errors.As(e, &timeout) && timeout.Timeout() {
					t.Fatal("slow client not evicted")
				}
				closed = true
				break
			}
		}
		if !closed {
			t.Fatal("slow connection survived")
		}
		alice.send(t, "/note after slow eviction")
		eventually(t, "healthy client after slow eviction", func() bool { return strings.Contains(alice.out.String(), "after slow eviction") })
	})
	alice.send(t, "/quit")
	server.stop(t)
}
