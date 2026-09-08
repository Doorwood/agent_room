//go:build linux || darwin

package admin

import (
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"sync"
)

var ErrDaemonLocked = errors.New("daemon lock holder is live or cannot be proven absent")

type Lock struct {
	mu   sync.Mutex
	file *os.File
}
type lockRecord struct {
	PID   int    `json:"pid"`
	Start string `json:"processStart"`
}

func checkPath(path string, uid, gid int, mode os.FileMode, dir bool) error {
	info, e := os.Lstat(path)
	if e != nil {
		return e
	}
	var stat unix.Stat_t
	if e = unix.Lstat(path, &stat); e != nil {
		return e
	}
	if int(stat.Uid) != uid || (gid >= 0 && int(stat.Gid) != gid) || info.Mode().Perm() != mode || info.IsDir() != dir || info.Mode()&os.ModeSymlink != 0 || (!dir && !info.Mode().IsRegular()) {
		return pathError(path)
	}
	return nil
}
func AcquireLock(path string) (*Lock, error) { return acquireLock(path, processIdentity) }
func acquireLock(path string, identity func(int) (string, error)) (*Lock, error) {
	fd, e := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	success := false
	defer func() {
		if !success {
			_ = f.Close()
		}
	}()
	var st unix.Stat_t
	if e = unix.Fstat(fd, &st); e != nil {
		return nil, e
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 || int(st.Uid) != os.Geteuid() || st.Nlink != 1 {
		return nil, ErrDaemonLocked
	}
	if e = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); e != nil {
		return nil, ErrDaemonLocked
	}
	data, e := io.ReadAll(io.LimitReader(f, 4097))
	if e != nil {
		return nil, e
	}
	if len(data) > 0 {
		var old lockRecord
		if len(data) > 4096 || json.Unmarshal(data, &old) != nil || old.PID <= 0 || old.Start == "" {
			return nil, ErrDaemonLocked
		}
		_, e := identity(old.PID)
		if e == nil {
			return nil, ErrDaemonLocked
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return nil, ErrDaemonLocked
		}
	}
	start, e := identity(os.Getpid())
	if e != nil || start == "" {
		return nil, ErrDaemonLocked
	}
	if e = f.Truncate(0); e != nil {
		return nil, e
	}
	if _, e = f.Seek(0, 0); e != nil {
		return nil, e
	}
	if e = json.NewEncoder(f).Encode(lockRecord{os.Getpid(), start}); e != nil {
		return nil, e
	}
	if e = f.Sync(); e != nil {
		return nil, e
	}
	success = true
	return &Lock{file: f}, nil
}

// Close clears the identity while still holding the kernel lock. Never unlink a
// lock file: competing open descriptors must continue referring to one inode.
func (l *Lock) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	e := l.file.Truncate(0)
	if e == nil {
		e = l.file.Sync()
	}
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(e, closeErr)
}
