package cli

import (
	"agent_romm/internal/admin"
	"bytes"
	"context"
	"errors"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRootHelpListsOnlySupportedCommands(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if code := Run(context.Background(), []string{"help"}, &out, &diagnostics, ProductionDependencies()); code != 0 {
		t.Fatalf("%d: %s", code, diagnostics.String())
	}
	for _, name := range []string{"init", "serve", "connect", "bridge", "repair-thread"} {
		if !strings.Contains(out.String(), name) {
			t.Fatal(name)
		}
	}
}
func TestInitFlags(t *testing.T) {
	for _, args := range [][]string{{}, {"--members", "alice,bob"}, {"--members", "alice,alice,bob"}, {"--members", "alice,bob,carol", "--state-dir", "relative"}} {
		assertUsage(t, append([]string{"init"}, args...))
	}
	valid := []string{"init", "--state-dir", "/absolute-state", "--project", "/absolute-project", "--room", "demo", "--execution-owner", "owner", "--shared-group", "team", "--members", "alice,bob,carol", "--full-owner-access"}
	for _, flag := range []string{"--state-dir", "--project", "--room", "--execution-owner", "--shared-group", "--members", "--full-owner-access"} {
		var args []string
		for i := 0; i < len(valid); i++ {
			if valid[i] == flag {
				if flag != "--full-owner-access" {
					i++
				}
				continue
			}
			args = append(args, valid[i])
		}
		assertUsage(t, args)
	}
	for _, same := range []bool{false, true} {
		ownerCalls := 0
		d := Dependencies{Admin: admin.Dependencies{LookupUser: func(name string) (*user.User, error) {
			if name == "owner" {
				ownerCalls++
				return nil, errors.New("stop before filesystem")
			}
			index := strings.Index("alice,bob,carol", name) + 501
			if same {
				index = 501
			}
			return &user.User{Uid: strconv.Itoa(index)}, nil
		}}}
		var out, diag bytes.Buffer
		if code := Run(context.Background(), valid, &out, &diag, d); code != 1 {
			t.Fatalf("code=%d", code)
		}
		if (ownerCalls == 0) != same {
			t.Fatalf("alias rejection=%v ownerCalls=%d", same, ownerCalls)
		}
	}
}
func TestRepairFlags(t *testing.T) {
	for _, args := range [][]string{{}, {"--use", "thread-1", "--create"}, {"--create", "extra"}} {
		assertUsage(t, append([]string{"repair-thread"}, args...))
	}
	for _, choice := range [][]string{{"--use", "thread-1"}, {"--create"}} {
		var out, diag bytes.Buffer
		args := append([]string{"repair-thread", "--state-dir", filepath.Join(t.TempDir(), "absent")}, choice...)
		if code := Run(context.Background(), args, &out, &diag, Dependencies{}); code != 1 {
			t.Fatalf("valid repair parser rejected %v: %d", args, code)
		}
	}
}
func TestRejectBypassesBeforeDependencies(t *testing.T) {
	for _, command := range []string{"", "init", "serve", "connect", "bridge", "repair-thread"} {
		for _, flag := range []string{"--as-user", "--fake", "--local"} {
			args := []string{flag}
			if command != "" {
				args = append([]string{command}, args...)
			}
			assertUsage(t, args)
		}
	}
	for _, args := range [][]string{{"connect"}, {"connect", "one", "two"}, {"connect", "host", "--fake"}, {"bridge", "extra"}} {
		assertUsage(t, args)
	}
}
func assertUsage(t *testing.T, args []string) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	if code := Run(context.Background(), args, &out, &diagnostics, Dependencies{}); code != 2 {
		t.Fatalf("%v: code=%d output=%s", args, code, diagnostics.String())
	}
}
