// Package gitview provides bounded, read-only Git projections.
package gitview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

var ErrOutputLimit = errors.New("git output exceeds limit")
var ErrUnsafeFilter = errors.New("Git projections unavailable: repository configures executable clean/process filters")

// View projects one repository. It includes gitlink commit changes but omits
// submodule worktree dirt; it is not a recursive submodule worktree view.
type View struct{ Root string }

func (v View) Status(ctx context.Context) ([]byte, error) {
	return v.project(ctx, "status", "--short", "--untracked-files=all", "--ignore-submodules=dirty")
}
func (v View) Diff(ctx context.Context) ([]byte, error) {
	return v.project(ctx, "diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty", "--binary", "--")
}

// Projections include gitlink changes but never scan submodule worktree dirt.
// Their local configuration and attributes are outside this repository's
// preflight boundary. Disable summaries and extended submodule diff formats too.
func (v View) project(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Git itself resolves local/worktree configuration, conditional includes,
	// and include paths. Merely reading config does not run filter commands.
	// Reject even empty configured values so no filter-name enumeration or
	// incomplete command-string denylist can become an execution bypass.
	configured, err := v.run(ctx, "config", "--includes", "--null", "--get-regexp", `^filter\..*\.(clean|process)$`)
	if err == nil {
		return nil, ErrUnsafeFilter
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || len(configured) != 0 {
		return nil, err
	}
	return v.run(ctx, args...)
}
func (v View) TopLevel(ctx context.Context) (string, error) {
	b, e := v.run(ctx, "rev-parse", "--show-toplevel")
	return strings.TrimSuffix(string(b), "\n"), e
}

type boundedWriter struct {
	buffer   bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	overflow bool
}

func (w *boundedWriter) Len() int       { return w.buffer.Len() }
func (w *boundedWriter) Bytes() []byte  { return w.buffer.Bytes() }
func (w *boundedWriter) String() string { return w.buffer.String() }

func (w *boundedWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.Len() {
		n, _ := w.buffer.Write(p[:w.limit-w.Len()])
		w.overflow = true
		w.cancel()
		return n, ErrOutputLimit
	}
	return w.buffer.Write(p)
}
func (v View) run(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	argv := append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "status.submoduleSummary=false", "-c", "diff.submodule=short"}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = v.Root
	cmd.WaitDelay = time.Second
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	stdout := &boundedWriter{limit: 4 << 20, cancel: cancel}
	stderr := &boundedWriter{limit: 64 << 10, cancel: cancel}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if stdout.overflow || stderr.overflow {
		return nil, ErrOutputLimit
	}
	if err != nil {
		return nil, fmt.Errorf("git: %w: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}
