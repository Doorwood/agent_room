//go:build linux

package codex

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const maxVersionOutputBytes = 4096

type linuxProcessManager struct {
	owner *processOwnerToken
}

func newPlatformProcessManager() ProcessManager {
	return &linuxProcessManager{owner: &processOwnerToken{}}
}

func (m *linuxProcessManager) Version(ctx context.Context, executable string) (string, error) {
	if strings.TrimSpace(executable) == "" {
		return "", ErrInvalidProcessSpec
	}
	var stdout boundedProcessOutput
	stdout.limit = maxVersionOutputBytes
	command := exec.CommandContext(ctx, executable, "--version")
	command.Stdout = &stdout
	command.Stderr = io.Discard
	if err := command.Run(); err != nil || stdout.overflow {
		return "", ErrProcessVersion
	}
	return stdout.String(), nil
}

func (m *linuxProcessManager) Start(ctx context.Context, spec ProcessSpec) (*Child, error) {
	if err := validateProcessSpec(ctx, spec); err != nil {
		return nil, err
	}
	// Deliberately do not use CommandContext: after Start, only the child-owned
	// lifecycle lock may signal or reap this process.
	command := exec.Command(spec.Executable, spec.Args...)
	command.Dir = spec.Dir
	command.Env = append([]string(nil), spec.Env...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	pipes, err := openProcessPipes(command)
	if err != nil {
		return nil, ErrProcessStart
	}
	if err := command.Start(); err != nil {
		pipes.closeAll()
		return nil, ErrProcessStart
	}
	pipes.closeChildEnds()
	pid := command.Process.Pid
	pgid, err := unix.Getpgid(pid)
	if err != nil || pgid != pid {
		killAndWait(command, 0)
		pipes.closeParentEnds()
		return nil, ErrProcessIdentityRead
	}
	actual, err := readLinuxProcessIdentity(pid)
	if err != nil || actual.PGID != pgid || actual.StartIdentity == "" {
		killAndWait(command, pgid)
		pipes.closeParentEnds()
		return nil, ErrProcessIdentityRead
	}
	identity := ProcessIdentity{
		Generation: spec.Generation, PID: pid, PGID: pgid, StartIdentity: actual.StartIdentity,
	}
	hooks := processHooks{
		observeExit: func(nonblocking bool) (bool, error) {
			options := unix.WEXITED | unix.WNOWAIT
			if nonblocking {
				options |= unix.WNOHANG
			}
			var info unix.Siginfo
			if err := unix.Waitid(unix.P_PID, pid, &info, options, nil); err != nil {
				return false, err
			}
			return info.Signo != 0, nil
		},
		signalGroup: func(signal processSignal) error {
			unixSignal := unix.SIGTERM
			if signal == processSignalKill {
				unixSignal = unix.SIGKILL
			}
			if err := unix.Kill(-pgid, unixSignal); err != nil {
				if !errors.Is(err, unix.ESRCH) {
					return err
				}
				// ESRCH is safe only if the original child exited between the
				// non-reaping observation and the group signal. A live child that
				// escaped its dedicated group must fail closed instead of hanging.
				var info unix.Siginfo
				if waitErr := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil); waitErr != nil || info.Signo == 0 {
					return err
				}
			}
			return nil
		},
		reap: command.Wait,
	}
	return newOwnedChild(m.owner, identity, pipes.stdin, pipes.stdout, pipes.stderr, hooks), nil
}

func (m *linuxProcessManager) Match(ctx context.Context, identity ProcessIdentity) (ProcessMatch, error) {
	if identity.PID <= 1 || identity.PGID <= 1 || strings.TrimSpace(identity.Generation) == "" || strings.TrimSpace(identity.StartIdentity) == "" {
		return ProcessUnknown, ErrProcessIdentityRead
	}
	first, err := readLinuxProcessIdentity(identity.PID)
	if errors.Is(err, errLinuxProcessAbsent) {
		return ProcessAbsent, nil
	}
	if err != nil {
		return ProcessUnknown, nil
	}
	if first.PID != identity.PID || first.PGID != identity.PGID || first.StartIdentity != identity.StartIdentity {
		return ProcessMismatch, nil
	}
	if err := ctx.Err(); err != nil {
		return ProcessUnknown, err
	}
	second, err := readLinuxProcessIdentity(identity.PID)
	if err != nil || second != first {
		return ProcessUnknown, ErrProcessIdentityChanged
	}
	return ProcessMatches, nil
}

func (m *linuxProcessManager) StopCurrent(ctx context.Context, child *Child, grace time.Duration) error {
	return stopOwnedChild(ctx, m.owner, child, grace)
}

func validateProcessSpec(ctx context.Context, spec ProcessSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(spec.Executable) == "" || !filepath.IsAbs(spec.Dir) || strings.TrimSpace(spec.Generation) == "" || len(spec.Args) == 0 {
		return ErrInvalidProcessSpec
	}
	return nil
}

var errLinuxProcessAbsent = errors.New("Linux process is absent")

func readLinuxProcessIdentity(pid int) (ProcessIdentity, error) {
	data, err := readProcStat(pid)
	if err != nil {
		return ProcessIdentity{}, err
	}
	closeParen := bytes.LastIndexByte(data, ')')
	openParen := bytes.IndexByte(data, '(')
	if openParen <= 0 || closeParen <= openParen || closeParen+1 >= len(data) {
		return ProcessIdentity{}, ErrProcessIdentityRead
	}
	parsedPID, err := strconv.Atoi(strings.TrimSpace(string(data[:openParen])))
	if err != nil || parsedPID != pid {
		return ProcessIdentity{}, ErrProcessIdentityRead
	}
	fields := strings.Fields(string(data[closeParen+1:]))
	// fields[0] is field 3 (state), fields[2] is field 5 (pgrp), and
	// fields[19] is field 22 (starttime).
	if len(fields) <= 19 {
		return ProcessIdentity{}, ErrProcessIdentityRead
	}
	pgid, err := strconv.Atoi(fields[2])
	if err != nil || pgid <= 1 {
		return ProcessIdentity{}, ErrProcessIdentityRead
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return ProcessIdentity{}, ErrProcessIdentityRead
	}
	return ProcessIdentity{PID: pid, PGID: pgid, StartIdentity: fields[19]}, nil
}

func readProcStat(pid int) ([]byte, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) {
		return nil, errLinuxProcessAbsent
	}
	if err != nil {
		return nil, ErrProcessIdentityRead
	}
	return data, nil
}

func killAndWait(command *exec.Cmd, pgid int) {
	if pgid > 1 {
		_ = unix.Kill(-pgid, unix.SIGKILL)
	}
	if command.Process != nil {
		_ = command.Process.Kill()
	}
	_ = command.Wait()
}

type boundedProcessOutput struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (w *boundedProcessOutput) Write(data []byte) (int, error) {
	original := len(data)
	remaining := w.limit - w.Len()
	if remaining < len(data) {
		w.overflow = true
		if remaining < 0 {
			remaining = 0
		}
		data = data[:remaining]
	}
	_, _ = w.Buffer.Write(data)
	return original, nil
}
