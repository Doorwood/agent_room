package answerwindow

import (
	"agent_romm/internal/client"
	"agent_romm/internal/room"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"
)

// EnableChat is called before the URL is exposed to a browser.
func (w *Window) EnableChat() <-chan client.Submission {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.submissions = make(chan client.Submission)
	return w.submissions
}
func (w *Window) Members(members []room.Member) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, member := range members {
		w.names[member.UID] = member.Name
	}
	w.revision++
}
func (w *Window) Event(e room.DurableEvent) error {
	if err := w.progress(e); err != nil {
		return err
	}
	switch e.Kind {
	case "message/accepted", "note/accepted", "control/accepted":
	default:
		return nil
	}
	var body struct {
		Body string `json:"body"`
		Kind string `json:"kind"`
		ID   string `json:"client_message_id"`
	}
	if err := json.Unmarshal(e.Payload, &body); err != nil {
		return err
	}
	if e.Kind == "control/accepted" {
		if body.Kind != "steer" {
			return nil
		}
		var control struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(body.Body), &control); err != nil {
			return err
		}
		body.Body = control.Text
	}
	ack := ""
	if e.Kind == "message/accepted" {
		ack = "已接收，等待模型处理。"
	}
	return w.add(Answer{Ack: ack, Seq: uint64(e.Seq), Text: body.Body, Role: "user", UID: e.ActorUID, Kind: body.Kind, ClientID: body.ID, Time: e.CreatedAt.Format(time.RFC3339)})
}
func (w *Window) post(out http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "http://"+r.Host {
		http.Error(out, "same-origin request required", http.StatusForbidden)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		http.Error(out, "JSON required", http.StatusUnsupportedMediaType)
		return
	}
	var body struct {
		ExpectedTurn string `json:"expectedTurn"`
		ID           string `json:"id"`
		Text         string `json:"text"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(out, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(out, "invalid message", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	submission := client.Submission{Context: ctx, ID: body.ID, Text: body.Text, Result: make(chan error, 1)}
	if r.URL.Path == w.path+"cancel" {
		submission.Method = "cancel"
		submission.ExpectedTurnID = body.ExpectedTurn
		w.mu.Lock()
		active, connected := w.activeTurn, w.connected
		w.mu.Unlock()
		if !connected || active == "" || active != body.ExpectedTurn {
			http.Error(out, "当前任务已变化或连接中断，请等待状态同步后重试。", 409)
			return
		}
	}
	if err := submission.Validate(); err != nil {
		http.Error(out, "请求无效或消息过长", http.StatusBadRequest)
		return
	}
	w.mu.Lock()
	submissions := w.submissions
	w.mu.Unlock()
	if submissions == nil {
		http.Error(out, "chat unavailable", http.StatusServiceUnavailable)
		return
	}
	select {
	case submissions <- submission:
	case <-ctx.Done():
		http.Error(out, "连接不可用，尚未提交。请重试。", http.StatusServiceUnavailable)
		return
	}
	select {
	case err := <-submission.Result:
		if err != nil {
			http.Error(out, "host 未接受请求；任务可能已结束，请刷新状态后重试。", http.StatusConflict)
			return
		}
		out.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(out, `{"accepted":true}`)
	case <-ctx.Done():
		http.Error(out, "确认超时，可能已提交。重试会使用同一消息 ID。", http.StatusGatewayTimeout)
	}
}

func (w *Window) Metadata(host, session, viewer string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.host, w.session, w.viewer = host, session, viewer
	w.revision++
}
