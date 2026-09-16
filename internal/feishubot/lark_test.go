package feishubot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fakeCLI(t *testing.T, ready bool) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
case "$3" in
whoami) printf '%s\n' '{"appId":"cli_1234567890","identity":"bot","available":true}' ;;
event)
`
	if ready {
		script += `printf '%s\n' '[event] ready event_key=im.message.receive_v1' >&2
`
	}
	script += `cat "$TEST_BOT_EVENT_FILE"
;;
im) printf '%s\n' '{"ok":true,"identity":"bot"}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "lark-cli"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "event.json")
	raw, _ := json.Marshal(event("/room 状态"))
	os.WriteFile(file, append(raw, '\n'), 0600)
	t.Setenv("TEST_BOT_EVENT_FILE", file)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}
func TestLarkConsumerWaitsForReadyAndChecksProfile(t *testing.T) {
	for _, ready := range []bool{true, false} {
		t.Run(map[bool]string{true: "ready", false: "not-ready"}[ready], func(t *testing.T) {
			fakeCLI(t, ready)
			l := Lark{Config: testConfig()}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			count := 0
			err := l.consumeOnce(ctx, func(context.Context, Event) error { count++; return nil }, func(string) {})
			if ready && (err != nil || count != 1) {
				t.Fatal(count, err)
			}
			if !ready && (err == nil || count != 0) {
				t.Fatal("consumed without ready", count, err)
			}
			l.Config.AppID = "cli_different123"
			if l.Check(ctx) == nil {
				t.Fatal("wrong app accepted")
			}
		})
	}
}
func TestLarkSendExplicitBotIdentity(t *testing.T) {
	fakeCLI(t, true)
	l := Lark{Config: testConfig()}
	if err := l.Send(context.Background(), "ou_alice123456", "<img src=secret> & $(echo nope)", "123"); err != nil {
		t.Fatal(err)
	}
	if l.Send(context.Background(), "oc_group123456", "private", "123") == nil {
		t.Fatal("group destination accepted")
	}
}
