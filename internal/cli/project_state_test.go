package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"agent_romm/internal/config"
)

func TestProjectStateDoesNotReuseAnotherProjectsLegacySession(t *testing.T) {
	home := t.TempDir()
	a := filepath.Join(home, "project-a")
	b := filepath.Join(home, "project-b")
	legacy := filepath.Join(home, ".local", "share", "agent_room", "host")
	if err := os.MkdirAll(filepath.Join(legacy, "private"), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(config.RuntimeConfig{ProjectRoot: a})
	if err := os.WriteFile(filepath.Join(legacy, "private", "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	old, err := projectState(home, a)
	if err != nil || old != legacy {
		t.Fatal("existing session not preserved", old, err)
	}
	other, err := projectState(home, b)
	if err != nil || other == legacy {
		t.Fatal("another project reused legacy state", other, err)
	}
	again, err := projectState(home, b)
	if err != nil || again != other {
		t.Fatal("project state is not stable", again, err)
	}
}

func TestProjectResolutionMatchesHostAndManagementAndAliases(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "repo")
	if err := os.MkdirAll(filepath.Join(project, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", project).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(project, alias); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var first string
	for _, path := range []string{project, filepath.Join(project, "sub"), alias} {
		root, err := canonicalProject(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		state, err := projectState(home, root)
		if err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = state
		} else if first != state {
			t.Fatal("same Git root used different states", first, state)
		}
	}
}

func TestAutomaticPortFallbackPreservesExplicitRequests(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	var available net.Listener
	start := func(address string) error { var err error; available, err = net.Listen("tcp", address); return err }
	if err = startWithPortFallback(occupied.Addr().String(), false, start); !errors.Is(err, syscall.EADDRINUSE) {
		if available != nil {
			available.Close()
		}
		t.Fatal("explicit port silently changed", err)
	}
	if err = startWithPortFallback(occupied.Addr().String(), true, start); err != nil {
		t.Fatal(err)
	}
	defer available.Close()
	if available.Addr().String() == occupied.Addr().String() {
		t.Fatal("fallback did not allocate another port")
	}
}

func TestAutomaticPortFallbackDoesNotHideOtherFailures(t *testing.T) {
	count := 0
	err := startWithPortFallback("127.0.0.1:7443", true, func(string) error { count++; return syscall.EACCES })
	if !errors.Is(err, syscall.EACCES) || count != 1 {
		t.Fatal(count, err)
	}
}

func TestHostEndpointPersistsSelectedPort(t *testing.T) {
	state := t.TempDir()
	if err := os.Mkdir(filepath.Join(state, "private"), 0700); err != nil {
		t.Fatal(err)
	}
	e := hostEndpoint{Listen: "0.0.0.0:45678", Address: "10.1.2.3:45678"}
	if err := saveHostEndpoint(state, e); err != nil {
		t.Fatal(err)
	}
	got, err := loadHostEndpoint(state)
	if err != nil || got != e {
		t.Fatal(got, err)
	}
	e.Listen = "127.0.0.1:43210"
	e.Address = e.Listen
	if err := saveHostEndpoint(state, e); err != nil {
		t.Fatal(err)
	}
	got, err = loadHostEndpoint(state)
	if err != nil || got != e {
		t.Fatal("changed endpoint not persisted", got, err)
	}
}

func TestHostEndpointAcceptsWildcardListenButRequiresJoinHost(t *testing.T) {
	e := hostEndpoint{Listen: ":45678", Address: "10.1.2.3:45678"}
	if err := e.validate(); err != nil {
		t.Fatal("valid wildcard listener rejected", err)
	}
	e.Address = ":45678"
	if err := e.validate(); err == nil {
		t.Fatal("unusable participant address accepted")
	}
}
