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
	"os/user"
	"path/filepath"

	"agent_romm/internal/admin"
	"agent_romm/internal/client"
	"agent_romm/internal/config"
	"agent_romm/internal/daemon"
	"agent_romm/internal/network"
	"agent_romm/internal/store"
)

func isNetworkCommand(s string) bool {
	switch s {
	case "host", "join", "requests", "approve", "deny", "revoke", "session":
		return true
	}
	return false
}
func defaultState() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "agent_room", "host"), nil
}
func runNetwork(ctx context.Context, args []string, out, diag io.Writer, d Dependencies) int {
	state, err := defaultState()
	if err != nil {
		fmt.Fprintln(diag, err)
		return 1
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(diag)
	address := "0.0.0.0:" + network.DefaultPort
	name := ""
	advertise := ""
	if args[0] != "join" {
		fs.StringVar(&state, "state", state, "persistent host state (use a different directory for each session)")
	}
	if args[0] == "host" {
		fs.StringVar(&address, "listen", address, "host listen IP:port")
		fs.StringVar(&advertise, "advertise", "", "address to display to participants")
	}
	if args[0] == "join" {
		fs.StringVar(&name, "name", "", "your display name (no system account required)")
	}
	// Permit flags before or after positional arguments for the documented short commands.
	if err = fs.Parse(interspersed(args[1:])); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	usage := func() int {
		fmt.Fprintln(diag, "usage: agent_room host [--state DIR] [--listen IP:PORT] PROJECT\n       agent_room join IP SESSION_ID --name NAME\n       agent_room requests|session [--state DIR]\n       agent_room approve|deny|revoke REQUEST_ID [--state DIR]")
		return 2
	}
	switch args[0] {
	case "host":
		if fs.NArg() != 1 {
			return usage()
		}
		err = runHost(ctx, fs.Arg(0), state, address, advertise, out, diag, d)
	case "join":
		if fs.NArg() != 2 || name == "" {
			return usage()
		}
		err = runJoin(ctx, fs.Arg(0), fs.Arg(1), name, out, diag, d)
	case "session":
		if fs.NArg() != 0 {
			return usage()
		}
		var cfg config.RuntimeConfig
		cfg, err = config.Read(filepath.Join(state, "private", "config.json"))
		if err == nil {
			var pin string
			_, pin, err = network.Certificate(filepath.Join(state, "private"))
			if err == nil {
				fmt.Fprintf(out, "session_id: %s.%s\nproject: %s\nstate: %s\n", cfg.RoomID, pin, cfg.ProjectRoot, state)
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
		err = network.Manage(ctx, filepath.Join(state, "private"), args[0], id, out)
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
			case "--state", "--listen", "--advertise", "--name", "-state", "-listen", "-advertise", "-name":
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
func runHost(ctx context.Context, project, state, address, advertise string, out, diag io.Writer, d Dependencies) error {
	if err := d.CheckPlatform(); err != nil {
		return err
	}
	d.CodexExecutable = installedCodex()
	root, err := filepath.Abs(project)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(state) {
		state, err = filepath.Abs(state)
		if err != nil {
			return err
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
	return serveNetwork(ctx, state, diag, d, func(cfg config.RuntimeConfig, st *store.Store, server *daemon.Server) (*network.Server, error) {
		remote, err := network.Start(ctx, address, filepath.Join(state, "private"), cfg.RoomID, st, server)
		if err != nil {
			return nil, err
		}
		if advertise == "" {
			advertise = advertisedAddress(remote.Address())
		}
		fmt.Fprintf(out, "Host ready\nproject: %s\nlisten: %s\nsession_id: %s\nJoin: agent_room join %s %s --name YOUR_NAME\nReview: agent_room requests --state %s\n", root, remote.Address(), remote.SessionID(), advertise, remote.SessionID(), state)
		return remote, nil
	})
}
func runJoin(ctx context.Context, host, session, name string, out, diag io.Writer, d Dependencies) error {
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
	sum := sha256.Sum256([]byte(name))
	target := address + "/" + session + fmt.Sprintf("/%x", sum[:8])
	return client.New(deps).Run(ctx, target, d.Input, out, diag)
}

func codexPath(d Dependencies) string {
	if d.CodexExecutable != "" {
		return d.CodexExecutable
	}
	return "codex"
}
func installedCodex() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "codex"
	}
	for _, rel := range []string{".local/share/agent_room/codex/node_modules/.bin/codex", ".local/share/agent-romm-runtime/node_modules/.bin/codex"} {
		p := filepath.Join(home, rel)
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return p
		}
	}
	return "codex"
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
