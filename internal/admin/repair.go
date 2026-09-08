package admin

import (
	"agent_romm/internal/codex"
	"agent_romm/internal/config"
	"agent_romm/internal/room"
	"agent_romm/internal/store"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrCandidateNotReviewed = errors.New("repair requires an explicitly reviewed candidate and reason")
var ErrThreadNeedsRepair = errors.New("thread creation result uncertain; owner repair required")

type RepairOptions struct {
	StateDir, CodexExecutable, Reason string
	Reviewed                          bool
}
type RepairSession interface {
	ReadThread(context.Context, room.ThreadID) (room.ThreadSnapshot, error)
	StartThread(context.Context) (room.ThreadSnapshot, error)
	Close(context.Context) error
}
type RepairDependencies struct {
	Admin Dependencies
	Start func(context.Context, config.RuntimeConfig, *store.Store, string) (RepairSession, error)
}
type repairSession struct {
	s       *codex.Supervisor
	session *codex.Session
}

func (s *repairSession) ReadThread(ctx context.Context, id room.ThreadID) (room.ThreadSnapshot, error) {
	return s.session.Adapter.ReadThread(ctx, id)
}
func (s *repairSession) StartThread(ctx context.Context) (room.ThreadSnapshot, error) {
	return s.session.Adapter.StartThread(ctx)
}
func (s *repairSession) Close(ctx context.Context) error { return s.s.Stop(ctx, s.session) }
func DefaultRepairDependencies() RepairDependencies {
	return RepairDependencies{DefaultDependencies(), func(ctx context.Context, c config.RuntimeConfig, db *store.Store, executable string) (RepairSession, error) {
		if executable == "" {
			executable = "codex"
		}
		s, e := codex.NewDefaultSupervisor(executable)
		if e != nil {
			return nil, e
		}
		image, e := db.LoadRecoveryImage(ctx, c.RoomID)
		if e != nil {
			return nil, e
		}
		if e = s.ReapPrevious(ctx, &image.Checkpoint); e != nil {
			return nil, e
		}
		session, e := s.Start(ctx, c.ProjectRoot, func(ctx context.Context, cp room.RuntimeCheckpoint) error {
			return db.SaveRuntimeCheckpoint(ctx, c.RoomID, cp)
		})
		if e != nil {
			return nil, e
		}
		return &repairSession{s, session}, nil
	}}
}
func RepairUse(ctx context.Context, o RepairOptions, id room.ThreadID) error {
	return RepairUseWithDependencies(ctx, DefaultRepairDependencies(), o, id)
}
func RepairCreate(ctx context.Context, o RepairOptions) error {
	return RepairCreateWithDependencies(ctx, DefaultRepairDependencies(), o)
}
func RepairUseWithDependencies(ctx context.Context, d RepairDependencies, o RepairOptions, id room.ThreadID) error {
	if !o.Reviewed || strings.TrimSpace(string(id)) == "" || strings.TrimSpace(o.Reason) == "" {
		return ErrCandidateNotReviewed
	}
	return repair(ctx, d, o, id, false)
}
func RepairCreateWithDependencies(ctx context.Context, d RepairDependencies, o RepairOptions) error {
	if strings.TrimSpace(o.Reason) == "" {
		return ErrCandidateNotReviewed
	}
	return repair(ctx, d, o, "", true)
}
func repair(ctx context.Context, d RepairDependencies, o RepairOptions, id room.ThreadID, create bool) (err error) {
	c, lock, e := ValidateServeWithDependencies(ctx, d.Admin, o.StateDir)
	if e != nil {
		return e
	}
	defer lock.Close()
	db, e := store.Open(ctx, c.DatabasePath)
	if e != nil {
		return e
	}
	defer db.Close()
	session, e := d.Start(ctx, c, db, o.CodexExecutable)
	if e != nil {
		return e
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = errors.Join(err, session.Close(cleanup))
	}()
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(o.Reason)))
	reason := room.RepairReason{Code: "owner-use", DetailDigest: digest}
	var snapshot room.ThreadSnapshot
	if create {
		reason.Code = "owner-create"
		image, e := db.LoadRecoveryImage(ctx, c.RoomID)
		if e != nil {
			return e
		}
		if image.Status != room.RoomThreadNeedsRepair {
			if _, e = db.MarkThreadNeedsRepair(ctx, c.RoomID, room.RepairReason{Code: "repair-create-pending", DetailDigest: digest}); e != nil {
				return e
			}
		}
		snapshot, e = session.StartThread(ctx)
		if e != nil {
			return errors.Join(ErrThreadNeedsRepair, e)
		}
	} else {
		snapshot, e = session.ReadThread(ctx, id)
		if e != nil {
			return e
		}
		if snapshot.ID != id {
			return ErrCandidateNotReviewed
		}
	}
	if snapshot.CWD != c.ProjectRoot {
		return ErrProjectNotGitRoot
	}
	_, e = db.RepairThread(ctx, c.RoomID, snapshot, reason)
	return e
}
