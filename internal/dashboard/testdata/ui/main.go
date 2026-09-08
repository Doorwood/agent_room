// Test-only UI fixture. It does not start Codex or execute submitted instructions.
package main

import (
	"agent_romm/internal/answerwindow"
	"agent_romm/internal/dashboard"
	"agent_romm/internal/network"
	"agent_romm/internal/questions"
	"agent_romm/internal/room"
	"agent_romm/internal/store"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	dir, err := os.MkdirTemp("", "agent-room-ui-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	qa, err := questions.Open(dir, func(ctx context.Context, actor room.Actor, id room.ClientMessageID, text string) (room.QuestionAnswer, error) {
		return room.QuestionAnswer{Session: "host-thread", Text: "只读回答：" + text}, nil
	})
	if err != nil {
		panic(err)
	}
	defer qa.Close()
	taskDB, err := store.Open(ctx, filepath.Join(dir, "tasks.db"))
	if err != nil {
		panic(err)
	}
	defer taskDB.Close()
	if err = taskDB.InitializeRoom(ctx, store.RoomSeed{ID: "ui-tasks", DisplayName: "Tasks", HostID: "ui", ProjectRoot: "/workspace/demo-project", ExecutionOwnerUID: 1001, Members: []room.Member{{UID: 1001, Name: "alice"}}}); err != nil {
		panic(err)
	}
	sourceID := room.ClientMessageID(strings.Repeat("1", 32))
	if _, err = taskDB.AcceptMessage(ctx, "ui-tasks", room.Actor{UID: 1001, Name: "alice"}, room.SubmitInput{ClientMessageID: sourceID, Text: "完成项目登录优化"}); err != nil {
		panic(err)
	}
	if _, err = taskDB.BindThread(ctx, "ui-tasks", room.ThreadSnapshot{ID: "ui-thread", CWD: "/workspace/demo-project"}); err != nil {
		panic(err)
	}
	finishTask := func(id room.ClientMessageID) error {
		if err := taskDB.BeginDispatch(ctx, "ui-tasks", id); err != nil {
			return err
		}
		turn := room.TurnID("turn-" + id)
		if _, err := taskDB.BindRunningTurn(ctx, "ui-tasks", id, turn); err != nil {
			return err
		}
		if _, err := taskDB.RecordCompletedItem(ctx, "ui-tasks", room.CompletedItem{ThreadID: "ui-thread", TurnID: turn, ItemID: room.ItemID("item-" + id), Payload: json.RawMessage(`{"type":"agentMessage","text":"实现完成，请验收。"}`)}); err != nil {
			return err
		}
		_, err := taskDB.FinishTurn(ctx, "ui-tasks", room.FinishTurnInput{TurnID: turn, State: room.RequestCompleted})
		return err
	}
	if err = finishTask(sourceID); err != nil {
		panic(err)
	}
	catalog := dashboard.Catalog{Home: dir, Config: filepath.Join(dir, "config")}
	session := strings.Repeat("a", 32) + "." + strings.Repeat("b", 64)
	if _, err := catalog.Add("127.0.0.1:7443", session, "alice"); err != nil {
		panic(err)
	}
	connector := func(ctx context.Context, r dashboard.Room, update dashboard.Update) error {
		update("pending", "等待 host 审批（测试模拟）", "")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		w, err := answerwindow.Start()
		if err != nil {
			return err
		}
		defer w.Close()
		w.EnableUploads(func(ctx context.Context, name string, size int64, reader io.ReadSeeker) (network.Upload, error) {
			data, err := io.ReadAll(reader)
			if err != nil {
				return network.Upload{}, err
			}
			sum := sha256.Sum256(data)
			target := filepath.Join(dir, fmt.Sprintf("%x-%s", sum, filepath.Base(name)))
			if err = os.WriteFile(target, data, 0600); err != nil {
				return network.Upload{}, err
			}
			return network.Upload{Name: filepath.Base(name), Path: target, Size: size}, nil
		})
		w.DraftDirectory(filepath.Join(dir, "drafts"))
		w.EnableDownloads(func(ctx context.Context, id string, out io.Writer) (network.Upload, error) {
			if filepath.Base(id) != id || len(id) < 66 {
				return network.Upload{}, fmt.Errorf("invalid id")
			}
			b, err := os.ReadFile(filepath.Join(dir, id))
			if err != nil {
				return network.Upload{}, err
			}
			_, err = out.Write(b)
			return network.Upload{Name: id[65:], Size: int64(len(b))}, err
		})
		role := "roommate"
		uid := room.UID(1001)
		if r.Name == "visitor" {
			role = "visitor"
			uid = 1003
		}
		if r.Name == "asker" {
			role = "asker"
			uid = 1002
		}
		w.EnableQuestions(func(ctx context.Context, req network.QueryRequest) (network.QueryReply, error) {
			reply := network.QueryReply{Role: role, Ready: true}
			var err error
			if req.Action == "list" {
				target, all := uid, role == "roommate"
				if all && req.UID > 0 {
					target = req.UID
					all = false
				}
				reply.Entries, err = qa.List(ctx, target, all, req.Before)
			}
			if req.Action == "ask" {
				entry, e := qa.Ask(ctx, room.Member{UID: uid, Name: r.Name}, req.ID, req.Text)
				err = e
				reply.Entries = []questions.Entry{entry}
			}
			return reply, err
		})
		w.EnableTasks(func(ctx context.Context, req room.TaskRequest) (room.TaskReply, error) {
			if req.Action == "list" || req.Action == "get" {
				return taskDB.Tasks(ctx, "ui-tasks", req)
			}
			if role != "roommate" {
				return room.TaskReply{}, fmt.Errorf("read only")
			}
			task, err := taskDB.ChangeTask(ctx, "ui-tasks", room.Actor{UID: 1001, Name: "alice"}, req)
			return room.TaskReply{Task: &task}, err
		})
		w.RefreshRole(ctx)
		w.Metadata(r.Address, r.Session, r.Name)
		w.Room("ui-room")
		if err := catalog.RememberProject(r.Address, r.Session, "/workspace/demo-project"); err != nil {
			return err
		}
		w.Members([]room.Member{{UID: 1001, Name: r.Name}, {UID: 1002, Name: "asker"}})
		w.Connection(true)
		seed := func(seq room.Seq, text string) {
			payload, _ := json.Marshal(map[string]string{"body": text, "kind": "prompt", "client_message_id": fmt.Sprint(seq)})
			w.Event(room.DurableEvent{Seq: seq, ActorUID: 1001, Kind: "message/accepted", Payload: payload, CreatedAt: time.Now()})
		}
		emitTasks := func(after room.Seq) error {
			events, err := taskDB.Events(ctx, "ui-tasks", after, 0, 1000)
			if err != nil {
				return err
			}
			for _, e := range events {
				if err = w.Event(e); err != nil {
					return err
				}
				if e.Kind == "item/completed" {
					var body struct {
						Payload struct {
							Text string `json:"text"`
						} `json:"payload"`
					}
					json.Unmarshal(e.Payload, &body)
					if err = w.Add(e, body.Payload.Text); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if r.Name == "tasks" {
			if err = emitTasks(0); err != nil {
				return err
			}
		} else {
			seed(1, "一起看看项目的下一步")
			w.Add(room.DurableEvent{Seq: 2, CreatedAt: time.Now()}, "## 已准备好\n\n支持 **协作** 与 [文档](https://example.com/docs)。\n\nhttps://example.com/plain\n\n`https://example.com/inline`\n\n```sh\nagent_room dashboard\n```\n\n| 项目 | 状态 |\n| --- | --- |\n| 页面 | 就绪 |\n\n<script>window.pwned=1</script>\n\n[危险链接](javascript:alert(1))\n\n![不加载图片](https://example.com/tracking.png)")
		}
		submissions := w.EnableChat()
		update("connected", "", w.URL())
		seq := room.Seq(3)
		if r.Name == "history" {
			for ; seq <= 132; seq++ {
				seed(seq, fmt.Sprintf("历史消息 %d", seq))
			}
		}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case submission := <-submissions:
				if r.Name == "tasks" {
					before, _ := taskDB.LatestSeq(ctx, "ui-tasks")
					_, err := taskDB.AcceptMessage(ctx, "ui-tasks", room.Actor{UID: 1001, Name: "alice"}, room.SubmitInput{ClientMessageID: room.ClientMessageID(submission.ID), Text: submission.Text, TaskID: submission.TaskID})
					if err == nil {
						err = finishTask(room.ClientMessageID(submission.ID))
					}
					if err == nil {
						err = emitTasks(before)
					}
					submission.Result <- err
					continue
				}

				if submission.Method == "cancel" {
					w.Event(room.DurableEvent{Seq: seq, Kind: "turn/interrupted", Payload: json.RawMessage(`{"turn_id":"ui-active"}`)})
					seq++
					w.ActiveTurn("")
					submission.Result <- nil
					continue
				}
				seed(seq, submission.Text)
				seq++
				if submission.Text == "测试长任务" {
					body, _ := json.Marshal(map[string]string{"turn_id": "ui-active", "client_message_id": fmt.Sprint(seq - 1)})
					w.Event(room.DurableEvent{Seq: seq, Kind: "turn/running", ActorUID: 1001, Payload: body})
					seq++
					w.ActiveTurn("ui-active")
				}
				submission.Result <- nil
			}
		}
	}
	server, err := dashboard.Start(ctx, catalog, connector)
	if err != nil {
		panic(err)
	}
	defer server.Close()
	fmt.Println(server.URL())
	<-ctx.Done()
}
