package admin

import (
	"agent_romm/internal/config"
	"agent_romm/internal/room"
	"agent_romm/internal/store"
	"context"
	"errors"
	"testing"
)

type fakeRepairSession struct {
	snapshot              room.ThreadSnapshot
	err                   error
	starts, reads, closes int
}

func (s *fakeRepairSession) ReadThread(context.Context, room.ThreadID) (room.ThreadSnapshot, error) {
	s.reads++
	return s.snapshot, s.err
}
func (s *fakeRepairSession) StartThread(context.Context) (room.ThreadSnapshot, error) {
	s.starts++
	return s.snapshot, s.err
}
func (s *fakeRepairSession) Close(context.Context) error { s.closes++; return nil }
func TestRepairLockAndCandidateAndUnknownCreation(t *testing.T) {
	for _, kind := range []string{"locked", "not-reviewed", "wrong-cwd", "wrong-id", "use", "unknown-create", "create"} {
		t.Run(kind, func(t *testing.T) {
			d, o := fixture(t)
			ctx := context.Background()
			c, e := InitWithDependencies(ctx, d, o)
			if e != nil {
				t.Fatal(e)
			}
			session := &fakeRepairSession{snapshot: room.ThreadSnapshot{ID: "candidate", CWD: c.ProjectRoot}}
			started := 0
			deps := RepairDependencies{Admin: d, Start: func(context.Context, config.RuntimeConfig, *store.Store, string) (RepairSession, error) {
				started++
				return session, nil
			}}
			opts := RepairOptions{StateDir: o.StateDir, Reason: "owner-requested", Reviewed: true}
			switch kind {
			case "locked":
				_, l, e := ValidateServeWithDependencies(ctx, d, o.StateDir)
				if e != nil {
					t.Fatal(e)
				}
				defer l.Close()
			case "not-reviewed":
				opts.Reviewed = false
			case "wrong-cwd":
				session.snapshot.CWD += "/other"
			case "wrong-id":
				session.snapshot.ID = "other"
			case "unknown-create":
				session.err = &room.MutationError{Operation: "thread/start", Certainty: room.DeliveryUnknown, Err: errors.New("lost response")}
			}
			if kind == "unknown-create" || kind == "create" {
				e = RepairCreateWithDependencies(ctx, deps, opts)
			} else {
				e = RepairUseWithDependencies(ctx, deps, opts, "candidate")
			}
			if kind == "use" || kind == "create" {
				if e != nil {
					t.Fatal(e)
				}
			} else if e == nil {
				t.Fatal("expected failure")
			}
			if kind == "locked" || kind == "not-reviewed" {
				if started != 0 {
					t.Fatal("started model before validation")
				}
				return
			}
			if session.closes != 1 {
				t.Fatal("session leaked")
			}
			db, e := store.Open(ctx, c.DatabasePath)
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			image, e := db.LoadRecoveryImage(ctx, c.RoomID)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "use", "create":
				if image.ThreadID != "candidate" || image.Status != room.RoomRecovering {
					t.Fatalf("%+v", image)
				}
			case "unknown-create":
				if session.starts != 1 || image.Status != room.RoomThreadNeedsRepair || image.ThreadID != "" {
					t.Fatalf("%+v calls=%d", image, session.starts)
				}
			default:
				if image.ThreadID != "" || session.starts != 0 {
					t.Fatal("invalid candidate changed binding")
				}
			}
		})
	}
}
