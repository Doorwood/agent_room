package cli

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"

	"agent_romm/internal/admin"
	"agent_romm/internal/answerwindow"
	"agent_romm/internal/client"
	"agent_romm/internal/codex"
	"agent_romm/internal/config"
	"agent_romm/internal/daemon"
	"agent_romm/internal/dashboard"
	"agent_romm/internal/hostview"
	"agent_romm/internal/network"
	"agent_romm/internal/resources"
	"agent_romm/internal/store"
)

func isNetworkCommand(s string) bool {
	switch s {
	case "host", "join", "answers", "requests", "approve", "deny", "revoke", "role", "session", "resource-mode":
		return true
	}
	return false
}
func runNetwork(ctx context.Context, args []string, out, diag io.Writer, d Dependencies) int {
	var state string
	var err error
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(diag)
	fs.Usage = func() {
		fmt.Fprintf(diag, "agent_room %s\n", args[0])
		switch args[0] {
		case "join", "answers":
			fmt.Fprintf(diag, "Usage: agent_room %s HOST_IP SESSION_ID --name YOUR_NAME\nExample: agent_room %s 192.168.1.10:7443 COMPLETE_SESSION_ID --name alice\n", args[0], args[0])
			fmt.Fprintln(diag, "Copy the complete session_id from the host. First connection requires host approval. For room management: agent_room dashboard")
		case "approve", "role":
			fmt.Fprintf(diag, "Usage: agent_room %s REQUEST_ID --role roommate|visitor|asker [--state DIR]\n", args[0])
		case "resource-mode":
			fmt.Fprintln(diag, "Usage: agent_room resource-mode [host|personal] [--state DIR]\nDefaults to host. Changes cancel in-flight resource calls. Personal credentials stay on the client.")
		case "host":
			fmt.Fprintln(diag, "Usage: agent_room host PROJECT\nExample: agent_room host .")
		default:
			fmt.Fprintf(diag, "Usage: agent_room %s [--state DIR]\n", args[0])
		}
		fs.PrintDefaults()
	}
	address := "0.0.0.0:" + network.DefaultPort
	name := ""
	role := ""
	if args[0] == "approve" || args[0] == "role" {
		fs.StringVar(&role, "role", "", "roommate | visitor | asker (host-assigned)")
	}
	advertise := ""
	webListen := ""
	answerView, noOpen := false, false
	if args[0] != "join" && args[0] != "answers" {
		fs.StringVar(&state, "state", "", "host state directory (default: current Git project session)")
	}
	if args[0] == "host" {
		fs.StringVar(&address, "listen", address, "host listen IP:port (default: saved port, then 7443 or an available port)")
		fs.StringVar(&advertise, "advertise", "", "address to display to participants")
		fs.StringVar(&webListen, "web-listen", "", "read-only web entry IP:port (default: host interface, port 7444 or an available port)")
	}
	if args[0] == "join" || args[0] == "answers" {
		fs.StringVar(&name, "name", "", "your display name (no system account required)")
		fs.BoolVar(&noOpen, "no-open", false, "print the answer window URL without opening a browser")
		if args[0] == "join" {
			fs.BoolVar(&answerView, "answers", false, "open a clean answer window alongside this terminal")
		}
	}
	// Permit flags before or after positional arguments for the documented short commands.
	if err = fs.Parse(interspersed(args[1:])); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	usage := func() int {
		fmt.Fprintln(diag, "usage: agent_room host [--state DIR] [--listen IP:PORT] PROJECT\n       agent_room join IP SESSION_ID --name NAME [--answers]\n       agent_room answers IP SESSION_ID --name NAME [--no-open]\n       agent_room requests|session [--state DIR]\n       agent_room approve|role REQUEST_ID --role roommate|visitor|asker [--state DIR]\n       agent_room deny|revoke REQUEST_ID [--state DIR]")
		return 2
	}
	switch args[0] {
	case "host":
		if fs.NArg() != 1 {
			return usage()
		}
		explicitListen := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "listen" {
				explicitListen = true
			}
		})
		err = runHost(ctx, fs.Arg(0), state, address, advertise, webListen, !explicitListen, out, diag, d)
	case "join", "answers":
		if fs.NArg() != 2 || name == "" {
			return usage()
		}
		if noOpen && !answerView && args[0] != "answers" {
			return usage()
		}
		err = runJoinView(ctx, fs.Arg(0), fs.Arg(1), name, answerView || args[0] == "answers", args[0] == "answers", noOpen, out, diag, d)
	case "resource-mode":
		if fs.NArg() > 1 {
			return usage()
		}
		mode := ""
		if fs.NArg() == 1 {
			mode = fs.Arg(0)
			if mode != "host" && mode != "personal" {
				return usage()
			}
		}
		state, err = resolveState(ctx, state, ".")
		if err == nil {
			err = network.ResourceMode(ctx, filepath.Join(state, "private"), mode, out)
		}
	case "session":
		if fs.NArg() != 0 {
			return usage()
		}
		state, err = resolveState(ctx, state, ".")
		if err != nil {
			break
		}
		var cfg config.RuntimeConfig
		cfg, err = config.Read(filepath.Join(state, "private", "config.json"))
		if err == nil {
			var pin string
			_, pin, err = network.Certificate(filepath.Join(state, "private"))
			if err == nil {
				fmt.Fprintf(out, "session_id: %s.%s\nproject: %s\nstate: %s\n", cfg.RoomID, pin, cfg.ProjectRoot, state)
				endpoint, e := loadHostEndpoint(state)
				if e == nil {
					fmt.Fprintf(out, "Join: agent_room join %s %s.%s --name YOUR_NAME\n", endpoint.Address, cfg.RoomID, pin)
				} else if !errors.Is(e, os.ErrNotExist) {
					err = e
				}
			}
		}
	default:
		id := ""
		if args[0] == "requests" {
			if fs.NArg() != 0 {
				return usage()
			}
		} else {
			if fs.NArg() != 1 {
				return usage()
			}
			id = fs.Arg(0)
		}
		state, err = resolveState(ctx, state, ".")
		if err == nil {
			err = network.ManageRole(ctx, filepath.Join(state, "private"), args[0], id, role, out)
		}
	}
	if err != nil {
		fmt.Fprintln(diag, client.SafeText(err.Error()))
		return 1
	}
	return 0
}

func interspersed(args []string) []string {
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			switch a {
			case "--web-listen", "-web-listen", "--role", "-role", "--state", "--listen", "--advertise", "--name", "-state", "-listen", "-advertise", "-name":
				if i+1 < len(args) {
					i++
					flags = append(flags, args[i])
				}
			}
		} else {
			positionals = append(positionals, a)
		}
	}
	return append(append(flags, "--"), positionals...)
}
func runHost(ctx context.Context, project, state, address, advertise, webListen string, automaticListen bool, out, diag io.Writer, d Dependencies) error {
	if err := d.CheckPlatform(); err != nil {
		return err
	}
	d.CodexExecutable = installedCodex()
	if d.CodexExecutable == "" {
		return errors.New("Codex executable not found on PATH; install Codex and make `codex --version` work in this environment before starting the host")
	}
	root, err := canonicalProject(ctx, project)
	if err != nil {
		return err
	}
	state, err = resolveState(ctx, state, root)
	if err != nil {
		return err
	}
	if automaticListen {
		endpoint, e := loadHostEndpoint(state)
		if e == nil {
			address = endpoint.Listen
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	cfgPath := filepath.Join(state, "private", "config.json")
	if _, err = os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
		u, e := user.Current()
		if e != nil {
			return e
		}
		g, e := user.LookupGroupId(u.Gid)
		if e != nil {
			return e
		}
		fmt.Fprintln(out, "Creating project session (approved members can run work with this host user's authority)...")
		_, err = admin.InitWithDependencies(ctx, d.Admin, admin.InitOptions{Project: root, StateDir: state, Room: filepath.Base(root), ExecutionOwner: u.Username, SharedGroup: g.Name, Members: []string{u.Username}, FullOwnerAccess: true, CodexExecutable: codexPath(d)})
		if err != nil {
			return fmt.Errorf("initialize host: %w", err)
		}
	} else if err != nil {
		return err
	}
	cfg, err := config.Read(cfgPath)
	if err != nil {
		return err
	}
	if cfg.ProjectRoot != root {
		return errors.New("this state belongs to a different project; use --state with a new directory")
	}
	var portal *hostview.Server
	defer func() {
		if portal != nil {
			portal.Close()
		}
	}()
	return serveNetwork(ctx, state, diag, d, func(cfg config.RuntimeConfig, st *store.Store, server *daemon.Server) (*network.Server, error) {
		var remote *network.Server
		var boundAddress string
		err := startWithPortFallback(address, automaticListen && advertise == "", func(candidate string) error {
			var e error
			remote, e = network.Start(ctx, candidate, filepath.Join(state, "private"), cfg.RoomID, st, server)
			boundAddress = candidate
			return e
		})
		if err != nil {
			return nil, err
		}
		remote.EnableResources(resources.Executor{Root: root}.Run, func(ctx context.Context, question, evidence string) (string, error) {
			return codex.SummarizeResource(ctx, codexPath(d), question, evidence)
		})
		if advertise == "" {
			advertise = advertisedAddress(remote.Address())
		}
		host, _, _ := net.SplitHostPort(boundAddress)
		_, port, _ := net.SplitHostPort(remote.Address())
		endpoint := hostEndpoint{Listen: net.JoinHostPort(host, port), Address: advertise}
		if err = saveHostEndpoint(state, endpoint); err != nil {
			remote.Close()
			return nil, err
		}
		webAddress := webListen
		automaticWeb := webAddress == ""
		if automaticWeb {
			webAddress = net.JoinHostPort(host, "7444")
		}
		err = startWithPortFallback(webAddress, automaticWeb, func(candidate string) error {
			var e error
			portal, e = hostview.Start(candidate, hostview.Info{Project: filepath.Base(root), Address: advertise, Session: remote.SessionID()})
			return e
		})
		if err != nil {
			remote.Close()
			return nil, fmt.Errorf("start read-only Host web entry: %w", err)
		}
		webHost, _, _ := net.SplitHostPort(portal.Address())
		if ip := net.ParseIP(webHost); webHost == "" || ip != nil && ip.IsUnspecified() {
			webHost, _, _ = net.SplitHostPort(advertise)
		}
		_, webPort, _ := net.SplitHostPort(portal.Address())
		fmt.Fprintf(out, "Host dashboard (read-only): http://%s/\n", net.JoinHostPort(webHost, webPort))
		if configDir, e := d.UserConfigDir(); e == nil {
			if e = (dashboard.Catalog{Config: configDir}).RememberHost(state); e != nil {
				fmt.Fprintln(diag, "Dashboard room index could not be saved:", e)
			}
		}
		fmt.Fprintf(out, "Host ready\nproject: %s\nlisten: %s\nsession_id: %s\nJoin: agent_room join %s %s --name YOUR_NAME\nReview: agent_room requests --state %s\nBrowser on your computer: agent_room answers %s %s --name YOUR_NAME\nThe local client prints the Browser URL after starting.\n", root, remote.Address(), remote.SessionID(), advertise, remote.SessionID(), state, advertise, remote.SessionID())
		return remote, nil
	})
}
func runJoin(ctx context.Context, host, session, name string, out, diag io.Writer, d Dependencies) error {
	return runJoinView(ctx, host, session, name, false, false, false, out, diag, d)
}
func runJoinView(ctx context.Context, host, session, name string, view, readOnly, noOpen bool, out, diag io.Writer, d Dependencies) error {
	address, err := network.Address(host)
	if err != nil {
		return err
	}
	cfgRoot, err := d.UserConfigDir()
	if err != nil {
		return err
	}
	credential, err := network.CredentialFor(filepath.Join(cfgRoot, "agent_room", "credentials"), address, session, name)
	if err != nil {
		return err
	}
	launcher := network.Launcher{Credential: credential}
	if err = launcher.WaitApproval(ctx, out); err != nil {
		return err
	}
	deps := d.Client
	deps.Launcher = launcher
	onProject := deps.OnProject
	deps.OnProject = func(project string) {
		if onProject != nil {
			onProject(project)
		}
		if err := (dashboard.Catalog{Config: cfgRoot}).RememberProject(address, session, project); err != nil {
			fmt.Fprintln(diag, "Could not save project name for Dashboard:", client.SafeText(err.Error()))
		}
	}
	if !view {
		fmt.Fprintln(out, "Browser: add --answers to this join command, or run agent_room answers HOST_IP SESSION_ID --name YOUR_NAME in another terminal.")
	}
	if view {
		window, err := answerwindow.Start()
		if err != nil {
			return err
		}
		defer window.Close()
		fmt.Fprintf(out, "\nBrowser URL: %s\nURL format: http://127.0.0.1:<local-port>/<random-access-id>/\nThe port and access ID are generated locally; this is not HOST_IP/session_id.\nKeep this process running; Ctrl+C closes its browser service.\n", window.URL())
		// Every new browser view replays task ownership independently of the terminal cursor.
		deps.Cursors = &client.ReplayCursors{}
		window.EnableUploads(launcher.Upload)
		window.EnableDownloads(launcher.Download)
		window.EnableQuestions(launcher.Query)
		window.EnableResources(launcher.Resources)
		window.EnableTasks(launcher.Tasks)
		window.DraftDirectory(filepath.Join(cfgRoot, "agent_room", "drafts"))
		window.Metadata(address, session, name)
		previousProject := deps.OnProject
		deps.OnProject = func(project string) {
			window.Project(project)
			if previousProject != nil {
				previousProject(project)
			}
		}
		deps.OnAnswer = window.Add
		deps.OnEvent = window.Event
		deps.OnMembers = window.Members
		deps.Submissions = window.EnableChat()
		deps.OnRoom = window.Room
		deps.OnActiveTurn = window.ActiveTurn
		deps.OnQueue = window.Queue
		deps.OnConnection = func(connected bool) {
			window.Connection(connected)
			if connected {
				window.RefreshRole(ctx)
			}
		}
		if !noOpen {
			if err := openAnswerWindow(ctx, window.URL()); err != nil {
				fmt.Fprintln(diag, "Could not open a browser; open the Browser URL above.")
			}
		}
	}
	if readOnly {
		deps.ReadOnly = false
		deps.Cursors = &client.ReplayCursors{}
		// Browser submissions use the typed channel; standalone mode ignores stdin.
		reader, writer := io.Pipe()
		defer writer.Close()
		d.Input = reader
		out = io.Discard
	}
	sum := sha256.Sum256([]byte(name))
	target := address + "/" + session + fmt.Sprintf("/%x", sum[:8])
	return client.New(deps).Run(ctx, target, d.Input, out, diag)
}

func openAnswerWindow(ctx context.Context, url string) error {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	cmd := exec.CommandContext(ctx, command, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func codexPath(d Dependencies) string {
	if d.CodexExecutable != "" {
		return d.CodexExecutable
	}
	return "codex"
}
func installedCodex() string {
	path, err := exec.LookPath("codex")
	if err != nil && !errors.Is(err, exec.ErrDot) {
		return ""
	}
	// Honor relative entries explicitly present in PATH, but freeze the
	// executable before the child switches to the project's working directory.
	path, err = filepath.Abs(path)
	if err != nil {
		return ""
	}
	return path
}

func advertisedAddress(bound string) string {
	host, port, err := net.SplitHostPort(bound)
	if err != nil {
		return bound
	}
	ip := net.ParseIP(host)
	if host == "" || (ip != nil && ip.IsUnspecified()) {
		addresses, err := net.InterfaceAddrs()
		if err == nil {
			for _, a := range addresses {
				if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && n.IP.IsGlobalUnicast() {
					return net.JoinHostPort(n.IP.String(), port)
				}
			}
		}
		return net.JoinHostPort("HOST_IP", port)
	}
	return bound
}
