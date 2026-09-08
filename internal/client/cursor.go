package client

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type Cursor struct {
	Target         string `json:"target"`
	RoomID         string `json:"roomId"`
	LastAppliedSeq uint64 `json:"lastAppliedSeq"`
}
type CursorStore interface {
	Load(string) (Cursor, error)
	Save(Cursor) error
}
type FileCursors struct{ Directory string }

func (s FileCursors) path(target string) (string, error) {
	dir := s.Directory
	if dir == "" {
		root, e := os.UserConfigDir()
		if e != nil {
			return "", e
		}
		dir = filepath.Join(root, "agent_romm", "cursors")
	}
	hash := sha256.Sum256([]byte(target))
	return filepath.Join(dir, hex.EncodeToString(hash[:])+".json"), nil
}
func (s FileCursors) Load(target string) (Cursor, error) {
	p, e := s.path(target)
	if e != nil {
		return Cursor{}, e
	}
	b, e := os.ReadFile(p)
	if errors.Is(e, os.ErrNotExist) {
		return Cursor{Target: target}, nil
	}
	if e != nil {
		return Cursor{}, e
	}
	var c Cursor
	if e = json.Unmarshal(b, &c); e != nil {
		return c, e
	}
	if c.Target != target || c.RoomID == "" {
		return Cursor{}, errors.New("cursor identity mismatch")
	}
	return c, nil
}
func (s FileCursors) Save(c Cursor) error {
	p, e := s.path(c.Target)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return e
	}
	b, e := json.Marshal(c)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(p), ".cursor-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if e = f.Chmod(0600); e != nil {
		return e
	}
	if _, e = f.Write(b); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), p); e != nil {
		return e
	}
	dir, e := os.Open(filepath.Dir(p))
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
