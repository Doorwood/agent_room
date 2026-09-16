package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
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
			if expected := os.Getenv("AGENT_ROOM_TEST_SETTINGS"); expected != "" {
				got, _ := json.Marshal(r.Params["config"])
				var want, actual any
				json.Unmarshal([]byte(expected), &want)
				json.Unmarshal(got, &actual)
				wantJSON, _ := json.Marshal(want)
				actualJSON, _ := json.Marshal(actual)
				if string(wantJSON) != string(actualJSON) {
					enc.Encode(map[string]any{"id": r.ID, "error": map[string]any{"code": -32602, "message": "model settings were not inherited"}})
					continue
				}
			}
			requestedCWD, _ := r.Params["cwd"].(string)
			requestedCWD, _ = filepath.EvalSymlinks(requestedCWD)
			actualCWD, _ := filepath.EvalSymlinks(cwd)
			if r.Params["ephemeral"] != true || r.Params["sandbox"] != "read-only" || requestedCWD != actualCWD {
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
			if failure := os.Getenv("AGENT_ROOM_TEST_FAILURE"); failure != "" {
				enc.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "private-test-thread", "turn": map[string]any{"status": "failed", "error": json.RawMessage(failure)}}})
				continue
			}
			enc.Encode(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "private-test-thread", "item": map[string]string{"type": "agentMessage", "text": "isolated answer"}}})
			enc.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "private-test-thread", "turn": map[string]string{"status": "completed"}}})
		}
	}
	os.Exit(0)
}

// Regression: planning must inherit only model settings, never Host extensions.
func TestPlannerInheritsEffectiveModelSettings(t *testing.T) {
	for _, tc := range []struct{ config, expected string }{
		{`{"model":"environment-model","model_reasoning_effort":"medium","mcp_servers":{"writer":{}}}`, `{"model":"environment-model","model_reasoning_effort":"medium"}`},
		{`{"model":"another-model","model_reasoning_effort":"high","hooks":{"Stop":["must-not-run"]}}`, `{"model":"another-model","model_reasoning_effort":"high"}`},
		{`{"model":null,"model_reasoning_effort":null}`, `{}`},
		{`{"model":"provider-model","model_provider":"private","model_providers":{"private":{"name":"Private","base_url":"https://example.invalid","env_key":"PRIVATE_KEY"},"other":{"name":"Unused"}}}`, `{"model":"provider-model","model_provider":"private","model_providers":{"private":{"name":"Private","base_url":"https://example.invalid","env_key":"PRIVATE_KEY"}}}`},
	} {
		t.Run(tc.config, func(t *testing.T) {
			script := isolatedTestExecutable(t)
			t.Setenv("AGENT_ROOM_TEST_SETTINGS", tc.expected)
			a := plannerSettingsAdapter(t, tc.config)
			r := &Runtime{supervisor: &Supervisor{executable: script}, adapter: a}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			answer, err := r.PlanCollaboration(ctx, "private-evidence")
			if err != nil || strings.TrimSpace(answer) != "isolated answer" {
				t.Fatalf("answer=%q err=%v", answer, err)
			}
		})
	}
}

func plannerSettingsAdapter(t *testing.T, config string) *Adapter {
	t.Helper()
	project := t.TempDir()
	rpc, _ := newRPCPeer(t, func(req map[string]json.RawMessage) json.RawMessage {
		var params struct {
			CWD string `json:"cwd"`
		}
		if err := json.Unmarshal(req["params"], &params); err != nil {
			t.Error(err)
		}
		if string(req["method"]) != `"config/read"` || params.CWD != project {
			t.Errorf("unexpected Host call: %s cwd=%q", req["method"], params.CWD)
		}
		return json.RawMessage(`{"config":` + config + `}`)
	})
	a, err := NewAdapter(rpc, AdapterConfig{ProjectRoot: project})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Regression: retain actionable structured failures without exposing raw payloads.
func TestIsolatedModelReportsSafeFailureReason(t *testing.T) {
	for _, tc := range []struct{ name, failure, want string }{
		{"quota", `{"message":"private-evidence Bearer secret","codexErrorInfo":"usageLimitExceeded"}`, "usageLimitExceeded"},
		{"unauthorized", `{"message":"secret","codexErrorInfo":{"httpConnectionFailed":{"httpStatusCode":401}}}`, "401"},
		{"unknown", `{"message":"private-evidence Bearer secret","codexErrorInfo":"unknown-secret-code"}`, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := isolatedTestExecutable(t)
			t.Setenv("AGENT_ROOM_TEST_FAILURE", tc.failure)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := SummarizeResource(ctx, script, "explain", "private-evidence")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q in error, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private-evidence") {
				t.Fatalf("sensitive error exposed: %v", err)
			}
		})
	}
}

func isolatedTestExecutable(t *testing.T) string {
	t.Helper()
	original := t.TempDir()
	if err := os.WriteFile(filepath.Join(original, "auth.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", original)
	t.Setenv("AGENT_ROOM_TEST_MODEL", "1")
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "fake-codex")
	content := fmt.Sprintf("#!/bin/sh\nexec '%s' -test.run=^TestResourceModelHelper$\n", strings.ReplaceAll(bin, "'", "'\\''"))
	if err := os.WriteFile(script, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	return script
}
func TestIsolatedModelIgnoresHostMCPAndRejectsUnsafeConfig(t *testing.T) {
	original := t.TempDir()
	os.WriteFile(filepath.Join(original, "auth.json"), []byte("{}"), 0600)
	os.WriteFile(filepath.Join(original, "config.toml"), []byte("[mcp_servers.writer]\ncommand = \"must-not-start\"\n"), 0600)
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
		r := &Runtime{supervisor: &Supervisor{executable: script}, adapter: plannerSettingsAdapter(t, `{}`)}
		answer, err := r.PlanCollaboration(ctx, "private-evidence")
		if !unsafe && err == nil {
			answer, err = SummarizeResource(ctx, script, "explain", "private-evidence")
		}
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
	if len(entries) != 2 {
		t.Fatal("wrote to host Codex home")
	}
}

// Explicit opt-in: sends only a synthetic planning request using the current login.
func TestLiveIsolatedCollaborationPlanner(t *testing.T) {
	if os.Getenv("AGENT_ROOM_TEST_LIVE_PLANNER") != "1" {
		t.Skip("requires Codex login and live model")
	}
	executable, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// Read real effective settings without submitting work to a shared thread.
	cmd := exec.CommandContext(ctx, executable, "app-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	rpc := NewRPCClient(stdout, stdin)
	go rpc.Run(ctx)
	if err := rpc.Call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "agent_room_planner_test", "version": "1"}}, new(json.RawMessage)); err != nil {
		t.Fatal(err)
	}
	if err := rpc.Notify(ctx, "initialized", nil); err != nil {
		t.Fatal(err)
	}
	project, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAdapter(rpc, AdapterConfig{ProjectRoot: project})
	if err != nil {
		t.Fatal(err)
	}
	r := &Runtime{supervisor: &Supervisor{executable: executable}, adapter: a}
	answer, err := r.PlanCollaboration(ctx, `只返回 JSON：{"steps":[{"id":"build","agent":"codex.b","prompt":"实现","dependsOn":[]},{"id":"review","agent":"claude.a","prompt":"评审","dependsOn":["build"]}]}。这是规划测试，禁止实际执行。`)
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Steps []struct {
			ID        string
			DependsOn []string
		}
	}
	if json.Unmarshal([]byte(answer), &plan) != nil || len(plan.Steps) != 2 || len(plan.Steps[1].DependsOn) != 1 || plan.Steps[1].DependsOn[0] != "build" {
		t.Fatal("invalid plan")
	}
}
