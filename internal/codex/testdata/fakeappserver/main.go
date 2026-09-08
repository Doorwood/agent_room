package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	cliOutput = "codex-cli 0.153.4"
	version   = "0.153.4"
	maxLine   = 8 << 20
)

type options struct {
	record  string
	mode    string
	delay   time.Duration
	delta   string
	control string
}

type request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(cliOutput)
		return
	}
	if len(os.Args) < 4 || os.Args[1] != "app-server" || os.Args[2] != "--listen" || os.Args[3] != "stdio://" {
		os.Exit(2)
	}
	flags := flag.NewFlagSet("fakeappserver", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var cfg options
	flags.StringVar(&cfg.record, "record", "", "test-owned request log")
	flags.StringVar(&cfg.mode, "mode", "normal", "deterministic test mode")
	flags.DurationVar(&cfg.delay, "delay", 50*time.Millisecond, "delayed-response duration")
	flags.StringVar(&cfg.delta, "delta", "", "delta to stream after turn/start")
	flags.StringVar(&cfg.control, "control-dir", "", "test-owned process control directory")
	if err := flags.Parse(os.Args[4:]); err != nil || !validMode(cfg.mode) {
		os.Exit(2)
	}
	if cfg.control != "" {
		if err := controlledServe(cfg); err != nil {
			os.Exit(1)
		}
		return
	}
	if err := serve(cfg); err != nil {
		os.Exit(1)
	}
}

func validMode(mode string) bool {
	switch mode {
	case "normal", "eof", "delayed-response", "malformed-response", "reverse-request", "crash-after-receive-before-response":
		return true
	default:
		return false
	}
}

func serve(cfg options) error {
	var record *os.File
	var err error
	if cfg.record != "" {
		record, err = os.OpenFile(cfg.record, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer record.Close()
	}
	writer := bufio.NewWriter(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), maxLine+1)
	reverseSent := false
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if record != nil {
			if _, err := record.Write(append(line, '\n')); err != nil {
				return err
			}
			if err := record.Sync(); err != nil {
				return err
			}
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil || strings.TrimSpace(req.Method) == "" {
			return err
		}
		if req.Method == "initialized" {
			continue
		}
		switch cfg.mode {
		case "eof":
			return nil
		case "malformed-response":
			_, _ = writer.WriteString("{malformed\n")
			return writer.Flush()
		case "delayed-response":
			time.Sleep(cfg.delay)
		case "crash-after-receive-before-response":
			if req.Method == "turn/start" {
				return nil
			}
		case "reverse-request":
			if !reverseSent {
				reverseSent = true
				if err := writeJSON(writer, map[string]any{
					"id": "reverse-1", "method": "item/tool/requestUserInput", "params": map[string]any{},
				}); err != nil {
					return err
				}
			}
		}
		if err := respond(writer, req); err != nil {
			return err
		}
		if req.Method == "turn/start" && cfg.delta != "" {
			if err := writeJSON(writer, map[string]any{
				"method": "item/agentMessage/delta",
				"params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "item-1", "delta": cfg.delta},
			}); err != nil {
				return err
			}
			if err := writeJSON(writer, map[string]any{
				"method": "turn/completed",
				"params": map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "status": "completed", "items": []any{}}},
			}); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func respond(writer *bufio.Writer, req request) error {
	var result any
	switch req.Method {
	case "initialize":
		result = map[string]any{
			"codexHome": "/tmp/fake-codex", "platformFamily": "unix", "platformOs": "linux",
			"userAgent": "agent_romm/" + version,
		}
	case "account/read":
		result = map[string]any{"account": map[string]any{"type": "apiKey"}, "requiresOpenaiAuth": false}
	case "thread/start", "thread/resume":
		result = map[string]any{
			"thread": map[string]any{"id": "thread-1", "cwd": "/srv/project", "turns": []any{}},
			"cwd":    "/srv/project", "approvalPolicy": "never", "sandbox": map[string]any{"type": "dangerFullAccess"},
		}
	case "thread/read":
		result = map[string]any{"thread": map[string]any{"id": "thread-1", "cwd": "/srv/project", "turns": []any{}}}
	case "turn/start":
		result = map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}}
	case "turn/steer":
		result = map[string]any{"turnId": "turn-1"}
	default:
		result = map[string]any{}
	}
	return writeJSON(writer, map[string]any{"id": req.ID, "result": result})
}

func writeJSON(writer *bufio.Writer, value any) error {
	line, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := writer.Write(append(line, '\n')); err != nil {
		return err
	}
	return writer.Flush()
}
