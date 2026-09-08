package answerwindow

import (
	"agent_romm/internal/room"
	"encoding/json"
	"time"
)

// progress projects public commentary and lifecycle facts only. Untyped stream
// deltas and reasoning items may contain private reasoning and are not rendered.
func (w *Window) progress(e room.DurableEvent) error {
	var body struct {
		ID      string `json:"client_message_id"`
		Target  string `json:"target_message_id"`
		Turn    string `json:"turn_id"`
		Payload struct {
			Type   string `json:"type"`
			Phase  string `json:"phase"`
			Text   string `json:"text"`
			Status string `json:"status"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(e.Payload, &body); err != nil {
		return err
	}
	state := ""
	switch e.Kind {
	case "recovery/skipped":
		body.ID = body.Target
		state = "已跳过该任务。"
	case "recovery/continued":
		body.ID = body.Target
		state = "已继续处理，后续结果见新的任务。"
	case "recovery/retried":
		body.ID = body.Target
		state = "已重新排队，等待模型处理。"
	case "turn/running":
		state = "已接收，模型正在处理。"
	case "turn/completed":
		state = "处理完成。"
	case "turn/failed", "message/failed":
		state = "处理失败，请查看原终端的错误提示。"
	case "turn/interrupted", "message/interrupted":
		state = "任务已中断。"
	case "turn/needs-review", "message/needs-review":
		state = "任务需要确认，请查看原终端。"
	}
	if state != "" {
		w.mu.Lock()
		if body.Turn == "" && body.ID != "" {
			body.Turn = w.messageTurns[body.ID]
		}
		if e.Kind == "turn/running" && body.Turn != "" {
			if w.turnOwners == nil {
				w.turnOwners = make(map[string]room.UID)
			}
			if _, ok := w.turnOwners[body.Turn]; !ok {
				w.turnOrder = append(w.turnOrder, body.Turn)
			}
			w.turnOwners[body.Turn] = e.ActorUID
			if body.ID != "" {
				if w.turnMessages == nil {
					w.turnMessages = make(map[string]string)
					w.messageTurns = make(map[string]string)
				}
				w.turnMessages[body.Turn] = body.ID
				w.messageTurns[body.ID] = body.Turn
			}
			if len(w.turnOrder) > 4096 {
				oldTurn := w.turnOrder[0]
				if id := w.turnMessages[oldTurn]; id != "" && w.messageTurns[id] == oldTurn {
					delete(w.messageTurns, id)
				}
				delete(w.turnMessages, oldTurn)
				delete(w.turnOwners, w.turnOrder[0])
				delete(w.turnStates, w.turnOrder[0])
				w.turnOrder = w.turnOrder[1:]
			}
		}
		if _, known := w.turnOwners[body.Turn]; known {
			if w.turnStates == nil {
				w.turnStates = make(map[string]string)
			}
			w.turnStates[body.Turn] = state
		}
		for i := range w.answers {
			a := &w.answers[i]
			if a.Role != "user" || (a.Kind != "prompt" && a.Kind != "recovery-prompt") {
				continue
			}
			if (body.ID != "" && a.ClientID == body.ID) || (body.Turn != "" && a.Turn == body.Turn) {
				a.Ack = state
				if body.Turn != "" {
					a.Turn = body.Turn
				}
			}
		}
		w.revision++
		w.mu.Unlock()
		return nil
	}
	if e.Kind != "item/completed" {
		return nil
	}
	text := ""
	kind := "progress"
	switch body.Payload.Type {
	case "agentMessage":
		if body.Payload.Phase == "commentary" {
			text = body.Payload.Text
		}
	case "commandExecution":
		text = "命令执行已结束。"
		if body.Payload.Status == "failed" {
			text = "命令执行失败。"
		}
	case "fileChange":
		text = "文件变更步骤已结束。"
	case "webSearch":
		text = "检索步骤已结束。"
	}
	if text == "" {
		return nil
	}
	return w.add(Answer{Seq: uint64(e.Seq), Text: text, Role: "assistant", Author: "模型", Kind: kind, Turn: body.Turn, Time: e.CreatedAt.Format(time.RFC3339)})
}
