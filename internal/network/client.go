package network

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent_romm/internal/client"
)

type Credential struct {
	Address string `json:"address"`
	Session string `json:"session"`
	Name    string `json:"name"`
	Token   string `json:"token"`
}

func Address(value string) (string, error) {
	if strings.ContainsAny(value, "/ \t\r\n") || value == "" {
		return "", errors.New("host must be an IP or hostname, optionally with :port")
	}
	if net.ParseIP(value) != nil {
		return net.JoinHostPort(value, DefaultPort), nil
	}
	if !strings.Contains(value, ":") {
		return net.JoinHostPort(value, DefaultPort), nil
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || host == "" || port == "" {
		return "", errors.New("invalid host:port")
	}
	return value, nil
}
func CredentialFor(dir, address, session, name string) (Credential, error) {
	c := Credential{Address: address, Session: session, Name: name}
	if _, _, err := ParseSession(session); err != nil {
		return c, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return c, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return c, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return c, errors.New("client credential directory must be private (0700)")
	}
	sum := sha256.Sum256([]byte(address + "\n" + session + "\n" + name))
	path := filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
	if _, err = os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		var b [32]byte
		if _, err = rand.Read(b[:]); err != nil {
			return c, err
		}
		c.Token = hex.EncodeToString(b[:])
		data, e := json.Marshal(c)
		if e != nil {
			return c, e
		}
		f, e := os.CreateTemp(dir, ".credential-*")
		if e != nil {
			return c, e
		}
		defer os.Remove(f.Name())
		defer f.Close()
		if _, e = f.Write(data); e == nil {
			e = f.Sync()
		}
		if e != nil {
			return c, e
		}
		if e = f.Close(); e != nil {
			return c, e
		}
		if e = os.Link(f.Name(), path); e != nil && !errors.Is(e, os.ErrExist) {
			return c, e
		}
	} else if err != nil {
		return c, err
	}
	info, err = os.Lstat(path)
	if err != nil {
		return c, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 8192 {
		return c, errors.New("invalid client credential file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	var saved Credential
	if err = json.Unmarshal(data, &saved); err != nil {
		return c, err
	}
	b, e := hex.DecodeString(saved.Token)
	if e != nil || len(b) != 32 || saved.Address != address || saved.Session != session || saved.Name != name {
		return c, errors.New("client credential identity mismatch")
	}
	return saved, nil
}

type Launcher struct{ Credential Credential }

func (l Launcher) dial(ctx context.Context) (net.Conn, Reply, error) {
	_, pin, err := ParseSession(l.Credential.Session)
	if err != nil {
		return nil, Reply{}, err
	}
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: PinnedTLS(pin)}
	c, err := d.DialContext(ctx, "tcp", l.Credential.Address)
	if err != nil {
		return nil, Reply{}, err
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if err = writeJSON(c, Hello{Session: l.Credential.Session, Token: l.Credential.Token, Name: l.Credential.Name}); err != nil {
		c.Close()
		return nil, Reply{}, err
	}
	var r Reply
	if err = readJSON(c, &r); err != nil {
		c.Close()
		return nil, r, err
	}
	_ = c.SetDeadline(time.Time{})
	return c, r, nil
}

func (l Launcher) WaitApproval(ctx context.Context, out io.Writer) error {
	printed := false
	for {
		c, r, err := l.dial(ctx)
		if err != nil {
			return err
		}
		c.Close()
		switch r.State {
		case "approved":
			fmt.Fprintln(out, "Approved. Connecting to shared session...")
			return nil
		case "pending":
			if !printed {
				fmt.Fprintf(out, "Join request: %s\nWaiting for host approval. Host: agent_room approve %s\n", r.RequestID, r.RequestID)
				printed = true
			}
		default:
			return fmt.Errorf("join %s; ask the host to review your request", r.State)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
func (l Launcher) Start(ctx context.Context, _ string) (*client.Connection, error) {
	c, r, err := l.dial(ctx)
	if err != nil {
		return nil, err
	}
	if r.State != "approved" {
		c.Close()
		return nil, client.Stop(fmt.Errorf("membership %s", r.State))
	}
	return &client.Connection{Reader: c, Writer: c}, nil
}
