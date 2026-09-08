package codex

import (
	"context"
	"errors"
	"testing"
)

func TestSupervisorStartsReviewedStableRuntimeAndRejectsOtherVersions(t *testing.T) {
	for _, version := range []string{"codex-cli 0.153.4", "codex-cli 0.151.0-alpha.7.2", "codex-cli 0.153.40", "codex-cli 0.154.0"} {
		t.Run(version, func(t *testing.T) {
			pm := &fakeProcessManager{cliVersion: version}
			s := mustSupervisor(t, pm)
			child, err := s.Start(context.Background(), "/srv/project", discardCheckpoint)
			if version != "codex-cli 0.153.4" {
				if !errors.Is(err, ErrUnsupportedCodexVersion) || pm.startCallCount() != 0 {
					t.Fatalf("unreviewed runtime started: child=%v error=%v", child, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Stop(context.Background(), child); err != nil {
				t.Fatal(err)
			}
		})
	}
}
