package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"agent_romm/internal/room"
)

func TestVersionPolicyRequiresExactAllowlistedCLIAndUserAgent(t *testing.T) {
	policy := DefaultVersionPolicy()
	for _, tc := range []struct {
		name   string
		output string
		wantOK bool
	}{
		{name: "exact", output: SupportedCLIOutput, wantOK: true},
		{name: "outer whitespace", output: " \n" + SupportedCLIOutput + "\t", wantOK: true},
		{name: "newer", output: "codex-cli 0.152.0"},
		{name: "suffix", output: SupportedCLIOutput + "0"},
		{name: "prefix", output: "x" + SupportedCLIOutput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := policy.ValidateCLI(tc.output)
			if (err == nil) != tc.wantOK {
				t.Fatalf("ValidateCLI(%q) err=%v wantOK=%v", tc.output, err, tc.wantOK)
			}
		})
	}

	for _, tc := range []struct {
		name      string
		userAgent string
		wantOK    bool
	}{
		{name: "exact token", userAgent: "agent_romm/" + SupportedCodexVersion, wantOK: true},
		{name: "following tokens", userAgent: "agent_romm/" + SupportedCodexVersion + " platform/linux", wantOK: true},
		{name: "different product", userAgent: "codex/" + SupportedCodexVersion},
		{name: "version suffix", userAgent: "agent_romm/" + SupportedCodexVersion + "0"},
		{name: "token prefix", userAgent: "xagent_romm/" + SupportedCodexVersion},
		{name: "embedded", userAgent: "wrapper agent_romm/" + SupportedCodexVersion},
	} {
		t.Run("user-agent "+tc.name, func(t *testing.T) {
			err := policy.ValidateUserAgent(SupportedCLIOutput, tc.userAgent)
			if (err == nil) != tc.wantOK {
				t.Fatalf("ValidateUserAgent(%q) err=%v wantOK=%v", tc.userAgent, err, tc.wantOK)
			}
		})
	}
}

func TestSupervisorRejectsUnknownVersionBeforeAppServer(t *testing.T) {
	pm := &fakeProcessManager{cliVersion: "codex-cli 0.152.0"}
	s := mustSupervisor(t, pm)
	_, err := s.Start(context.Background(), "/srv/project", discardCheckpoint)
	if !errors.Is(err, ErrUnsupportedCodexVersion) {
		t.Fatalf("got %v", err)
	}
	if got := pm.startCallCount(); got != 0 {
		t.Fatalf("started child %d times", got)
	}
}

func TestSupervisorRandomFailureStopsBeforeAppServer(t *testing.T) {
	pm := &fakeProcessManager{}
	s := mustSupervisor(t, pm)
	s.random = errorReader{err: errors.New("random secret")}
	_, err := s.Start(context.Background(), "/srv/project", discardCheckpoint)
	if !errors.Is(err, ErrGenerationUnavailable) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "random secret") {
		t.Fatalf("raw random error escaped: %v", err)
	}
	if got := pm.startCallCount(); got != 0 {
		t.Fatalf("started child %d times", got)
	}
}

func TestReapPreviousFailClosed(t *testing.T) {
	valid := room.RuntimeCheckpoint{
		State: room.RuntimeProcessRunning, Generation: "g-1", PID: 42, PGID: 42,
		ProcessStart: "old", CodexVersion: SupportedCLIOutput, SchemaSHA256: SupportedSchemaSHA256,
	}
	tests := []struct {
		name       string
		checkpoint *room.RuntimeCheckpoint
		match      ProcessMatch
		matchErr   error
		want       error
		matchCalls int
	}{
		{name: "missing checkpoint"},
		{name: "corrupt empty state", checkpoint: &room.RuntimeCheckpoint{PID: 42, PGID: 42}, want: ErrInvalidProcessCheckpoint},
		{name: "stopped checkpoint", checkpoint: &room.RuntimeCheckpoint{State: room.RuntimeProcessStopped}},
		{name: "absent", checkpoint: cloneCheckpoint(&valid), match: ProcessAbsent, matchCalls: 1},
		{name: "still matches", checkpoint: cloneCheckpoint(&valid), match: ProcessMatches, want: ErrPreviousProcessRunning, matchCalls: 1},
		{name: "mismatch", checkpoint: cloneCheckpoint(&valid), match: ProcessMismatch, want: ErrProcessIdentityMismatch, matchCalls: 1},
		{name: "unknown", checkpoint: cloneCheckpoint(&valid), match: ProcessUnknown, want: ErrProcessIdentityUnknown, matchCalls: 1},
		{name: "changed while inspected", checkpoint: cloneCheckpoint(&valid), matchErr: ErrProcessIdentityChanged, want: ErrProcessIdentityUnknown, matchCalls: 1},
	}
	for _, value := range []int{0, 1, -1} {
		pid := cloneCheckpoint(&valid)
		pid.PID = value
		tests = append(tests, struct {
			name       string
			checkpoint *room.RuntimeCheckpoint
			match      ProcessMatch
			matchErr   error
			want       error
			matchCalls int
		}{name: fmt.Sprintf("invalid pid %d", value), checkpoint: pid, want: ErrInvalidProcessCheckpoint})
		pgid := cloneCheckpoint(&valid)
		pgid.PGID = value
		tests = append(tests, struct {
			name       string
			checkpoint *room.RuntimeCheckpoint
			match      ProcessMatch
			matchErr   error
			want       error
			matchCalls int
		}{name: fmt.Sprintf("invalid pgid %d", value), checkpoint: pgid, want: ErrInvalidProcessCheckpoint})
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pm := &fakeProcessManager{match: tc.match, matchErr: tc.matchErr}
			s := mustSupervisor(t, pm)
			err := s.ReapPrevious(context.Background(), tc.checkpoint)
			if tc.want == nil && err != nil {
				t.Fatalf("unexpected err=%v", err)
			}
			if tc.want != nil {
				if !errors.Is(err, tc.want) || !errors.Is(err, ErrOwnerRepairRequired) {
					t.Fatalf("err=%v want %v and owner repair", err, tc.want)
				}
				var ownerErr *OwnerRepairError
				if !errors.As(err, &ownerErr) {
					t.Fatalf("err=%T want *OwnerRepairError", err)
				}
			}
			if got := pm.matchCallCount(); got != tc.matchCalls {
				t.Fatalf("Match calls=%d want=%d", got, tc.matchCalls)
			}
			if got := pm.stopCallCount(); got != 0 {
				t.Fatalf("persisted identity was signaled %d times", got)
			}
		})
	}
}

func TestSupervisorPersistsRunningBeforeInitialize(t *testing.T) {
	pm := &fakeProcessManager{}
	s := mustSupervisor(t, pm)
	var checkpoints []room.RuntimeCheckpoint
	session, err := s.Start(context.Background(), "/srv/project", func(_ context.Context, checkpoint room.RuntimeCheckpoint) error {
		if checkpoint.State == room.RuntimeProcessRunning {
			if got := pm.requestMethods(); len(got) != 0 {
				t.Fatalf("requests before running checkpoint: %v", got)
			}
		}
		checkpoints = append(checkpoints, checkpoint)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop(context.Background(), session) })
	if len(checkpoints) != 1 {
		t.Fatalf("checkpoints=%d want=1", len(checkpoints))
	}
	cp := checkpoints[0]
	if cp.State != room.RuntimeProcessRunning || cp.PID <= 1 || cp.PGID <= 1 || cp.ProcessStart == "" {
		t.Fatalf("invalid running checkpoint: %+v", cp)
	}
	if len(cp.Generation) != 32 || cp.Generation != session.Child.Identity.Generation {
		t.Fatalf("generation=%q child=%q", cp.Generation, session.Child.Identity.Generation)
	}
	if cp.CodexVersion != SupportedCLIOutput || cp.SchemaSHA256 != SupportedSchemaSHA256 {
		t.Fatalf("compatibility checkpoint=%+v", cp)
	}
	if got := pm.requestMethods(); !reflect.DeepEqual(got, []string{"initialize", "initialized", "account/read"}) {
		t.Fatalf("startup requests=%v", got)
	}
	if got := pm.specsSnapshot(); len(got) != 1 || !reflect.DeepEqual(got[0].Args, []string{"app-server", "--listen", "stdio://"}) || got[0].Dir != "/srv/project" {
		t.Fatalf("specs=%+v", got)
	}
}

func TestSupervisorPersistenceFailureStopsExactChildBeforeInitialize(t *testing.T) {
	pm := &fakeProcessManager{}
	s := mustSupervisor(t, pm)
	_, err := s.Start(context.Background(), "/srv/project", func(context.Context, room.RuntimeCheckpoint) error {
		return errors.New("database secret")
	})
	if !errors.Is(err, ErrCheckpointPersistence) {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), "database secret") {
		t.Fatalf("raw checkpoint error escaped: %v", err)
	}
	if got := pm.stopCallCount(); got != 1 {
		t.Fatalf("stop calls=%d want=1", got)
	}
	if got := pm.requestMethods(); len(got) != 0 {
		t.Fatalf("requests after failed checkpoint=%v", got)
	}
}

func TestSupervisorPostCheckpointFailureStopsAndPersistsStopped(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config fakeAppConfig
		want   error
	}{
		{name: "user-agent mismatch", config: fakeAppConfig{userAgent: "agent_romm/0.153.40", stderr: "stderr-secret"}, want: ErrIncompatibleUserAgent},
		{name: "RPC rejection", config: fakeAppConfig{rpcError: "rpc-body-secret", stderr: "stderr-secret"}, want: ErrRPCRequestFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pm := &fakeProcessManager{configs: []fakeAppConfig{tc.config}}
			s := mustSupervisor(t, pm)
			var checkpoints []room.RuntimeCheckpoint
			_, err := s.Start(context.Background(), "/srv/project", func(_ context.Context, checkpoint room.RuntimeCheckpoint) error {
				checkpoints = append(checkpoints, checkpoint)
				return nil
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
			if strings.Contains(err.Error(), "stderr-secret") || strings.Contains(err.Error(), "rpc-body-secret") {
				t.Fatalf("peer diagnostic escaped: %v", err)
			}
			if got := pm.stopCallCount(); got != 1 {
				t.Fatalf("stop calls=%d want=1", got)
			}
			if len(checkpoints) != 2 || checkpoints[0].State != room.RuntimeProcessRunning || checkpoints[1].State != room.RuntimeProcessStopped {
				t.Fatalf("checkpoints=%+v", checkpoints)
			}
			if checkpoints[1].PID != 0 || checkpoints[1].PGID != 0 || checkpoints[1].ProcessStart == "" {
				t.Fatalf("invalid stopped checkpoint=%+v", checkpoints[1])
			}
		})
	}
}

func TestSupervisorRPCFailureAndChildExitEachCompleteSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(*fakeAppSession)
	}{
		{name: "protocol failure", fail: func(server *fakeAppSession) { server.writeRaw([]byte(`{"rpc-secret":true}` + "\n")) }},
		{name: "child exit", fail: func(server *fakeAppSession) { server.crash(errors.New("process-secret")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pm := &fakeProcessManager{}
			s := mustSupervisor(t, pm)
			var checkpoints []room.RuntimeCheckpoint
			session, err := s.Start(context.Background(), "/srv/project", func(_ context.Context, checkpoint room.RuntimeCheckpoint) error {
				checkpoints = append(checkpoints, checkpoint)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			tc.fail(pm.server(0))
			awaitClosed(t, session.Done)
			if err := session.Err(); err == nil {
				t.Fatal("Session.Err()=nil")
			} else if strings.Contains(err.Error(), "secret") {
				t.Fatalf("raw failure escaped: %v", err)
			}
			if err := s.Stop(context.Background(), session); err != nil {
				t.Fatal(err)
			}
			if got := pm.stopCallCount(); got != 1 {
				t.Fatalf("stop calls=%d", got)
			}
			if len(checkpoints) != 2 || checkpoints[1].State != room.RuntimeProcessStopped {
				t.Fatalf("checkpoints=%+v", checkpoints)
			}
		})
	}
}

func TestSessionChildExitLeavesRPCOpenToDrainStdout(t *testing.T) {
	clientStdout, serverStdout := io.Pipe()
	serverStdin, clientStdin := io.Pipe()
	rpc := NewRPCClient(clientStdout, clientStdin)
	adapter, err := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
	if err != nil {
		t.Fatal(err)
	}
	childDone := make(chan struct{})
	child := &Child{
		Identity: ProcessIdentity{Generation: "generation", PID: 42, PGID: 42, StartIdentity: "start"},
		Stdin:    clientStdin, Stdout: clientStdout, Stderr: nopReadCloser{}, Done: childDone,
		WaitErr: func() error {
			<-childDone
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	stderrDone := make(chan struct{})
	close(stderrDone)
	session := newSession(nil, adapter, rpc, child, cancel, stderrDone, discardCheckpoint, room.RuntimeCheckpoint{})
	go session.runRPC(ctx)
	go session.observeTerminal()

	close(childDone)
	awaitClosed(t, session.Done)
	select {
	case <-session.rpcDone:
		t.Fatal("child exit canceled RPC before buffered stdout could drain")
	case <-time.After(25 * time.Millisecond):
	}

	if err := serverStdout.Close(); err != nil {
		t.Fatal(err)
	}
	awaitClosed(t, session.rpcDone)
	_ = serverStdin.Close()
}

func TestSessionObservationFailureCancelsRPCInsteadOfWaitingForEOF(t *testing.T) {
	clientStdout, serverStdout := io.Pipe()
	defer serverStdout.Close()
	serverStdin, clientStdin := io.Pipe()
	defer serverStdin.Close()
	rpc := NewRPCClient(clientStdout, clientStdin)
	adapter, err := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/srv/project"})
	if err != nil {
		t.Fatal(err)
	}
	childDone := make(chan struct{})
	child := &Child{
		Identity: ProcessIdentity{Generation: "generation", PID: 42, PGID: 42, StartIdentity: "start"},
		Stdin:    clientStdin, Stdout: clientStdout, Stderr: nopReadCloser{}, Done: childDone,
		WaitErr: func() error {
			<-childDone
			return ErrProcessObservation
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	stderrDone := make(chan struct{})
	close(stderrDone)
	session := newSession(nil, adapter, rpc, child, cancel, stderrDone, discardCheckpoint, room.RuntimeCheckpoint{})
	go session.runRPC(ctx)
	go session.observeTerminal()

	close(childDone)
	awaitClosed(t, session.Done)
	awaitClosed(t, session.rpcDone)
}

func TestSupervisorStopIsConcurrentAndIdempotent(t *testing.T) {
	pm := &fakeProcessManager{}
	s := mustSupervisor(t, pm)
	var mu sync.Mutex
	var checkpoints []room.RuntimeCheckpoint
	session, err := s.Start(context.Background(), "/srv/project", func(_ context.Context, checkpoint room.RuntimeCheckpoint) error {
		mu.Lock()
		checkpoints = append(checkpoints, checkpoint)
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	const callers = 16
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.Stop(context.Background(), session)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Stop err=%v", err)
		}
	}
	if got := pm.stopCallCount(); got != 1 {
		t.Fatalf("stop calls=%d want=1", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(checkpoints) != 2 {
		t.Fatalf("checkpoints=%+v", checkpoints)
	}
}

func TestSupervisorStopBoundsCheckpointPersistenceContext(t *testing.T) {
	pm := &fakeProcessManager{}
	s := mustSupervisor(t, pm)
	s.cleanupTimeout = 20 * time.Millisecond
	deadlineObserved := make(chan bool, 1)
	session, err := s.Start(context.Background(), "/srv/project", func(ctx context.Context, checkpoint room.RuntimeCheckpoint) error {
		if checkpoint.State == room.RuntimeProcessRunning {
			return nil
		}
		_, ok := ctx.Deadline()
		deadlineObserved <- ok
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	callerCtx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err = s.Stop(callerCtx, session)
	if !errors.Is(err, ErrCheckpointPersistence) {
		t.Fatalf("Stop err=%v want ErrCheckpointPersistence", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop err=%v did not preserve cleanup deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Stop took %v with blocked checkpoint sink", elapsed)
	}
	if ok := <-deadlineObserved; !ok {
		t.Fatal("stopped checkpoint sink received an unbounded context")
	}
}

func TestSupervisorStopBoundsProcessCleanupContext(t *testing.T) {
	pm := &fakeProcessManager{stopWaitForContext: true}
	s := mustSupervisor(t, pm)
	s.cleanupTimeout = 20 * time.Millisecond
	stoppedWrites := 0
	session, err := s.Start(context.Background(), "/srv/project", func(_ context.Context, checkpoint room.RuntimeCheckpoint) error {
		if checkpoint.State == room.RuntimeProcessStopped {
			stoppedWrites++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = s.Stop(context.Background(), session)
	if !errors.Is(err, ErrProcessCleanup) {
		t.Fatalf("Stop err=%v want ErrProcessCleanup", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop err=%v did not preserve cleanup deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Stop took %v with blocked process cleanup", elapsed)
	}
	if stoppedWrites != 0 {
		t.Fatalf("persisted stopped %d times without proven cleanup", stoppedWrites)
	}
}

func TestSupervisorProbeInitializesWithoutThreadAccessAndReaps(t *testing.T) {
	pm := &fakeProcessManager{}
	s := mustSupervisor(t, pm)
	checkpoint, err := s.Probe(context.Background(), "/srv/project")
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.State != room.RuntimeProcessStopped || checkpoint.PID != 0 || checkpoint.PGID != 0 || checkpoint.ProcessStart == "" {
		t.Fatalf("checkpoint=%+v", checkpoint)
	}
	if got := pm.stopCallCount(); got != 1 {
		t.Fatalf("stop calls=%d", got)
	}
	if got := pm.requestMethods(); !reflect.DeepEqual(got, []string{"initialize", "initialized", "account/read"}) {
		t.Fatalf("probe requests=%v", got)
	}
}

func TestSupervisorProbeFailsWhenCleanupCannotBeProven(t *testing.T) {
	pm := &fakeProcessManager{stopErrors: []error{errors.New("cleanup secret")}}
	s := mustSupervisor(t, pm)
	_, err := s.Probe(context.Background(), "/srv/project")
	if !errors.Is(err, ErrProcessCleanup) {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), "cleanup secret") {
		t.Fatalf("raw cleanup error escaped: %v", err)
	}
}

func TestFakeAppServerExecutable(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "fakeappserver")
	command := exec.Command("go", "build", "-o", executable, "./testdata/fakeappserver")
	command.Env = append(os.Environ(), "GOCACHE=/tmp/agent-romm-gocache", "GOMODCACHE=/tmp/agent-romm-gomodcache")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build fake app server: %v\n%s", err, output)
	}
	version, err := exec.Command(executable, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(version)); got != SupportedCLIOutput {
		t.Fatalf("version=%q", got)
	}

	record := filepath.Join(t.TempDir(), "requests.jsonl")
	server := exec.Command(executable, "app-server", "--listen", "stdio://", "--record", record)
	stdin, err := server.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := server.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(stdin, `{"id":1,"method":"initialize","params":{"clientInfo":{"name":"agent_romm","version":"0.153.4"}}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(line, []byte(`"userAgent":"agent_romm/0.153.4"`)) {
		t.Fatalf("initialize response=%s", line)
	}
	_ = stdin.Close()
	if err := server.Wait(); err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(recorded, []byte(`"method":"initialize"`)) {
		t.Fatalf("recorded=%s", recorded)
	}

	initializeLine := `{"id":1,"method":"initialize","params":{"clientInfo":{"name":"agent_romm","version":"0.153.4"}}}` + "\n"
	t.Run("EOF", func(t *testing.T) {
		output := runFakeAppServer(t, executable, []string{"--mode", "eof"}, initializeLine)
		if len(output) != 0 {
			t.Fatalf("output=%q", output)
		}
	})
	t.Run("delayed response", func(t *testing.T) {
		started := time.Now()
		output := runFakeAppServer(t, executable, []string{"--mode", "delayed-response", "--delay", "30ms"}, initializeLine)
		if elapsed := time.Since(started); elapsed < 25*time.Millisecond {
			t.Fatalf("response was not delayed: %v", elapsed)
		}
		if !bytes.Contains(output, []byte(`"userAgent":"agent_romm/0.153.4"`)) {
			t.Fatalf("output=%s", output)
		}
	})
	t.Run("malformed response", func(t *testing.T) {
		output := runFakeAppServer(t, executable, []string{"--mode", "malformed-response"}, initializeLine)
		if string(output) != "{malformed\n" {
			t.Fatalf("output=%q", output)
		}
	})
	t.Run("reverse request", func(t *testing.T) {
		output := runFakeAppServer(t, executable, []string{"--mode", "reverse-request"}, initializeLine)
		if !bytes.Contains(output, []byte(`"method":"item/tool/requestUserInput"`)) ||
			!bytes.Contains(output, []byte(`"userAgent":"agent_romm/0.153.4"`)) {
			t.Fatalf("output=%s", output)
		}
	})
	t.Run("crash after receive", func(t *testing.T) {
		input := initializeLine +
			`{"method":"initialized"}` + "\n" +
			`{"id":2,"method":"account/read","params":{"refreshToken":false}}` + "\n" +
			`{"id":3,"method":"turn/start","params":{}}` + "\n"
		output := runFakeAppServer(t, executable, []string{"--mode", "crash-after-receive-before-response"}, input)
		if bytes.Contains(output, []byte(`"id":3`)) || !bytes.Contains(output, []byte(`"id":2`)) {
			t.Fatalf("output=%s", output)
		}
	})
	t.Run("configured stream", func(t *testing.T) {
		input := `{"id":3,"method":"turn/start","params":{}}` + "\n"
		output := runFakeAppServer(t, executable, []string{"--delta", "hello"}, input)
		if !bytes.Contains(output, []byte(`"delta":"hello"`)) || !bytes.Contains(output, []byte(`"method":"turn/completed"`)) {
			t.Fatalf("output=%s", output)
		}
	})
}

func runFakeAppServer(t *testing.T, executable string, extraArgs []string, input string) []byte {
	t.Helper()
	args := append([]string{"app-server", "--listen", "stdio://"}, extraArgs...)
	command := exec.Command(executable, args...)
	command.Stdin = strings.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run fake App Server: %v output=%s", err, output)
	}
	return output
}

func mustSupervisor(t *testing.T, pm ProcessManager) *Supervisor {
	t.Helper()
	s, err := NewSupervisor("codex", DefaultVersionPolicy(), pm)
	if err != nil {
		t.Fatal(err)
	}
	s.stopGrace = time.Millisecond
	return s
}

func discardCheckpoint(context.Context, room.RuntimeCheckpoint) error { return nil }

func cloneCheckpoint(checkpoint *room.RuntimeCheckpoint) *room.RuntimeCheckpoint {
	copyCheckpoint := *checkpoint
	return &copyCheckpoint
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

type fakeAppConfig struct {
	userAgent   string
	stderr      string
	rpcError    string
	crashMethod string
}

type fakeProcessManager struct {
	mu                 sync.Mutex
	cliVersion         string
	versionErr         error
	match              ProcessMatch
	matchErr           error
	configs            []fakeAppConfig
	startErrors        map[int]error
	stopErrors         []error
	stopWaitForContext bool
	startCalls         int
	stopCalls          int
	matchCalls         int
	specs              []ProcessSpec
	servers            []*fakeAppSession
}

func (pm *fakeProcessManager) Version(context.Context, string) (string, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.versionErr != nil {
		return "", pm.versionErr
	}
	if pm.cliVersion == "" {
		return SupportedCLIOutput, nil
	}
	return pm.cliVersion, nil
}

func (pm *fakeProcessManager) Start(_ context.Context, spec ProcessSpec) (*Child, error) {
	pm.mu.Lock()
	pm.startCalls++
	call := pm.startCalls
	pm.specs = append(pm.specs, spec)
	if err := pm.startErrors[call]; err != nil {
		pm.mu.Unlock()
		return nil, err
	}
	config := fakeAppConfig{}
	if call <= len(pm.configs) {
		config = pm.configs[call-1]
	}
	server := newFakeAppSession(spec.Generation, 100+call, config)
	pm.servers = append(pm.servers, server)
	pm.mu.Unlock()
	server.start()
	return server.child, nil
}

func (pm *fakeProcessManager) Match(context.Context, ProcessIdentity) (ProcessMatch, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.matchCalls++
	return pm.match, pm.matchErr
}

func (pm *fakeProcessManager) StopCurrent(ctx context.Context, child *Child, _ time.Duration) error {
	pm.mu.Lock()
	pm.stopCalls++
	call := pm.stopCalls
	var server *fakeAppSession
	for _, candidate := range pm.servers {
		if candidate.child == child {
			server = candidate
			break
		}
	}
	var configured error
	if call <= len(pm.stopErrors) {
		configured = pm.stopErrors[call-1]
	}
	pm.mu.Unlock()
	if server == nil {
		return errors.New("foreign child")
	}
	if pm.stopWaitForContext {
		<-ctx.Done()
		server.stop()
		if configured != nil {
			return configured
		}
		return ctx.Err()
	}
	server.stop()
	return configured
}

func (pm *fakeProcessManager) startCallCount() int {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.startCalls
}

func (pm *fakeProcessManager) stopCallCount() int {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.stopCalls
}

func (pm *fakeProcessManager) matchCallCount() int {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.matchCalls
}

func (pm *fakeProcessManager) server(index int) *fakeAppSession {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.servers[index]
}

func (pm *fakeProcessManager) specsSnapshot() []ProcessSpec {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return append([]ProcessSpec(nil), pm.specs...)
}

func (pm *fakeProcessManager) requestMethods() []string {
	pm.mu.Lock()
	servers := append([]*fakeAppSession(nil), pm.servers...)
	pm.mu.Unlock()
	var methods []string
	for _, server := range servers {
		methods = append(methods, server.methodsSnapshot()...)
	}
	return methods
}

type fakeAppSession struct {
	child        *Child
	config       fakeAppConfig
	serverInput  *io.PipeReader
	clientInput  *io.PipeWriter
	clientOutput *io.PipeReader
	serverOutput *io.PipeWriter
	clientStderr *io.PipeReader
	serverStderr *io.PipeWriter

	mu       sync.Mutex
	writeMu  sync.Mutex
	methods  []string
	waitErr  error
	done     chan struct{}
	doneOnce sync.Once
}

func newFakeAppSession(generation string, pid int, config fakeAppConfig) *fakeAppSession {
	serverInput, clientInput := io.Pipe()
	clientOutput, serverOutput := io.Pipe()
	clientStderr, serverStderr := io.Pipe()
	server := &fakeAppSession{
		config: config, serverInput: serverInput, clientInput: clientInput,
		clientOutput: clientOutput, serverOutput: serverOutput,
		clientStderr: clientStderr, serverStderr: serverStderr, done: make(chan struct{}),
	}
	server.child = &Child{
		Identity: ProcessIdentity{Generation: generation, PID: pid, PGID: pid, StartIdentity: fmt.Sprintf("start-%d", pid)},
		Stdin:    clientInput, Stdout: clientOutput, Stderr: clientStderr, Done: server.done,
		WaitErr: func() error {
			<-server.done
			server.mu.Lock()
			defer server.mu.Unlock()
			return server.waitErr
		},
	}
	return server
}

func (s *fakeAppSession) start() {
	go func() {
		if s.config.stderr != "" {
			_, _ = io.WriteString(s.serverStderr, s.config.stderr)
		}
		scanner := bufio.NewScanner(s.serverInput)
		scanner.Buffer(make([]byte, 1024), MaxJSONLLineBytes+1)
		for scanner.Scan() {
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(scanner.Bytes(), &request) != nil {
				s.crash(errors.New("bad request"))
				return
			}
			s.mu.Lock()
			s.methods = append(s.methods, request.Method)
			s.mu.Unlock()
			if request.Method == "initialized" {
				continue
			}
			if s.config.crashMethod == request.Method {
				s.crash(errors.New("crash-after-receive secret"))
				return
			}
			if err := s.respond(request.ID, request.Method); err != nil {
				s.crash(err)
				return
			}
		}
		s.crash(scanner.Err())
	}()
}

func (s *fakeAppSession) respond(id json.RawMessage, method string) error {
	if s.config.rpcError != "" && method == "initialize" {
		return s.writeMessage(map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": s.config.rpcError}})
	}
	userAgent := s.config.userAgent
	if userAgent == "" {
		userAgent = "agent_romm/" + SupportedCodexVersion
	}
	var result any
	switch method {
	case "initialize":
		result = map[string]any{"codexHome": "/tmp/codex", "platformFamily": "unix", "platformOs": "linux", "userAgent": userAgent}
	case "account/read":
		result = map[string]any{"account": map[string]any{"type": "apiKey"}, "requiresOpenaiAuth": false}
	case "thread/start":
		result = fakePolicyThreadResult("thread-1", "/srv/project")
	case "thread/read":
		result = map[string]any{"thread": fakeThread("thread-1", "/srv/project")}
	case "thread/resume":
		result = fakePolicyThreadResult("thread-1", "/srv/project")
	case "turn/start":
		result = map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}}
	case "turn/steer":
		result = map[string]any{"turnId": "turn-1"}
	case "turn/interrupt":
		result = map[string]any{}
	default:
		result = map[string]any{}
	}
	return s.writeMessage(map[string]any{"id": id, "result": result})
}

func fakePolicyThreadResult(id, cwd string) map[string]any {
	return map[string]any{
		"thread": fakeThread(id, cwd), "cwd": cwd, "approvalPolicy": "never",
		"sandbox": map[string]any{"type": "dangerFullAccess"},
	}
}

func fakeThread(id, cwd string) map[string]any {
	return map[string]any{"id": id, "cwd": cwd, "turns": []any{}}
}

func (s *fakeAppSession) writeMessage(value any) error {
	line, err := json.Marshal(value)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	return s.writeRaw(line)
}

func (s *fakeAppSession) writeRaw(line []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.serverOutput.Write(line)
	return err
}

func (s *fakeAppSession) notify(method string, params any) error {
	return s.writeMessage(map[string]any{"method": method, "params": params})
}

func (s *fakeAppSession) crash(err error) {
	_ = s.serverOutput.Close()
	_ = s.serverStderr.Close()
	_ = s.serverInput.Close()
	s.finish(err)
}

func (s *fakeAppSession) exitKeepingStdoutOpen(err error) {
	s.finish(err)
}

func (s *fakeAppSession) finish(err error) {
	s.doneOnce.Do(func() {
		s.mu.Lock()
		s.waitErr = err
		s.mu.Unlock()
		close(s.done)
	})
}

func (s *fakeAppSession) stop() {
	_ = s.clientInput.Close()
	_ = s.clientOutput.Close()
	_ = s.clientStderr.Close()
	_ = s.serverOutput.Close()
	_ = s.serverStderr.Close()
	_ = s.serverInput.Close()
	s.finish(nil)
}

func (s *fakeAppSession) methodsSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.methods...)
}

func awaitClosed(t *testing.T, channel <-chan struct{}) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for channel close")
	}
}
