package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"agent_romm/internal/buildinfo"
	"agent_romm/internal/codex"
	"agent_romm/internal/config"
	"agent_romm/internal/dashboard"
)

func runVersion(args []string, out, diag io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(diag)
	jsonOutput := fs.Bool("json", false, "machine-readable version")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		return 2
	}
	info := buildinfo.Current()
	if *jsonOutput {
		json.NewEncoder(out).Encode(info)
	} else {
		fmt.Fprintf(out, "agent_room %s (%s, %s/%s)\n", info.Version, info.Commit, info.Platform, info.Arch)
	}
	return 0
}
func runDashboard(ctx context.Context, args []string, out, diag io.Writer, d Dependencies) int {
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	fs.SetOutput(diag)
	var hostStates []string
	fs.Func("host-state", "also index an existing host state directory (repeatable)", func(value string) error {
		absolute, err := filepath.Abs(value)
		if err != nil {
			return err
		}
		if _, err = config.Read(filepath.Join(absolute, "private", "config.json")); err != nil {
			return err
		}
		hostStates = append(hostStates, absolute)
		return nil
	})
	noOpen := fs.Bool("no-open", false, "print the local URL without opening a browser")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(diag, "Usage: agent_room dashboard [--no-open]")
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(diag, err)
		return 1
	}
	configDir, err := d.UserConfigDir()
	if err != nil {
		fmt.Fprintln(diag, err)
		return 1
	}
	catalog := dashboard.Catalog{Home: home, Config: configDir}
	for _, state := range hostStates {
		if err := catalog.RememberHost(state); err != nil {
			fmt.Fprintln(diag, err)
			return 1
		}
	}
	server, err := dashboard.Start(ctx, catalog, nil)
	if err != nil {
		fmt.Fprintln(diag, err)
		return 1
	}
	defer server.Close()
	fmt.Fprintf(out, "Dashboard URL: %s\nKeep this process running. Dashboard disconnect only closes its own client connections; hosts and submitted tasks continue.\n", server.URL())
	if !*noOpen {
		if err := openAnswerWindow(ctx, server.URL()); err != nil {
			fmt.Fprintln(diag, "Could not open a browser. Open the Dashboard URL above.")
		}
	}
	<-ctx.Done()
	return 0
}

type diagnosis struct {
	Status string `json:"status"`
	Check  string `json:"check"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

func runDoctor(ctx context.Context, args []string, out, diag io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(diag)
	host := fs.Bool("host", false, "also check Git and the Codex host runtime")
	machine := fs.Bool("json", false, "machine-readable diagnostics")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		return 2
	}
	var checks []diagnosis
	add := func(status, check, detail, hint string) {
		checks = append(checks, diagnosis{status, check, detail, hint})
	}
	executable, _ := os.Executable()
	resolved, _ := filepath.EvalSymlinks(executable)
	add("PASS", "version", buildinfo.Version+" ("+buildinfo.Commit+")", "")
	add("PASS", "executable", resolved, "")
	if command, e := exec.LookPath("agent_room"); e == nil {
		add("PASS", "PATH", command, "")
	} else {
		add("WARN", "PATH", "agent_room is not on PATH", "Add ~/.local/bin to your shell PATH or run scripts/setup.sh")
	}
	add("PASS", "platform", runtime.GOOS+"/"+runtime.GOARCH, "")
	for _, command := range []string{"node", "npm"} {
		path, err := exec.LookPath(command)
		if err != nil {
			add("WARN", command, "not found", "Node.js 20+ and npm are needed for npm installation and updates; native clients can run without them")
			continue
		}
		bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
		value, err := exec.CommandContext(bounded, path, "--version").Output()
		cancel()
		if err != nil {
			add("WARN", command, "version check failed", "Check the executable in your PATH")
		} else {
			status, hint := "PASS", ""
			if command == "node" {
				var major int
				fmt.Sscanf(strings.TrimSpace(string(value)), "v%d", &major)
				if major < 20 {
					status = "WARN"
					hint = "Install Node.js 20+ for npm updates"
				}
			}
			add(status, command, strings.TrimSpace(string(value)), hint)
		}
	}
	home, _ := os.UserHomeDir()
	managed := filepath.Join(home, ".local", "share", "agent_room", "npm")
	source := "native/source installation"
	if strings.HasPrefix(resolved, managed+string(os.PathSeparator)) {
		source = "user-managed (agent_room-update)"
	} else if strings.Contains(resolved, "node_modules/menmu-agent-room/") {
		source = "npm package (update using the same npm prefix)"
	}
	add("PASS", "installation source", source, "")
	if configDir, err := os.UserConfigDir(); err == nil {
		credentials := filepath.Join(configDir, "agent_room", "credentials")
		info, err := os.Lstat(credentials)
		if err == nil && (!info.IsDir() || info.Mode().Perm()&0077 != 0) {
			add("FAIL", "credential directory", "directory must be private (0700)", "Check permissions on your user configuration directory; do not delete credentials")
		} else if err != nil && !os.IsNotExist(err) {
			add("FAIL", "credential directory", "not accessible", "Check your user configuration directory permissions")
		}
	}
	for _, manifest := range []string{filepath.Join(managed, "current", "node_modules", "menmu-agent-room", "package.json"), filepath.Join(managed, "node_modules", "menmu-agent-room", "package.json")} {
		if b, err := os.ReadFile(manifest); err == nil {
			var pkg struct{ Version string }
			if json.Unmarshal(b, &pkg) == nil {
				add("PASS", "managed installation", pkg.Version+" at "+manifest, "Use agent_room-update --check to check the official registry")
			}
		}
	}
	if *host {
		if runtime.GOOS != "linux" {
			add("FAIL", "host platform", "Hosting requires Linux", "Use join or dashboard on this computer")
		}
		if _, err := canonicalProject(ctx, "."); err != nil {
			add("FAIL", "Git project", "not in an accessible Git project", "Run doctor --host from the project Git root")
		} else {
			add("PASS", "Git project", "available", "")
		}
		path := installedCodex()
		if path == "" {
			add("FAIL", "Codex", "not found", "Install the supported Codex runtime and make codex --version work in this shell")
		} else {
			bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
			value, err := exec.CommandContext(bounded, path, "--version").Output()
			cancel()
			if err != nil || strings.TrimSpace(string(value)) != codex.SupportedCLIOutput {
				add("FAIL", "Codex", "missing or unsupported runtime version", "Validated runtime: @openai/codex@0.153.4; run codex --version")
			} else {
				add("PASS", "Codex", path+" (0.153.4)", "")
			}
		}
	}
	code := 0
	for _, check := range checks {
		if check.Status == "FAIL" {
			code = 1
		}
	}
	if *machine {
		json.NewEncoder(out).Encode(checks)
	} else {
		for _, check := range checks {
			fmt.Fprintf(out, "%s  %s: %s\n", check.Status, check.Check, check.Detail)
			if check.Hint != "" {
				fmt.Fprintln(out, "      "+check.Hint)
			}
		}
	}
	return code
}
