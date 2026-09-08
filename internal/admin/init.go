// Package admin owns owner-only initialization and maintenance entry points.
package admin

import (
	"agent_romm/internal/codex"
	"agent_romm/internal/config"
	"agent_romm/internal/gitview"
	"agent_romm/internal/room"
	"agent_romm/internal/store"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

var ErrExecutionOwnerMismatch = errors.New("execution owner does not match effective UID")
var ErrProjectNotGitRoot = errors.New("project must be the canonical Git top level")
var ErrSharedGroup = errors.New("all members must belong to explicit shared group")

type InitOptions struct {
	Project, StateDir, Room, ExecutionOwner string
	Members                                 []string
	SharedGroup                             string
	FullOwnerAccess                         bool
	CodexExecutable                         string
}

// Dependencies is for explicit embedding and deterministic tests. CLI production
// commands must use DefaultDependencies; there are no environment bypasses.
type Dependencies struct {
	EUID            func() int
	LookupUser      func(string) (*user.User, error)
	LookupGroup     func(string) (*user.Group, error)
	GroupIDs        func(*user.User) ([]string, error)
	Random          io.Reader
	Probe           func(context.Context, string, string) (room.RuntimeCheckpoint, error)
	ProcessIdentity func(int) (string, error)
}

func DefaultDependencies() Dependencies {
	return Dependencies{os.Geteuid, user.Lookup, user.LookupGroup, func(u *user.User) ([]string, error) { return u.GroupIds() }, rand.Reader, func(ctx context.Context, executable, root string) (room.RuntimeCheckpoint, error) {
		s, e := codex.NewDefaultSupervisor(executable)
		if e != nil {
			return room.RuntimeCheckpoint{}, e
		}
		return s.Probe(ctx, root)
	}, processIdentity}
}
func Init(ctx context.Context, o InitOptions) (config.RuntimeConfig, error) {
	return InitWithDependencies(ctx, DefaultDependencies(), o)
}
func resolveOwner(d Dependencies, name string) (*user.User, room.UID, error) {
	u, e := d.LookupUser(name)
	if e != nil {
		return nil, 0, e
	}
	id, e := strconv.ParseUint(u.Uid, 10, 32)
	if e != nil {
		return nil, 0, e
	}
	if id == 0 || int(id) != d.EUID() {
		return nil, 0, ErrExecutionOwnerMismatch
	}
	return u, room.UID(id), nil
}
func validateProject(ctx context.Context, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", ErrProjectNotGitRoot
	}
	root, e := filepath.EvalSymlinks(path)
	if e != nil {
		return "", e
	}
	info, e := os.Stat(root)
	if e != nil || !info.IsDir() {
		return "", ErrProjectNotGitRoot
	}
	top, e := (gitview.View{Root: root}).TopLevel(ctx)
	if e != nil || top != root {
		return "", ErrProjectNotGitRoot
	}
	return root, nil
}
func members(d Dependencies, names []string, owner *user.User, gid string) ([]room.Member, error) {
	names = append(append([]string(nil), names...), owner.Username)
	seen := map[room.UID]bool{}
	out := []room.Member{}
	for _, name := range names {
		u, e := d.LookupUser(name)
		if e != nil {
			return nil, e
		}
		id, e := strconv.ParseUint(u.Uid, 10, 32)
		if e != nil || id == 0 {
			return nil, ErrSharedGroup
		}
		groups, e := d.GroupIDs(u)
		if e != nil {
			return nil, e
		}
		found := false
		for _, g := range groups {
			if g == gid {
				found = true
			}
		}
		if !found {
			return nil, ErrSharedGroup
		}
		if !seen[room.UID(id)] {
			out = append(out, room.Member{UID: room.UID(id), Name: u.Username})
			seen[room.UID(id)] = true
		}
	}
	return out, nil
}
func InitWithDependencies(ctx context.Context, d Dependencies, o InitOptions) (result config.RuntimeConfig, err error) {
	owner, uid, e := resolveOwner(d, o.ExecutionOwner)
	if e != nil {
		return result, e
	}
	if !o.FullOwnerAccess {
		return result, errors.New("full owner access acknowledgment required")
	}
	if strings.TrimSpace(o.Room) == "" || o.SharedGroup == "" {
		return result, ErrSharedGroup
	}
	group, e := d.LookupGroup(o.SharedGroup)
	if e != nil {
		return result, e
	}
	gid, e := strconv.ParseUint(group.Gid, 10, 32)
	if e != nil {
		return result, e
	}
	ms, e := members(d, o.Members, owner, group.Gid)
	if e != nil {
		return result, e
	}
	root, e := validateProject(ctx, o.Project)
	if e != nil {
		return result, e
	}
	l, e := config.NewLayout(o.StateDir, root)
	if e != nil {
		return result, e
	}
	if _, e = os.Lstat(l.StateDir); e == nil {
		if e = checkPath(l.StateDir, int(uid), int(gid), 0710, true); e != nil {
			return result, e
		}
	} else if !os.IsNotExist(e) {
		return result, e
	}
	if _, e = os.Lstat(l.PrivateDir); !os.IsNotExist(e) {
		if e == nil {
			e = os.ErrExist
		}
		return result, e
	}
	ids := [2]string{}
	for i := range ids {
		b := make([]byte, 16)
		if _, e = io.ReadFull(d.Random, b); e != nil {
			return result, e
		}
		ids[i] = hex.EncodeToString(b)
	}
	// Readiness creates no permanent room state and leaves a stopped checkpoint.
	executable := o.CodexExecutable
	if executable == "" {
		executable = "codex"
	}
	cp, e := d.Probe(ctx, executable, root)
	if e != nil {
		return result, e
	}
	if cp.State != room.RuntimeProcessStopped {
		return result, errors.New("probe did not stop")
	}
	result = config.RuntimeConfig{SchemaVersion: 1, RoomID: room.RoomID(ids[0]), HostID: ids[1], RoomName: o.Room, ProjectRoot: root, ExecutionOwnerUID: uid, ExecutionOwnerName: owner.Username, FullOwnerAccess: true, SharedGroupGID: uint32(gid), SocketPath: l.SocketPath, DatabasePath: l.DatabasePath, Members: ms, CodexVersion: cp.CodexVersion, SchemaSHA256: cp.SchemaSHA256}
	if e = result.Validate(l); e != nil {
		return result, e
	}
	// Only directories made by this attempt are rollback targets. Existing state
	// must be privately owned and is never recursively removed.
	created := []string{}
	committed := false
	defer func() {
		if !committed {
			for i := len(created) - 1; i >= 0; i-- {
				_ = os.RemoveAll(created[i])
			}
		}
	}()
	missing := []string{}
	for path := l.StateDir; ; path = filepath.Dir(path) {
		if _, e = os.Lstat(path); e == nil {
			break
		}
		if !os.IsNotExist(e) {
			return result, e
		}
		missing = append(missing, path)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		path := missing[i]
		if e = os.Mkdir(path, 0710); e != nil {
			return result, e
		}
		created = append(created, path)
		if e = os.Chown(path, int(uid), int(gid)); e != nil {
			return result, e
		}
		if e = os.Chmod(path, 0710); e != nil {
			return result, e
		}
	}
	if e = checkPath(l.StateDir, int(uid), int(gid), 0710, true); e != nil {
		return result, e
	}
	if e = os.Mkdir(l.PrivateDir, 0700); e != nil {
		return result, e
	}
	created = append(created, l.PrivateDir)
	if e = os.Chmod(l.PrivateDir, 0700); e != nil {
		return result, e
	}
	lock, e := acquireLock(l.LockPath, d.ProcessIdentity)
	if e != nil {
		return result, e
	}
	defer lock.Close()
	file, e := os.OpenFile(l.DatabasePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return result, e
	}
	if e = file.Close(); e != nil {
		return result, e
	}
	db, e := store.Open(ctx, l.DatabasePath)
	if e != nil {
		return result, e
	}
	closed := false
	defer func() {
		if !closed {
			_ = db.Close()
		}
	}()
	if e = os.Chmod(l.DatabasePath, 0600); e != nil {
		return result, e
	}
	if e = db.InitializeRoom(ctx, store.RoomSeed{ID: result.RoomID, DisplayName: result.RoomName, HostID: result.HostID, ProjectRoot: root, ExecutionOwnerUID: uid, Members: ms}); e != nil {
		return result, e
	}
	if e = db.SaveRuntimeCheckpoint(ctx, result.RoomID, cp); e != nil {
		return result, e
	}
	e = db.Close()
	closed = true
	if e != nil {
		return result, e
	}
	if e = config.WriteAtomic(l.ConfigPath, result); e != nil {
		return result, e
	}
	durableDirs := []string{l.StateDir, filepath.Dir(l.StateDir)}
	for _, path := range created {
		durableDirs = append(durableDirs, filepath.Dir(path))
	}
	for _, path := range durableDirs {
		dir, syncErr := os.Open(path)
		if syncErr != nil {
			return result, syncErr
		}
		syncErr = dir.Sync()
		closeErr := dir.Close()
		if e = errors.Join(syncErr, closeErr); e != nil {
			return result, e
		}
	}
	committed = true
	return result, nil
}
func ValidateServe(ctx context.Context, state string) (config.RuntimeConfig, *Lock, error) {
	return ValidateServeWithDependencies(ctx, DefaultDependencies(), state)
}
func ValidateServeWithDependencies(ctx context.Context, d Dependencies, state string) (config.RuntimeConfig, *Lock, error) {
	c := config.RuntimeConfig{}
	l, e := config.NewLayout(state, "")
	if e != nil {
		return c, nil, e
	}
	c, e = config.Read(l.ConfigPath)
	if e != nil {
		return c, nil, e
	}
	if _, _, e = resolveOwner(d, c.ExecutionOwnerName); e != nil {
		return c, nil, e
	}
	if int(c.ExecutionOwnerUID) != d.EUID() {
		return c, nil, ErrExecutionOwnerMismatch
	}
	if e = c.Validate(l); e != nil {
		return c, nil, e
	}
	l, e = config.NewLayout(state, c.ProjectRoot)
	if e != nil {
		return c, nil, e
	}
	for _, p := range []struct {
		path string
		mode os.FileMode
		dir  bool
		gid  int
	}{{l.StateDir, 0710, true, int(c.SharedGroupGID)}, {l.PrivateDir, 0700, true, -1}, {l.ConfigPath, 0600, false, -1}, {l.DatabasePath, 0600, false, -1}} {
		if e = checkPath(p.path, int(c.ExecutionOwnerUID), p.gid, p.mode, p.dir); e != nil {
			return c, nil, e
		}
	}
	owner, _, e := resolveOwner(d, c.ExecutionOwnerName)
	if e != nil {
		return c, nil, e
	}
	names := []string{}
	for _, m := range c.Members {
		u, e := d.LookupUser(m.Name)
		if e != nil || u.Uid != strconv.FormatUint(uint64(m.UID), 10) || u.Username != m.Name {
			return c, nil, ErrSharedGroup
		}
		names = append(names, m.Name)
	}
	if _, e = members(d, names, owner, strconv.FormatUint(uint64(c.SharedGroupGID), 10)); e != nil {
		return c, nil, e
	}
	root, e := validateProject(ctx, c.ProjectRoot)
	if e != nil {
		return c, nil, e
	}
	if root != c.ProjectRoot {
		return c, nil, ErrProjectNotGitRoot
	}
	lock, e := acquireLock(l.LockPath, d.ProcessIdentity)
	return c, lock, e
}
func pathError(path string) error { return fmt.Errorf("unsafe owner, type or permissions: %s", path) }
