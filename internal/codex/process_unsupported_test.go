//go:build !linux

package codex

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUnsupportedProcessManagerAndSupervisorFailClosed(t *testing.T) {
	manager := NewProcessManager()
	if _, err := manager.Version(context.Background(), "codex"); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("Version err=%v", err)
	}
	if _, err := manager.Start(context.Background(), ProcessSpec{}); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("Start err=%v", err)
	}
	if _, err := manager.Match(context.Background(), ProcessIdentity{}); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("Match err=%v", err)
	}
	if err := manager.StopCurrent(context.Background(), nil, time.Second); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("StopCurrent err=%v", err)
	}
	supervisor, err := NewSupervisor("codex", DefaultVersionPolicy(), manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Start(context.Background(), "/srv/project", discardCheckpoint); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("Supervisor.Start err=%v", err)
	}
}
