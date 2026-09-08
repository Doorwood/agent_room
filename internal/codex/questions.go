package codex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"agent_romm/internal/room"
)

// Read-only filesystem policy does not constrain MCP servers, apps, or hooks.
// Refuse this path when such extensions are configured: never silently fall
// back to a normal, writable turn or rely on the question prompt for safety.
func (a *Adapter) StartReadOnlyTurn(ctx context.Context, thread room.ThreadID, id room.ClientMessageID, text string) (room.TurnID, error) {
	if !validProtocolID(string(thread)) || !room.ValidClientMessageID(id) || strings.TrimSpace(text) == "" {
		return "", notSentMutation("turn/start", ErrInvalidAgentRequest)
	}
	var config struct {
		Config map[string]json.RawMessage `json:"config"`
	}
	if err := a.rpc.Call(ctx, "config/read", map[string]any{"includeLayers": false, "cwd": a.projectRoot}, &config); err != nil {
		return "", notSentMutation("turn/start", err)
	}
	if !safeQuestionConfig(config.Config) {
		return "", notSentMutation("turn/start", errors.New("当前 Codex 配置含沙箱外扩展，无法保证只读问答；请求未执行"))
	}
	var servers struct {
		Data       []json.RawMessage `json:"data"`
		NextCursor *string           `json:"nextCursor"`
	}
	if err := a.rpc.Call(ctx, "mcpServerStatus/list", map[string]any{"threadId": string(thread)}, &servers); err != nil {
		return "", notSentMutation("turn/start", err)
	}
	if servers.Data == nil || len(servers.Data) > 0 || servers.NextCursor != nil {
		return "", notSentMutation("turn/start", errors.New("当前 Codex 已加载 MCP 工具，无法保证只读问答；请求未执行"))
	}
	request := startTurnRequest(string(thread), string(id), a.projectRoot, text)
	request.SandboxPolicy = sandboxPolicy{Type: "readOnly"}
	// No turn can escalate: the RPC client rejects permission/tool approvals.
	var response turnStartResult
	if err := a.rpc.Call(ctx, "turn/start", request, &response); err != nil {
		return "", mutationError("turn/start", err)
	}
	turn, err := decodeProtocolTurn(response.Turn)
	if err != nil {
		return "", unknownMutation("turn/start", err)
	}
	if _, ok := mapTurnState(turn.Status); !ok {
		return "", unknownMutation("turn/start", ErrInvalidResponse)
	}
	return room.TurnID(turn.ID), nil
}
func safeQuestionConfig(config map[string]json.RawMessage) bool {
	if config == nil {
		return false
	}
	// Nonempty extension settings are conservatively rejected, even if individual
	// entries look disabled: lazy discovery and per-project overrides are unsafe.
	for _, key := range []string{"mcp_servers", "apps", "plugins", "hooks", "notify", "environments"} {
		if b, ok := config[key]; ok {
			v := strings.TrimSpace(string(b))
			if v != "null" && v != "{}" && v != "[]" {
				return false
			}
		}
	}
	var features map[string]bool
	if b := config["features"]; len(b) > 0 && string(b) != "null" {
		if json.Unmarshal(b, &features) != nil {
			return false
		}
	}
	for _, key := range []string{"apps", "plugins", "hooks", "multi_agent", "collab", "js_repl", "memory_tool"} {
		if features[key] {
			return false
		}
	}
	return true
}
func (r *Runtime) StartReadOnlyTurn(ctx context.Context, thread room.ThreadID, id room.ClientMessageID, text string) (room.TurnID, error) {
	a, err := r.snapshotAdapter()
	if err != nil {
		return "", unavailableMutation("turn/start")
	}
	return a.StartReadOnlyTurn(ctx, thread, id, text)
}
