//go:build darwin

package codex

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Only ephemeral, child-owned sessions use this manager. Host checkpoint
// recovery still uses the Linux-only manager; persisted PIDs are never adopted.
type darwinIsolatedProcessManager struct {
	unsupportedProcessManager
	owner *processOwnerToken
}

func newIsolatedProcessManager() ProcessManager {
	return &darwinIsolatedProcessManager{owner: &processOwnerToken{}}
}

func (m *darwinIsolatedProcessManager) Start(ctx context.Context, spec ProcessSpec) (*Child, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(spec.Executable) == "" || !filepath.IsAbs(spec.Dir) || strings.TrimSpace(spec.Generation) == "" || len(spec.Args) == 0 {
		return nil, ErrInvalidProcessSpec
	}
	cmd := exec.Command(spec.Executable, spec.Args...)
	cmd.Dir, cmd.Env = spec.Dir, append([]string(nil), spec.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pipes, err := openProcessPipes(cmd)
	if err != nil {
		return nil, ErrProcessStart
	}
	if err = cmd.Start(); err != nil {
		pipes.closeAll()
		return nil, ErrProcessStart
	}
	pipes.closeChildEnds()
	pid := cmd.Process.Pid
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info.Proc.P_pid != int32(pid) || info.Eproc.Pgid != int32(pid) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		pipes.closeParentEnds()
		return nil, ErrProcessIdentityRead
	}
	start := info.Proc.P_starttime
	// Sysctl observes the unreaped child, including its zombie state. Only the
	// shared ownership lock calls Wait, so its PID cannot be reused before a signal.
	observe := func(nonblocking bool) (bool, error) {
		for {
			current, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
			if err != nil {
				return false, err
			}
			if current.Proc.P_pid != int32(pid) || current.Proc.P_starttime != start {
				return false, ErrProcessIdentityRead
			}
			const zombie = 5 // Darwin sys/proc.h: SZOMB.
			if current.Proc.P_stat == zombie {
				return true, nil
			}
			if current.Eproc.Pgid != int32(pid) {
				return false, ErrInvalidCurrentProcess
			}
			if nonblocking {
				return false, nil
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	hooks := processHooks{observeExit: observe, reap: cmd.Wait, signalGroup: func(signal processSignal) error {
		sig := unix.SIGTERM
		if signal == processSignalKill {
			sig = unix.SIGKILL
		}
		if err := unix.Kill(-pid, sig); err != nil {
			if errors.Is(err, unix.ESRCH) {
				if exited, checkErr := observe(true); checkErr == nil && exited {
					return nil
				}
			}
			return err
		}
		return nil
	}}
	identity := ProcessIdentity{Generation: spec.Generation, PID: pid, PGID: pid, StartIdentity: fmt.Sprintf("%d:%d", start.Sec, start.Usec)}
	return newOwnedChild(m.owner, identity, pipes.stdin, pipes.stdout, pipes.stderr, hooks), nil
}

func (m *darwinIsolatedProcessManager) StopCurrent(ctx context.Context, child *Child, grace time.Duration) error {
	return stopOwnedChild(ctx, m.owner, child, grace)
}
