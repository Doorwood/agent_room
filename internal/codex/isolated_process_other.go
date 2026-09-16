//go:build !darwin

package codex

func newIsolatedProcessManager() ProcessManager { return NewProcessManager() }
