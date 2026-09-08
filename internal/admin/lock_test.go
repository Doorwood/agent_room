package admin

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLockFailsClosedAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	identity := func(int) (string, error) { return "start", nil }
	l, e := acquireLock(path, identity)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = acquireLock(path, identity); !errors.Is(e, ErrDaemonLocked) {
		t.Fatal(e)
	}
	if e = l.Close(); e != nil {
		t.Fatal(e)
	}
	l, e = acquireLock(path, identity)
	if e != nil {
		t.Fatal(e)
	}
	l.Close()
	for _, body := range []string{`{"pid":123,"processStart":"start"}`, `{"pid":123,"processStart":"other"}`, `broken`} {
		if e = os.WriteFile(path, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e = acquireLock(path, identity); !errors.Is(e, ErrDaemonLocked) {
			t.Fatal(e)
		}
	}
	os.WriteFile(path, []byte(`{"pid":123,"processStart":"old"}`), 0600)
	l, e = acquireLock(path, func(pid int) (string, error) {
		if pid == os.Getpid() {
			return "start", nil
		}
		return "", os.ErrNotExist
	})
	if e != nil {
		t.Fatal(e)
	}
	l.Close()
}
