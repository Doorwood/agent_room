package workgroup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeCommandsPreservePermissionChecks(t *testing.T) {
	for _, p := range []string{"cursor", "claude-code"} {
		for _, mode := range []string{"review", "work"} {
			args, e := NativeCommand(p, "/cli", mode)
			if e != nil {
				t.Fatal(e)
			}
			text := strings.Join(args, " ")
			if strings.Contains(text, "--force") || strings.Contains(text, "bypassPermissions") || strings.Contains(text, "--dangerously") {
				t.Fatal(args)
			}
			if mode == "review" && !(strings.Contains(text, "ask") || strings.Contains(text, "plan")) {
				t.Fatal(args)
			}
		}
	}
}
func TestNativeResultRequiresProvenSuccess(t *testing.T) {
	for _, data := range []string{`{"type":"result","subtype":"success","result":"ok","is_error":false}`, `{"type":"result","result":"ok"}`} {
		r, e := ParseNativeResult("worker", []byte(data))
		if e != nil || r.Text != "ok" {
			t.Fatal(r, e)
		}
	}
	for _, data := range []string{`{"type":"result","is_error":true,"result":"error"}`, `{"type":"result","result":"partial","permission_denials":[{}]}`, `{"result":"not a receipt"}`, `{"type":"result","subtype":"error_max_turns","result":"partial"}`} {
		if _, e := ParseNativeResult("worker", []byte(data)); e == nil {
			t.Fatal(data)
		}
	}
}
func TestNativeAdapterUsesStdinAndLocalIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cursor-agent")
	script := "#!/bin/sh\ncat > prompt.txt\nprintf '%s' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"read done\",\"is_error\":false}'\n"
	os.WriteFile(path, []byte(script), 0700)
	r, e := (NativeProvider{Provider: "cursor"}).Run(context.Background(), Member{ID: "cursor", Mode: "review", Command: []string{path}}, Assignment{StepID: "review", StepPrompt: "review only the completed build", ProjectRoot: dir, Prompt: "literal $(do-not-execute)", Prior: []Result{{AgentID: "codex", Text: "prior"}}})
	if e != nil || r.Text != "read done" {
		t.Fatal(r, e)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "prompt.txt"))
	if !strings.Contains(string(raw), "$(do-not-execute)") || !strings.Contains(string(raw), "prior") || !strings.Contains(string(raw), "review only the completed build") {
		t.Fatal("prompt lost")
	}
}

func TestLiveLocalCodexWorkspace(t *testing.T) {
	if os.Getenv("AGENT_ROOM_TEST_LIVE_LOCAL_CODEX") != "1" {
		t.Skip("requires local Codex login")
	}
	for _, mode := range []string{"review", "work"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			os.WriteFile(filepath.Join(root, "source.txt"), []byte("LOCAL_CODEX_PROOF"), 0600)
			prompt := "读取 source.txt 并回复其中的内容。不要修改文件。"
			if mode == "work" {
				prompt = "读取 source.txt，将其内容写入 result.txt，并回复已完成。只修改 result.txt。"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			result, err := (NativeProvider{Provider: "codex"}).Run(ctx, Member{ID: "local-codex", Mode: mode}, Assignment{ProjectRoot: root, Prompt: prompt})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "review" && !strings.Contains(result.Text, "LOCAL_CODEX_PROOF") {
				t.Fatal("missing file read")
			}
			if mode == "work" {
				data, err := os.ReadFile(filepath.Join(root, "result.txt"))
				if err != nil || !strings.Contains(string(data), "LOCAL_CODEX_PROOF") {
					t.Fatal("missing workspace change", err)
				}
			}
		})
	}
}
