package cli

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInstalledCodexResolvesCurrentPATHOnEveryCall(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	for _, dir := range []string{first, second} {
		if err := os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", first)
	if got := installedCodex(); got != filepath.Join(first, "codex") {
		t.Fatalf("did not select current PATH executable: %q", got)
	}
	t.Setenv("PATH", second)
	if got := installedCodex(); got != filepath.Join(second, "codex") {
		t.Fatalf("cached previous executable: %q", got)
	}
	t.Setenv("PATH", t.TempDir())
	if got := installedCodex(); got != "" {
		t.Fatalf("missing PATH executable must not fall back to a private runtime: %q", got)
	}
}

func TestInstalledCodexFreezesRelativePATHBeforeChangingProject(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "tools"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "tools", "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("PATH", "tools")
	for _, setting := range []string{"execerrdot=0", "execerrdot=1"} {
		t.Setenv("GODEBUG", setting)
		if got := installedCodex(); got != path {
			t.Fatalf("relative executable could change when project cwd changes: %q", got)
		}
	}
}

func TestJoinFlagsWorkAfterPositionals(t *testing.T) {
	got := interspersed([]string{"10.1.2.3", "session", "--name", "me"})
	want := []string{"--name", "me", "--", "10.1.2.3", "session"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}
func TestAdvertisedAddressNeverWildcard(t *testing.T) {
	for _, bound := range []string{"0.0.0.0:7443", "[::]:7443"} {
		got := advertisedAddress(bound)
		host, port, err := net.SplitHostPort(got)
		if err != nil || port != "7443" || host == "" {
			t.Fatal(got, err)
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			t.Fatal(got)
		}
	}
	if got := advertisedAddress("127.0.0.1:7443"); got != "127.0.0.1:7443" {
		t.Fatal(got)
	}
}
