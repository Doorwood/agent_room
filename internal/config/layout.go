package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var ErrUnsafeLayout = errors.New("unsafe state directory layout")

type Layout struct{ StateDir, PrivateDir, ConfigPath, DatabasePath, SocketPath, LockPath string }

func NewLayout(state, project string) (Layout, error) {
	if !filepath.IsAbs(state) || filepath.Clean(state) != state || state == string(filepath.Separator) {
		return Layout{}, ErrUnsafeLayout
	}
	if project != "" && (state == project || strings.HasPrefix(state, project+string(filepath.Separator))) {
		return Layout{}, ErrUnsafeLayout
	}
	ancestor := state
	for {
		_, e := os.Lstat(ancestor)
		if e == nil {
			break
		}
		if !os.IsNotExist(e) {
			return Layout{}, e
		}
		next := filepath.Dir(ancestor)
		if next == ancestor {
			return Layout{}, ErrUnsafeLayout
		}
		ancestor = next
	}
	canonical, e := filepath.EvalSymlinks(ancestor)
	if e != nil {
		return Layout{}, e
	}
	if canonical != ancestor {
		return Layout{}, ErrUnsafeLayout
	}
	private := filepath.Join(state, "private")
	return Layout{state, private, filepath.Join(private, "config.json"), filepath.Join(private, "room.db"), filepath.Join(state, "room.sock"), filepath.Join(private, "daemon.lock")}, nil
}
