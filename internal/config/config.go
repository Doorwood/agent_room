package config

import (
	"agent_romm/internal/room"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type RuntimeConfig struct {
	SchemaVersion      int           `json:"schemaVersion"`
	RoomID             room.RoomID   `json:"roomId"`
	RoomName           string        `json:"roomName"`
	HostID             string        `json:"hostId"`
	ProjectRoot        string        `json:"projectRoot"`
	ExecutionOwnerUID  room.UID      `json:"executionOwnerUid"`
	ExecutionOwnerName string        `json:"executionOwnerName"`
	FullOwnerAccess    bool          `json:"fullOwnerAccess"`
	SharedGroupGID     uint32        `json:"sharedGroupGid"`
	SocketPath         string        `json:"socketPath"`
	DatabasePath       string        `json:"databasePath"`
	Members            []room.Member `json:"members"`
	CodexVersion       string        `json:"codexVersion"`
	SchemaSHA256       string        `json:"schemaSha256"`
}

var ErrInvalidConfig = errors.New("invalid runtime configuration")

func (c RuntimeConfig) Validate(l Layout) error {
	if c.SchemaVersion != 1 || !room.ValidConnectionID(room.ConnectionID(c.RoomID)) || !room.ValidConnectionID(room.ConnectionID(c.HostID)) || strings.TrimSpace(c.RoomName) == "" || !filepath.IsAbs(c.ProjectRoot) || filepath.Clean(c.ProjectRoot) != c.ProjectRoot || !c.FullOwnerAccess || c.ExecutionOwnerUID == 0 || c.ExecutionOwnerName == "" || c.SocketPath != l.SocketPath || c.DatabasePath != l.DatabasePath || c.CodexVersion == "" || len(c.SchemaSHA256) != 64 {
		return ErrInvalidConfig
	}
	seen := map[room.UID]bool{}
	names := map[string]bool{}
	if _, err := hex.DecodeString(c.SchemaSHA256); err != nil || strings.ToLower(c.SchemaSHA256) != c.SchemaSHA256 {
		return ErrInvalidConfig
	}
	owner := false
	for _, m := range c.Members {
		if m.UID == 0 || strings.TrimSpace(m.Name) == "" || m.Name != strings.TrimSpace(m.Name) || seen[m.UID] || names[m.Name] {
			return ErrInvalidConfig
		}
		seen[m.UID] = true
		names[m.Name] = true
		if m.UID == c.ExecutionOwnerUID && m.Name == c.ExecutionOwnerName {
			owner = true
		}
	}
	if !owner {
		return ErrInvalidConfig
	}
	return nil
}
func Read(path string) (RuntimeConfig, error) {
	var c RuntimeConfig
	f, e := os.Open(path)
	if e != nil {
		return c, e
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	if e = dec.Decode(&c); e != nil {
		return c, e
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return c, ErrInvalidConfig
	}
	return c, nil
}

// WriteAtomic publishes a new immutable config. Callers hold the private layout lock.
func WriteAtomic(path string, c RuntimeConfig) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".config-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if e = f.Chmod(0600); e != nil {
		return e
	}
	if e = json.NewEncoder(f).Encode(c); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if _, e = os.Lstat(path); !os.IsNotExist(e) {
		if e == nil {
			return os.ErrExist
		}
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	if e = d.Sync(); e != nil {
		return e
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	return d.Sync()
}
