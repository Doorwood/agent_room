package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// controlledServe is a test-executable fixture, never linked into agent_romm.
// File controls let the test finish/crash turns without timing model responses.
func controlledServe(cfg options) error {
	log, err := os.OpenFile(cfg.record, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	writer := bufio.NewWriter(os.Stdout)
	turns := []map[string]any{}
	saved := filepath.Join(cfg.control, "turns.json")
	if data, e := os.ReadFile(saved); e == nil {
		if e = json.Unmarshal(data, &turns); e != nil {
			return e
		}
	}
	persist := func() error {
		data, e := json.Marshal(turns)
		if e != nil {
			return e
		}
		return os.WriteFile(saved, data, 0600)
	}
	consume := func(name string) bool { return os.Remove(filepath.Join(cfg.control, name)) == nil }
	thread := func() map[string]any { return map[string]any{"id": "thread-1", "cwd": cwd, "turns": turns} }
	lines := make(chan []byte)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 65536), maxLine)
		for scanner.Scan() {
			lines <- append([]byte(nil), scanner.Bytes()...)
		}
	}()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	active := ""
	unsupported, interrupted, inputAnswered := false, false, false
	for {
		if unsupported && interrupted && inputAnswered && active != "" {
			for _, turn := range turns {
				if turn["id"] == active {
					turn["status"] = "interrupted"
				}
			}
			if err := persist(); err != nil {
				return err
			}
			if err := writeJSON(writer, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": active, "status": "interrupted", "items": []any{}}}}); err != nil {
				return err
			}
			active = ""
			unsupported = false
			interrupted = false
			inputAnswered = false
		}
		select {
		case <-tick.C:
			if active != "" && consume("finish") {
				item := map[string]any{"id": "item-" + active[5:], "type": "agentMessage", "text": "completed " + active}
				for _, turn := range turns {
					if turn["id"] == active {
						turn["status"] = "completed"
						turn["items"] = []any{item}
					}
				}
				if err := persist(); err != nil {
					return err
				}
				if err := writeJSON(writer, map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread-1", "turnId": active, "completedAtMs": time.Now().UnixMilli(), "item": item}}); err != nil {
					return err
				}
				if err := writeJSON(writer, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": active, "status": "completed", "items": []any{item}}}}); err != nil {
					return err
				}
				active = ""
			}
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			if _, err := log.Write(append(line, '\n')); err != nil {
				return err
			}
			if err := log.Sync(); err != nil {
				return err
			}
			var req request
			if err := json.Unmarshal(line, &req); err != nil {
				return err
			}
			if req.Method == "" && string(req.ID) == `"reverse-approval"` {
				if err := writeJSON(writer, map[string]any{"id": "reverse-1", "method": "item/tool/requestUserInput", "params": map[string]any{"threadId": "thread-1", "turnId": active}}); err != nil {
					return err
				}
			}
			if req.Method == "initialized" || req.Method == "" {
				if string(req.ID) == `"reverse-1"` {
					inputAnswered = true
				}
				continue
			}
			if (req.Method == "initialize" && consume("hang-init")) || (req.Method == "turn/start" && consume("hang-turn")) {
				for range lines {
				}
				return nil
			}
			var result any = map[string]any{}
			switch req.Method {
			case "account/read":
				result = map[string]any{"account": map[string]any{"type": "apiKey"}, "requiresOpenaiAuth": false}
			case "initialize":
				agent := "agent_romm/" + version
				if consume("wrong-agent") {
					agent = "agent_romm/0.0.0"
				}
				result = map[string]any{"userAgent": agent, "codexHome": cwd, "platformFamily": "unix", "platformOs": "linux"}
			case "thread/start":
				if consume("crash-thread") {
					fmt.Fprintln(os.Stderr, "SECRET_STDERR_SENTINEL")
					return nil
				}
				fallthrough
			case "thread/resume":
				result = map[string]any{"thread": thread(), "cwd": cwd, "approvalPolicy": "never", "sandbox": map[string]any{"type": "dangerFullAccess"}}
			case "thread/read":
				result = map[string]any{"thread": thread()}
			case "turn/start":
				if active != "" {
					return fmt.Errorf("overlapping active turn")
				}
				if consume("crash-next") {
					fmt.Fprintln(os.Stderr, "SECRET_STDERR_SENTINEL")
					_, _ = writer.WriteString("{SECRET_RPC_SENTINEL\n")
					_ = writer.Flush()
					return fmt.Errorf("SECRET_RPC_SENTINEL")
				}
				active = fmt.Sprintf("turn-%d", len(turns)+1)
				turn := map[string]any{"id": active, "status": "inProgress", "items": []any{}}
				turns = append(turns, turn)
				if err := persist(); err != nil {
					return err
				}
				result = map[string]any{"turn": turn}
			case "turn/steer":
				result = map[string]any{"turnId": active}
			case "turn/interrupt":
				if unsupported {
					interrupted = true
				}
			}
			if err := writeJSON(writer, map[string]any{"id": req.ID, "result": result}); err != nil {
				return err
			}
			if req.Method == "turn/start" {
				if consume("malformed-terminal") {
					unsupported = true
					inputAnswered = true
					if err := writeJSON(writer, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": active, "status": "not-a-terminal-state", "items": []any{}}}}); err != nil {
						return err
					}
				}
				if err := writeJSON(writer, map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"threadId": "thread-1", "turnId": active, "itemId": "item-" + active[5:], "delta": "streaming " + active}}); err != nil {
					return err
				}
				if consume("reverse") {
					unsupported = true
					if err := writeJSON(writer, map[string]any{"id": "reverse-approval", "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "thread-1", "turnId": active}}); err != nil {
						return err
					}
				}
			}
		}
	}
}
