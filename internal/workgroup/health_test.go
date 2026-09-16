package workgroup

import (
	"agent_romm/internal/room"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHealthProbeVerifiesReadAndWrite(t *testing.T) {
	for _, mode := range []string{"review", "work"} {
		for _, broken := range []bool{false, true} {
			err := checkHealth(context.Background(), "codex", mode, func(_ context.Context, _ Member, a Assignment) (Result, error) {
				b, _ := os.ReadFile(filepath.Join(a.ProjectRoot, "challenge.txt"))
				if broken {
					return Result{Text: "OK"}, nil
				}
				if mode == "work" {
					os.WriteFile(filepath.Join(a.ProjectRoot, "result.txt"), b, 0600)
				}
				return Result{Text: string(b)}, nil
			})
			if (err != nil) != broken {
				t.Fatalf("mode %s broken %v: %v", mode, broken, err)
			}
		}
	}
}
func TestHealthProbeRequiresActualWrite(t *testing.T) {
	err := checkHealth(context.Background(), "codex", "work", func(_ context.Context, _ Member, a Assignment) (Result, error) {
		b, _ := os.ReadFile(filepath.Join(a.ProjectRoot, "challenge.txt"))
		return Result{Text: string(b)}, nil
	})
	if err == nil {
		t.Fatal("accepted missing write")
	}
}
func TestRemoteHealthAdmission(t *testing.T) {
	b := NewRemoteBroker()
	uid := room.UID(17)
	r, e := b.Handle(uid, WorkerRequest{Action: "invite", ID: "0123456789abcdef0123456789abcdef", Provider: "codex", Mode: "review"})
	if e != nil {
		t.Fatal(e)
	}
	if len(b.Members()) != 0 {
		t.Fatal("unchecked worker advertised")
	}
	if _, e = b.Run(context.Background(), Member{ID: r.ID}, Assignment{}); e == nil {
		t.Fatal("unchecked worker dispatched")
	}
	if _, e = b.Handle(uid+1, WorkerRequest{Action: "health", ID: r.ID, Healthy: true}); e == nil {
		t.Fatal("foreign health accepted")
	}
	if _, e = b.Handle(uid, WorkerRequest{Action: "health", ID: r.ID, Healthy: true}); e != nil {
		t.Fatal(e)
	}
	if len(b.Members()) != 1 {
		t.Fatal("healthy worker missing")
	}
	b.Handle(uid, WorkerRequest{Action: "health", ID: r.ID, Healthy: false})
	b.Handle(uid, WorkerRequest{Action: "poll", ID: r.ID})
	if len(b.Members()) != 0 {
		t.Fatal("heartbeat bypassed health")
	}
}
func TestLiveHealthCodex(t *testing.T) {
	if os.Getenv("AGENT_ROOM_LIVE_HEALTH") != "1" {
		t.Skip("opt-in real model check")
	}
	for _, mode := range []string{"review", "work"} {
		t.Run(mode, func(t *testing.T) {
			if e := HealthCheck(context.Background(), "codex", mode); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestHealthExpiresAndRevivalRequiresProbe(t *testing.T) {
	b := NewRemoteBroker()
	req := WorkerRequest{Action: "invite", ID: "0123456789abcdef0123456789abcdef", Provider: "codex", Mode: "review"}
	inv, _ := b.Handle(7, req)
	b.Handle(7, WorkerRequest{Action: "health", ID: inv.ID, Healthy: true})
	b.workers[inv.ID].healthyUntil = time.Now().Add(-time.Second)
	if len(b.Members()) != 0 {
		t.Fatal("expired health admitted")
	}
	b.Handle(7, WorkerRequest{Action: "health", ID: inv.ID, Healthy: true})
	b.workers[inv.ID].lease = time.Now().Add(-time.Second)
	b.Handle(7, req)
	if len(b.Members()) != 0 {
		t.Fatal("revival retained health")
	}
	b.Handle(7, WorkerRequest{Action: "health", ID: inv.ID, Healthy: true})
	b.Handle(7, WorkerRequest{Action: "leave", ID: inv.ID})
	b.Handle(7, req)
	if len(b.Members()) != 0 {
		t.Fatal("leave retained health")
	}
}
