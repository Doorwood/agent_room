// Package cli wires the host-local administration and SSH transport commands.
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"agent_romm/internal/admin"
	"agent_romm/internal/bridge"
	"agent_romm/internal/client"
	"agent_romm/internal/codex"
	"agent_romm/internal/config"
	"agent_romm/internal/daemon"
	"agent_romm/internal/gitview"
	"agent_romm/internal/identity"
	"agent_romm/internal/network"
	"agent_romm/internal/observability"
	"agent_romm/internal/room"
	"agent_romm/internal/store"
)

// Dependencies supplies typed embedding ports; production has no alternate
// identity, executable, or transport selectors in its argument parser.
type Dependencies struct {
	Admin           admin.Dependencies
	Repair          admin.RepairDependencies
	Processes       codex.ProcessManager
	CodexExecutable string
	Peers           identity.Resolver
	Client          client.Deps
	Input           io.ReadCloser
	UserConfigDir   func() (string, error)
	CheckPlatform   func() error
}

func ProductionDependencies() Dependencies {
	return Dependencies{Admin: admin.DefaultDependencies(), Repair: admin.DefaultRepairDependencies(), Processes: codex.NewProcessManager(), Peers: identity.KernelResolver{}, Input: os.Stdin, UserConfigDir: os.UserConfigDir, CheckPlatform: func() error {
		if runtime.GOOS != "linux" {
			return codex.ErrUnsupportedPlatform
		}
		return nil
	}}
}

const help = "agent_romm: trusted local room engine\ncommands: host, join, answers, requests, approve, deny, revoke, session\nlegacy: init, serve, connect, bridge, repair-thread\nUse <command> --help for options.\n"

// Run returns 2 for invalid arguments, 3 for unsupported platforms, and 1 for
// operational failures. It never resolves a Codex executable for help/parsing.
func Run(ctx context.Context, args []string, out, diagnostics io.Writer, d Dependencies) int {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")) {
		fmt.Fprint(out, help)
		return 0
	}
	if isNetworkCommand(args[0]) {
		return runNetwork(ctx, args, out, diagnostics, d)
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	usage := func() int { fmt.Fprintln(diagnostics, "invalid arguments; use", args[0], "--help"); return 2 }
	var state, project, name, owner, group, memberList, use string
	var full, create bool
	switch args[0] {
	case "init":
		fs.StringVar(&state, "state-dir", "", "absolute room state directory")
		fs.StringVar(&project, "project", "", "absolute Git root")
		fs.StringVar(&name, "room", "", "room name")
		fs.StringVar(&owner, "execution-owner", "", "execution owner account")
		fs.StringVar(&group, "shared-group", "", "shared Unix group")
		fs.StringVar(&memberList, "members", "", "at least three distinct comma-separated accounts")
		fs.BoolVar(&full, "full-owner-access", false, "acknowledge full execution-owner authority")
	case "serve":
		fs.StringVar(&state, "state-dir", "", "absolute room state directory")
	case "repair-thread":
		fs.StringVar(&state, "state-dir", "", "absolute room state directory")
		fs.StringVar(&use, "use", "", "bind reviewed existing thread ID")
		fs.BoolVar(&create, "create", false, "create and bind a replacement thread")
	case "connect", "bridge":
	default:
		return usage()
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if args[0] == "connect" {
		if fs.NArg() != 1 || client.ValidateTarget(fs.Arg(0)) != nil {
			return usage()
		}
	} else if fs.NArg() != 0 {
		return usage()
	}
	var err error
	switch args[0] {
	case "init":
		members := strings.Split(memberList, ",")
		distinct := map[string]bool{}
		for _, m := range members {
			if m == "" || strings.TrimSpace(m) != m || distinct[m] {
				return usage()
			}
			distinct[m] = true
		}
		if len(distinct) < 3 || !filepath.IsAbs(state) || !filepath.IsAbs(project) || name == "" || owner == "" || group == "" || !full {
			return usage()
		}
		err = requireDistinctAccounts(d.Admin, members)
		if err == nil {
			_, err = admin.InitWithDependencies(ctx, d.Admin, admin.InitOptions{Project: project, StateDir: state, Room: name, ExecutionOwner: owner, Members: members, SharedGroup: group, FullOwnerAccess: full})
		}
	case "serve":
		if !filepath.IsAbs(state) {
			return usage()
		}
		err = serve(ctx, state, diagnostics, d)
	case "repair-thread":
		if !filepath.IsAbs(state) || (use != "") == create {
			return usage()
		}
		opts := admin.RepairOptions{StateDir: state, Reviewed: true, Reason: "owner-requested"}
		if create {
			err = admin.RepairCreateWithDependencies(ctx, d.Repair, opts)
		} else {
			err = admin.RepairUseWithDependencies(ctx, d.Repair, opts, room.ThreadID(use))
		}
	case "connect":
		err = client.New(d.Client).Run(ctx, fs.Arg(0), d.Input, out, diagnostics)
	case "bridge":
		err = relay(ctx, d, out)
	}
	if err == nil {
		return 0
	}
	if errors.Is(err, codex.ErrUnsupportedPlatform) {
		fmt.Fprintln(diagnostics, "unsupported-platform")
		return 3
	}
	// Diagnostics may contain private RPC payloads or filesystem text. Only a
	// digest crosses the command logging boundary.
	sum := sha256.Sum256([]byte(err.Error()))
	observability.New(diagnostics).Failure(ctx, observability.FailureFields{Code: observability.InternalFailure, Digest: observability.DiagnosticDigest(fmt.Sprintf("%x", sum))})
	return 1
}

func requireDistinctAccounts(d admin.Dependencies, names []string) error {
	seen := map[uint64]bool{}
	for _, name := range names {
		account, err := d.LookupUser(name)
		if err != nil {
			return err
		}
		id, err := strconv.ParseUint(account.Uid, 10, 32)
		if err != nil {
			return err
		}
		if id == 0 {
			return admin.ErrExecutionOwnerMismatch
		}
		seen[id] = true
	}
	if len(seen) < 3 {
		return errors.New("at least three distinct Unix accounts are required")
	}
	return nil
}

func relay(ctx context.Context, d Dependencies, out io.Writer) error {
	dir, err := d.UserConfigDir()
	if err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(dir, "agent_romm", "bridge.json"))
	if err != nil {
		return err
	}
	defer f.Close()
	var locator struct {
		SocketPath string `json:"socketPath"`
	}
	dec := json.NewDecoder(io.LimitReader(f, 4096))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&locator); err != nil {
		return err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || !filepath.IsAbs(locator.SocketPath) {
		return errors.New("invalid bridge locator")
	}
	output, ok := out.(io.WriteCloser)
	if !ok {
		return errors.New("bridge requires closable output")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", locator.SocketPath)
	if err != nil {
		return err
	}
	return bridge.Relay(ctx, d.Input, output, conn)
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

type loggedSink struct {
	hub *daemon.Hub
	log *observability.Logger
}

func (s loggedSink) PublishTransient(e room.TransientEvent) { s.hub.PublishTransient(e) }
func (s loggedSink) PublishDurable(e room.DurableEvent) {
	s.log.RoomSequence(context.Background(), e.Seq)
	var ids struct {
		ThreadID room.ThreadID `json:"thread_id"`
		TurnID   room.TurnID   `json:"turn_id"`
		ItemID   room.ItemID   `json:"item_id"`
	}
	if json.Unmarshal(e.Payload, &ids) == nil {
		s.log.Committed(context.Background(), observability.CommittedFields{Seq: e.Seq, ActorUID: e.ActorUID, Kind: e.Kind, ThreadID: ids.ThreadID, TurnID: ids.TurnID, ItemID: ids.ItemID})
	}
	if e.Kind == "room/status" {
		var value struct {
			Current room.RoomStatus `json:"current"`
		}
		if json.Unmarshal(e.Payload, &value) == nil {
			s.log.Recovery(context.Background(), value.Current)
		}
	}
	s.hub.PublishDurable(e)
}

func serve(ctx context.Context, state string, diagnostics io.Writer, d Dependencies) error {
	return serveNetwork(ctx, state, diagnostics, d, nil)
}

type networkStart func(config.RuntimeConfig, *store.Store, *daemon.Server) (*network.Server, error)

func serveNetwork(ctx context.Context, state string, diagnostics io.Writer, d Dependencies, start networkStart) (err error) {
	if err = d.CheckPlatform(); err != nil {
		return err
	}
	cfg, lock, err := admin.ValidateServeWithDependencies(ctx, d.Admin, state)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if cfg.CodexVersion != codex.SupportedCLIOutput || cfg.SchemaSHA256 != codex.SupportedSchemaSHA256 {
		return codex.ErrUnsupportedCodexVersion
	}
	st, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, st.Close()) }()
	image, err := st.LoadRecoveryImage(ctx, cfg.RoomID)
	if err != nil {
		return err
	}
	supervisor, err := codex.NewSupervisor(codexPath(d), codex.DefaultVersionPolicy(), d.Processes)
	if err != nil {
		return err
	}
	if err = supervisor.ReapPrevious(ctx, &image.Checkpoint); err != nil {
		return err
	}
	checkpoint := func(ctx context.Context, cp room.RuntimeCheckpoint) error {
		return st.SaveRuntimeCheckpoint(ctx, cfg.RoomID, cp)
	}
	rt, err := codex.NewDefaultRuntime(supervisor, codex.RuntimeConfig{ProjectRoot: cfg.ProjectRoot}, checkpoint)
	if err != nil {
		return err
	}
	// Caller cancellation first stops server sessions; runtime/coordinator receive
	// cancellation only through the ordered cleanup below.
	ownerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	var coordinatorDone chan error
	var serverDone chan error
	var observedDone <-chan struct{}
	var server *daemon.Server
	var remote *network.Server
	var remoteErrors <-chan error
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if remote != nil {
			err = errors.Join(err, remote.Close())
		}
		if server != nil {
			err = errors.Join(err, server.Shutdown(cleanup))
		}
		if serverDone != nil {
			select {
			case e := <-serverDone:
				err = errors.Join(err, e)
			case <-cleanup.Done():
				err = errors.Join(err, cleanup.Err())
			}
		}
		cancel()
		err = errors.Join(err, rt.Close(cleanup))
		if observedDone != nil {
			select {
			case <-observedDone:
			case <-cleanup.Done():
				err = errors.Join(err, cleanup.Err())
			}
		}
		if coordinatorDone != nil {
			select {
			case e := <-coordinatorDone:
				if e != nil && !errors.Is(e, context.Canceled) && !errors.Is(e, room.ErrAgentRuntimeClosed) {
					err = errors.Join(err, e)
				}
			case <-cleanup.Done():
				err = errors.Join(err, cleanup.Err())
			}
		}
	}()
	startup, stopStartup := context.WithTimeout(ownerCtx, 30*time.Second)
	// Runtime must retain ownerCtx after the bounded startup; cancellation of
	// startup is used for Recover only, not as Runtime's lifetime context.
	defer stopStartup()
	started := make(chan error, 1)
	go func() { started <- rt.Start(ownerCtx) }()
	select {
	case err = <-started:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-startup.Done():
		return startup.Err()
	}
	if start != nil && image.ThreadID != "" {
		active, e := st.HasAgentActivity(startup, cfg.RoomID)
		if e != nil {
			return e
		}
		if !active {
			if _, e = rt.ReadThread(startup, image.ThreadID); errors.Is(e, codex.ErrThreadNotFound) {
				if _, e = st.ResetUnusedThread(startup, cfg.RoomID, image.ThreadID); e != nil {
					return e
				}
			} else if e != nil {
				return e
			}
		}
	}
	hub := daemon.NewHub(256, 16<<20)
	logger := observability.New(diagnostics)
	agent, agentDone := observeAgent(ownerCtx, rt, logger)
	observedDone = agentDone
	coordinator, err := room.NewCoordinator(cfg.RoomID, cfg.ProjectRoot, st, agent, loggedSink{hub, logger}, wallClock{})
	if err != nil {
		return err
	}
	coordinatorDone = make(chan error, 1)
	go func() { coordinatorDone <- coordinator.Run(ownerCtx) }()
	recovered := make(chan error, 1)
	go func() { recovered <- coordinator.Recover(startup) }()
	select {
	case err = <-recovered:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-startup.Done():
		return startup.Err()
	}
	server, err = daemon.NewServer(daemon.Config{RoomID: cfg.RoomID, RoomName: cfg.RoomName, ProjectRoot: cfg.ProjectRoot, ExecutionOwner: cfg.ExecutionOwnerName, SocketPath: cfg.SocketPath, SocketGID: cfg.SharedGroupGID}, daemon.Dependencies{Logger: logger, Peers: d.Peers, Members: st, Events: st, Coordinator: coordinator, Hub: hub, Connections: &loggedConnections{Store: st, log: logger}, ProjectView: gitview.View{Root: cfg.ProjectRoot}})
	if err != nil {
		return err
	}
	if start != nil {
		if err = st.CloseStale(ctx, cfg.RoomID, time.Now()); err != nil {
			return err
		}
		remote, err = start(cfg, st, server)
		if err != nil {
			return err
		}
		remoteErrors = remote.Errors()
	}
	if start == nil {
		serverDone = make(chan error, 1)
		go func() { serverDone <- server.Serve(ownerCtx) }()
	}
	select {
	case <-ctx.Done():
		return nil
	case err = <-remoteErrors:
		return err
	case err = <-serverDone:
		serverDone = nil
		return err
	case err = <-coordinatorDone:
		coordinatorDone = nil
		if err == nil {
			return errors.New("coordinator exited unexpectedly")
		}
		return err
	}
}
