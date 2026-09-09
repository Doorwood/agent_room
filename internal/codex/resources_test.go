package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResourceModelHelper(t *testing.T) {
	if os.Getenv("AGENT_ROOM_TEST_MODEL") != "1" {
		return
	}
	if os.Getenv("CODEX_THREAD_ID") != "" {
		os.Exit(9)
	}
	config, err := os.ReadFile(filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"))
	if err != nil || !strings.Contains(string(config), "shell_tool = false") {
		os.Exit(3)
	}
	if _, err = os.Lstat(filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")); err != nil {
		os.Exit(4)
	}
	cwd, _ := os.Getwd()
	enc := json.NewEncoder(os.Stdout)
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		var r struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(scan.Bytes(), &r) != nil {
			os.Exit(5)
		}
		if r.ID == nil {
			continue
		}
		var result any = map[string]any{}
		switch r.Method {
		case "config/read":
			features := map[string]bool{"shell_tool": false, "unified_exec": false}
			if os.Getenv("AGENT_ROOM_TEST_UNSAFE") == "1" {
				features["shell_tool"] = true
			}
			result = map[string]any{"config": map[string]any{"features": features}}
		case "thread/start":
			if r.Params["ephemeral"] != true || r.Params["sandbox"] != "read-only" || r.Params["cwd"] != cwd {
				os.Exit(6)
			}
			result = map[string]any{"thread": map[string]any{"id": "private-test-thread", "ephemeral": true}}
		case "mcpServerStatus/list":
			result = map[string]any{"data": []any{}}
		case "turn/start":
			input, _ := json.Marshal(r.Params["input"])
			if !strings.Contains(string(input), "private-evidence") || r.Params["threadId"] != "private-test-thread" {
				os.Exit(7)
			}
			result = map[string]any{"turn": map[string]any{"id": "resource-turn", "status": "inProgress"}}
		}
		enc.Encode(map[string]any{"id": r.ID, "result": result})
		if r.Method == "turn/start" {
			enc.Encode(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "private-test-thread", "item": map[string]string{"type": "agentMessage", "text": "isolated answer"}}})
			enc.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "private-test-thread", "turn": map[string]string{"status": "completed"}}})
		}
	}
	os.Exit(0)
}
func TestResourceSummaryUsesIsolatedProcessAndRejectsUnsafeConfig(t *testing.T) {
	original := t.TempDir()
	os.WriteFile(filepath.Join(original, "auth.json"), []byte("{}"), 0600)
	t.Setenv("CODEX_HOME", original)
	t.Setenv("AGENT_ROOM_TEST_MODEL", "1")
	t.Setenv("CODEX_THREAD_ID", "shared-thread-must-not-be-inherited")
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "fake-codex")
	content := fmt.Sprintf("#!/bin/sh\nexec '%s' -test.run=^TestResourceModelHelper$\n", strings.ReplaceAll(bin, "'", "'\\''"))
	if err = os.WriteFile(script, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []bool{false, true} {
		value := "0"
		if unsafe {
			value = "1"
		}
		t.Setenv("AGENT_ROOM_TEST_UNSAFE", value)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		answer, err := SummarizeResource(ctx, script, "explain", "private-evidence")
		cancel()
		if unsafe {
			if err == nil {
				t.Fatal("unsafe config accepted")
			}
		} else if err != nil || strings.TrimSpace(answer) != "isolated answer" {
			t.Fatal(answer, err)
		}
	}
	entries, _ := os.ReadDir(original)
	if len(entries) != 1 {
		t.Fatal("wrote to host Codex home")
	}
}
