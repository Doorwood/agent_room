package admin

import (
	"agent_romm/internal/codex"
	"agent_romm/internal/config"
	"agent_romm/internal/room"
	"agent_romm/internal/store"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestInitRejectsNonOwnerWithoutPartialState(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	d := DefaultDependencies()
	d.EUID = func() int { return -1 }
	_, e := InitWithDependencies(context.Background(), d, InitOptions{ExecutionOwner: os.Getenv("USER"), StateDir: state, FullOwnerAccess: true})
	if !errors.Is(e, ErrExecutionOwnerMismatch) {
		t.Fatal(e)
	}
	if _, e := os.Stat(state); !os.IsNotExist(e) {
		t.Fatal("partial state")
	}
}

func fixture(t *testing.T) (Dependencies, InitOptions) {
	t.Helper()
	parent, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(parent, "repo")
	if e = exec.Command("git", "init", root).Run(); e != nil {
		t.Fatal(e)
	}
	uid := os.Geteuid()
	if uid == 0 {
		t.Skip("room owners must not be root")
	}
	gid := os.Getegid()
	u := &user.User{Uid: strconv.Itoa(uid), Gid: strconv.Itoa(gid), Username: "owner"}
	d := DefaultDependencies()
	d.LookupUser = func(n string) (*user.User, error) {
		if n != "owner" && n != "alias" {
			return nil, errors.New("no user")
		}
		return u, nil
	}
	d.LookupGroup = func(n string) (*user.Group, error) {
		if n != "shared" {
			return nil, errors.New("no group")
		}
		return &user.Group{Name: n, Gid: u.Gid}, nil
	}
	d.GroupIDs = func(*user.User) ([]string, error) { return []string{u.Gid}, nil }
	d.ProcessIdentity = func(pid int) (string, error) {
		if pid == os.Getpid() {
			return "test-start", nil
		}
		return "", os.ErrNotExist
	}
	d.Probe = func(context.Context, string, string) (room.RuntimeCheckpoint, error) {
		return room.RuntimeCheckpoint{State: room.RuntimeProcessStopped, Generation: "probe", ProcessStart: "probe-start", CodexVersion: codex.SupportedCLIOutput, SchemaSHA256: codex.SupportedSchemaSHA256}, nil
	}
	return d, InitOptions{Project: root, StateDir: filepath.Join(parent, "state"), Room: "team", ExecutionOwner: "owner", Members: []string{"alias"}, SharedGroup: "shared", FullOwnerAccess: true}
}
func TestInitRoundTripAndServeLock(t *testing.T) {
	d, o := fixture(t)
	c, e := InitWithDependencies(context.Background(), d, o)
	if e != nil {
		t.Fatal(e)
	}
	if c.RoomName != "team" || c.RoomID == room.RoomID(c.HostID) || len(c.RoomID) != 32 || len(c.Members) != 1 || c.Members[0].Name != "owner" {
		t.Fatalf("bad config %+v", c)
	}
	got, lock, e := ValidateServeWithDependencies(context.Background(), d, o.StateDir)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	if got.RoomID != c.RoomID {
		t.Fatal("config mismatch")
	}
	if _, second, e := ValidateServeWithDependencies(context.Background(), d, o.StateDir); !errors.Is(e, ErrDaemonLocked) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("second lock: %v", e)
	}
	db, e := store.Open(context.Background(), c.DatabasePath)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	image, e := db.LoadRecoveryImage(context.Background(), c.RoomID)
	if e != nil || image.Checkpoint.State != room.RuntimeProcessStopped {
		t.Fatalf("%+v %v", image, e)
	}
}

func TestInitCreatesMissingStateParents(t *testing.T) {
	d, o := fixture(t)
	o.StateDir = filepath.Join(filepath.Dir(o.StateDir), "parent", "nested", "state")
	if _, e := InitWithDependencies(context.Background(), d, o); e != nil {
		t.Fatal(e)
	}
	_, lock, e := ValidateServeWithDependencies(context.Background(), d, o.StateDir)
	if e != nil {
		t.Fatal(e)
	}
	lock.Close()
}
func TestInitValidationsHaveNoProbeOrPartialState(t *testing.T) {
	for _, kind := range []string{"relative-project", "subdir", "missing-project", "file-project", "non-git", "relative-state", "nested-state", "symlink-state", "missing-group", "not-member", "no-ack", "random-failure", "existing-private"} {
		t.Run(kind, func(t *testing.T) {
			d, o := fixture(t)
			switch kind {
			case "relative-project":
				o.Project = "relative"
			case "subdir":
				o.Project = filepath.Join(o.Project, "sub")
				os.Mkdir(o.Project, 0700)
			case "missing-project":
				o.Project += "missing"
			case "file-project":
				o.Project = filepath.Join(filepath.Dir(o.Project), "file")
				os.WriteFile(o.Project, nil, 0600)
			case "non-git":
				o.Project = filepath.Dir(o.Project)
			case "relative-state":
				o.StateDir = "relative"
			case "nested-state":
				o.StateDir = filepath.Join(o.Project, "state")
			case "symlink-state":
				link := filepath.Join(filepath.Dir(o.Project), "link")
				os.Symlink(o.Project, link)
				o.StateDir = filepath.Join(link, "state")
			case "missing-group":
				o.SharedGroup = ""
			case "not-member":
				d.GroupIDs = func(*user.User) ([]string, error) { return nil, nil }
			case "no-ack":
				o.FullOwnerAccess = false
			case "random-failure":
				d.Random = bytes.NewReader(nil)
			case "existing-private":
				os.Mkdir(o.StateDir, 0710)
				os.Chmod(o.StateDir, 0710)
				os.Mkdir(filepath.Join(o.StateDir, "private"), 0700)
			}
			probed := false
			d.Probe = func(context.Context, string, string) (room.RuntimeCheckpoint, error) {
				probed = true
				return room.RuntimeCheckpoint{}, errors.New("must not probe")
			}
			if _, e := InitWithDependencies(context.Background(), d, o); e == nil {
				t.Fatal("accepted invalid input")
			}
			if probed {
				t.Fatal("probe before validation")
			}
			if kind != "existing-private" {
				if _, e := os.Stat(o.StateDir); !os.IsNotExist(e) {
					t.Fatalf("partial state: %v", e)
				}
			}
		})
	}
}
func TestInitRollbackPreservesPreexistingDirectory(t *testing.T) {
	d, o := fixture(t)
	if e := os.Mkdir(o.StateDir, 0710); e != nil {
		t.Fatal(e)
	}
	os.Chmod(o.StateDir, 0710)
	sentinel := filepath.Join(o.StateDir, "keep")
	os.WriteFile(sentinel, []byte("keep"), 0600)
	probe := d.Probe
	d.Probe = func(ctx context.Context, a, b string) (room.RuntimeCheckpoint, error) {
		cp, e := probe(ctx, a, b)
		cp.Generation = ""
		return cp, e
	}
	if _, e := InitWithDependencies(context.Background(), d, o); e == nil {
		t.Fatal("bad checkpoint accepted")
	}
	if b, e := os.ReadFile(sentinel); e != nil || string(b) != "keep" {
		t.Fatal("removed existing data")
	}
	if _, e := os.Stat(filepath.Join(o.StateDir, "private")); !os.IsNotExist(e) {
		t.Fatalf("private not rolled back: %v", e)
	}
}
func TestServeRejectsConfigAndProjectChangesBeforeLock(t *testing.T) {
	for _, kind := range []string{"ack", "root", "owner", "member", "db-symlink"} {
		t.Run(kind, func(t *testing.T) {
			d, o := fixture(t)
			c, e := InitWithDependencies(context.Background(), d, o)
			if e != nil {
				t.Fatal(e)
			}
			l, _ := config.NewLayout(o.StateDir, c.ProjectRoot)
			switch kind {
			case "ack":
				c.FullOwnerAccess = false
			case "owner":
				c.ExecutionOwnerUID++
			case "member":
				c.Members[0].UID++
			case "root":
				os.Rename(c.ProjectRoot, c.ProjectRoot+"old")
				os.Mkdir(c.ProjectRoot, 0700)
			case "db-symlink":
				os.Rename(c.DatabasePath, c.DatabasePath+"old")
				os.Symlink(c.DatabasePath+"old", c.DatabasePath)
			}
			if kind == "ack" || kind == "owner" || kind == "member" {
				os.Remove(l.ConfigPath)
				if e = config.WriteAtomic(l.ConfigPath, c); e != nil {
					t.Fatal(e)
				}
			}
			if _, lock, e := ValidateServeWithDependencies(context.Background(), d, o.StateDir); e == nil {
				lock.Close()
				t.Fatal("accepted invalid state")
			}
			b, e := os.ReadFile(l.LockPath)
			if e != nil || strings.TrimSpace(string(b)) != "" {
				t.Fatal("lock mutated before validation")
			}
		})
	}
}
