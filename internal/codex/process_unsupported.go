//go:build !linux

package codex

import (
	"context"
	"time"
)

type unsupportedProcessManager struct{}

func newPlatformProcessManager() ProcessManager { return &unsupportedProcessManager{} }

func (*unsupportedProcessManager) Version(context.Context, string) (string, error) {
	return "", ErrUnsupportedPlatform
}

func (*unsupportedProcessManager) Start(context.Context, ProcessSpec) (*Child, error) {
	return nil, ErrUnsupportedPlatform
}

func (*unsupportedProcessManager) Match(context.Context, ProcessIdentity) (ProcessMatch, error) {
	return ProcessUnknown, ErrUnsupportedPlatform
}

func (*unsupportedProcessManager) StopCurrent(context.Context, *Child, time.Duration) error {
	return ErrUnsupportedPlatform
}
