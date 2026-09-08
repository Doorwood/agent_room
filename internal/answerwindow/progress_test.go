package answerwindow

import (
	"agent_romm/internal/room"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAcceptanceProgressAndCompletion(t *testing.T) {
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, e := range []room.DurableEvent{
		{Seq: 1, Kind: "message/accepted", Payload: json.RawMessage(`{"kind":"prompt","body":"question","client_message_id":"a"}`)},
		{Seq: 2, Kind: "turn/running", Payload: json.RawMessage(`{"client_message_id":"a","turn_id":"t"}`)},
	} {
		if err := w.Event(e); err != nil {
			t.Fatal(err)
		}
	}
	if w.answers[0].Ack != "已接收，模型正在处理。" {
		t.Fatal(w.answers[0])
	}
	if err := w.Event(room.DurableEvent{Seq: 3, Kind: "turn/completed", Payload: json.RawMessage(`{"turn_id":"t"}`)}); err != nil {
		t.Fatal(err)
	}
	if w.answers[0].Ack != "处理完成。" {
		t.Fatal(w.answers[0])
	}
}
func TestProgressIncludesPublicCommentaryButNotReasoning(t *testing.T) {
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for i, payload := range []string{
		`{"payload":{"type":"reasoning","text":"private reasoning","summary":[{"text":"private summary"}]}}`,
		`{"payload":{"type":"agentMessage","phase":"commentary","text":"我会先检查测试结果。"}}`,
		`{"payload":{"type":"commandExecution","status":"completed","aggregatedOutput":"raw secret output"}}`,
		`{"payload":{"type":"agentMessage","phase":"final_answer","text":"final"}}`,
	} {
		if err := w.Event(room.DurableEvent{Seq: room.Seq(i + 1), Kind: "item/completed", Payload: json.RawMessage(payload)}); err != nil {
			t.Fatal(err)
		}
	}
	if len(w.answers) != 2 || w.answers[0].Text != "我会先检查测试结果。" || w.answers[1].Text != "命令执行已结束。" {
		t.Fatal(w.answers)
	}
}

func TestInterleavedQuestionsKeepAnswerOwnership(t *testing.T) {
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Room("room")
	for _, e := range []room.DurableEvent{
		{Seq: 1, ActorUID: 10, Kind: "message/accepted", Payload: json.RawMessage(`{"kind":"prompt","body":"Alice question","client_message_id":"a"}`)},
		{Seq: 2, ActorUID: 20, Kind: "message/accepted", Payload: json.RawMessage(`{"kind":"prompt","body":"Bob question","client_message_id":"b"}`)},
		{Seq: 3, ActorUID: 10, Kind: "turn/running", Payload: json.RawMessage(`{"client_message_id":"a","turn_id":"ta"}`)},
		{Seq: 4, Kind: "item/completed", Payload: json.RawMessage(`{"turn_id":"ta","payload":{"type":"agentMessage","phase":"commentary","text":"Alice progress"}}`)},
	} {
		if err := w.Event(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Add(room.DurableEvent{Seq: 5, Payload: json.RawMessage(`{"turn_id":"ta"}`)}, "Alice answer"); err != nil {
		t.Fatal(err)
	}
	if err := w.Event(room.DurableEvent{Seq: 6, ActorUID: 20, Kind: "turn/running", Payload: json.RawMessage(`{"client_message_id":"b","turn_id":"tb"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := w.Add(room.DurableEvent{Seq: 7, Payload: json.RawMessage(`{"turn_id":"tb"}`)}, "Bob answer"); err != nil {
		t.Fatal(err)
	}
	for i, want := range []room.UID{10, 20, 10, 10, 20} {
		if w.answers[i].Owner == nil || *w.answers[i].Owner != want {
			t.Fatal(i, w.answers[i])
		}
	}
	w.Metadata("10.1.2.3:7443", "session", "viewer")
	w.Members([]room.Member{{UID: 20, Name: "Bob"}, {UID: 10, Name: "Alice"}})
	out := httptest.NewRecorder()
	w.serve(out, httptest.NewRequest("GET", w.URL()+"answers", nil))
	if !strings.Contains(out.Body.String(), `"host":"10.1.2.3:7443"`) || !strings.Contains(out.Body.String(), `"viewer":"viewer"`) {
		t.Fatal(out.Body.String())
	}
}

func TestTurnStatusSurvivesPromptEviction(t *testing.T) {
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.Event(room.DurableEvent{Seq: 1, ActorUID: 10, Kind: "turn/running", Payload: json.RawMessage(`{"client_message_id":"a","turn_id":"ta"}`)}); err != nil {
		t.Fatal(err)
	}
	for i := 2; i < maxAnswers+20; i++ {
		if err := w.Event(room.DurableEvent{Seq: room.Seq(i), Kind: "item/completed", Payload: json.RawMessage(`{"turn_id":"ta","payload":{"type":"agentMessage","phase":"commentary","text":"step"}}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Event(room.DurableEvent{Seq: 999, Kind: "turn/failed", Payload: json.RawMessage(`{"turn_id":"ta"}`)}); err != nil {
		t.Fatal(err)
	}
	out := httptest.NewRecorder()
	w.serve(out, httptest.NewRequest("GET", w.URL()+"answers", nil))
	if !strings.Contains(out.Body.String(), `"taskStatus":"处理失败，请查看原终端的错误提示。"`) {
		t.Fatal("terminal status lost")
	}
}

func TestMessageLifecycleResolvesTurnAfterPromptEviction(t *testing.T) {
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.Event(room.DurableEvent{Seq: 1, ActorUID: 10, Kind: "turn/running", Payload: json.RawMessage(`{"client_message_id":"a","turn_id":"ta"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := w.Event(room.DurableEvent{Seq: 2, Kind: "item/completed", Payload: json.RawMessage(`{"turn_id":"ta","payload":{"type":"agentMessage","phase":"commentary","text":"step"}}`)}); err != nil {
		t.Fatal(err)
	}
	if err := w.Event(room.DurableEvent{Seq: 3, Kind: "message/needs-review", Payload: json.RawMessage(`{"client_message_id":"a"}`)}); err != nil {
		t.Fatal(err)
	}
	if w.turnStates["ta"] != "任务需要确认，请查看原终端。" {
		t.Fatal(w.turnStates)
	}
	if err := w.Event(room.DurableEvent{Seq: 4, Kind: "recovery/skipped", Payload: json.RawMessage(`{"target_message_id":"a"}`)}); err != nil {
		t.Fatal(err)
	}
	if w.turnStates["ta"] != "已跳过该任务。" {
		t.Fatal(w.turnStates)
	}
	out := httptest.NewRecorder()
	w.serve(out, httptest.NewRequest("GET", w.URL()+"answers", nil))
	if !strings.Contains(out.Body.String(), `"taskStatus":"已跳过该任务。"`) {
		t.Fatal(out.Body.String())
	}
}
