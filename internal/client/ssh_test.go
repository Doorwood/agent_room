package client

import (
	"context"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestSSHExactArgv(t *testing.T) {
	cmd := sshCommand(context.Background(), "user@host")
	if !reflect.DeepEqual(cmd.Args, []string{"ssh", "-T", "user@host", "agent_romm bridge"}) {
		t.Fatal(cmd.Args)
	}
}

func TestSSHWaitPreservesUnreadStdout(t *testing.T) {
	if os.Getenv("AGENT_ROMM_CLIENT_TEST_CHILD") == "1" {
		_, _ = io.WriteString(os.Stdout, strings.Repeat("p", 4096))
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSSHWaitPreservesUnreadStdout$")
	cmd.Env = append(os.Environ(), "AGENT_ROMM_CLIENT_TEST_CHILD=1")
	c, err := startCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.Wait(); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(c.Reader)
	if err != nil || string(b) != strings.Repeat("p", 4096) {
		t.Fatal(len(b), err)
	}
}

func TestSSHRejectsOptionInjection(t *testing.T) {
	for _, target := range []string{"", " ", "-oProxyCommand=evil", "host\nother", "host\x00", "host\u202e"} {
		if _, e := (SSHLauncher{Stderr: io.Discard}).Start(context.Background(), target); e == nil {
			t.Fatal(target)
		}
	}
	if e := ValidateTarget("alice@host"); e != nil {
		t.Fatal(e)
	}
}
