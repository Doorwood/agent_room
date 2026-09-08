package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"agent_romm/internal/config"
	"agent_romm/internal/gitview"
)

func canonicalProject(ctx context.Context, path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	root, err := (gitview.View{Root: absolute}).TopLevel(ctx)
	if err != nil {
		return "", fmt.Errorf("run this command in a Git project or use --state for an existing host: %w", err)
	}
	return filepath.EvalSymlinks(root)
}

// projectState keeps the original session for its project. New projects get
// independent, stable paths; the short hash also leaves room for Unix sockets.
func projectState(home, root string) (string, error) {
	base := filepath.Join(home, ".local", "share", "agent_room")
	legacy := filepath.Join(base, "host")
	cfg, err := config.Read(filepath.Join(legacy, "private", "config.json"))
	if err == nil && cfg.ProjectRoot == root {
		return legacy, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read previous host configuration: %w", err)
	}
	digest := sha256.Sum256([]byte(root))
	return filepath.Join(base, "projects", fmt.Sprintf("%x", digest[:8])), nil
}

func resolveState(ctx context.Context, explicit, project string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return "", err
	}
	root, err := canonicalProject(ctx, project)
	if err != nil {
		return "", err
	}
	return projectState(home, root)
}

// Only an automatically selected port may move on EADDRINUSE. Permission,
// storage, TLS and administration failures must retain their original errors.
func startWithPortFallback(address string, automatic bool, start func(string) error) error {
	err := start(address)
	if !automatic || !errors.Is(err, syscall.EADDRINUSE) {
		return err
	}
	host, _, splitErr := net.SplitHostPort(address)
	if splitErr != nil {
		return err
	}
	return start(net.JoinHostPort(host, "0"))
}

type hostEndpoint struct {
	Listen  string `json:"listen"`
	Address string `json:"address"`
}

func (e hostEndpoint) validate() error {
	for i, value := range []string{e.Listen, e.Address} {
		host, port, err := net.SplitHostPort(value)
		if err != nil || (i == 1 && host == "") {
			return errors.New("invalid saved host endpoint")
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("invalid saved host port")
		}
	}
	return nil
}
func loadHostEndpoint(state string) (hostEndpoint, error) {
	var endpoint hostEndpoint
	f, err := os.Open(filepath.Join(state, "private", "network.json"))
	if err != nil {
		return endpoint, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 4096))
	d.DisallowUnknownFields()
	if err = d.Decode(&endpoint); err != nil {
		return endpoint, err
	}
	if d.Decode(new(any)) != io.EOF {
		return endpoint, errors.New("invalid saved host endpoint")
	}
	return endpoint, endpoint.validate()
}
func saveHostEndpoint(state string, endpoint hostEndpoint) error {
	if err := endpoint.validate(); err != nil {
		return err
	}
	dir := filepath.Join(state, "private")
	f, err := os.CreateTemp(dir, ".network-address-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = json.NewEncoder(f).Encode(endpoint); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, "network.json")); err != nil {
		return err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
