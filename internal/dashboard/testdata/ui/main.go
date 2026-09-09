// Test-only UI fixture. It does not start Codex or execute submitted instructions.
package main

import (
	"agent_romm/internal/answerwindow"
	"agent_romm/internal/dashboard"
	"agent_romm/internal/hostview"
	"agent_romm/internal/network"
	"agent_romm/internal/personal"
	"agent_romm/internal/questions"
	"agent_romm/internal/resources"
	"agent_romm/internal/room"
	"agent_romm/internal/store"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	fakeBin := filepath.Join(dir, "fake-bin")
	os.MkdirAll(fakeBin, 0700)
	fakeCLI := `#!/bin/sh
if [ "$1" = "whoami" ]; then
 echo '{"identity":"user","available":true,"tokenStatus":"ready","onBehalfOf":{"userName":"Browser User","openId":"ou_browser123456"}}'
elif [ "$1" = "docs" ] && [ "$2" = "+create" ]; then
 cat >/dev/null
 echo '{"ok":true,"identity":"user","data":{"document":{"document_id":"NaturalDoc123456","url":"https://example.feishu.cn/docx/NaturalDoc123456"}}}'
else
 exit 1
fi
`
	os.WriteFile(filepath.Join(fakeBin, "lark-cli"), []byte(fakeCLI), 0700)
	os.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
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
		broker := personal.New(nil)
		broker.SetMode("personal")
		defer broker.Invalidate()
		docResults := make(chan personal.Result, 1)
		commitResults := make(chan personal.CommitReceipt, 1)
		gitRoot := ""
		fixtureGit := func(args ...string) string {
			cmd := exec.Command("git", append([]string{"-C", gitRoot, "-c", "user.name=Fixture Host", "-c", "user.email=fixture-host@example.test", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
			b, e := cmd.CombinedOutput()
			if e != nil {
				panic(string(b))
			}
			return strings.TrimSpace(string(b))
		}
		if r.Name == "git-sender" {
			gitRoot, _ = os.MkdirTemp(dir, "git-fixture-")
			fixtureGit("init")
			os.WriteFile(filepath.Join(gitRoot, "code.go"), []byte("initial\n"), 0600)
			fixtureGit("add", "code.go")
			fixtureGit("commit", "-m", "initial")
		}

		w.EnableResources(func(ctx context.Context, req network.ResourceRequest, execute resources.Execute) (network.ResourceReply, error) {
			role := "roommate"
			if r.Name == "visitor" || r.Name == "asker" {
				role = r.Name
			}
			if req.Action == "personal-pending" || req.Action == "personal-result" {
				if role != "roommate" {
					return network.ResourceReply{}, fmt.Errorf("role denied")
				}
				reply := network.ResourceReply{Type: "done", Mode: "personal", Role: role}
				if req.Action == "personal-pending" {
					var err error
					reply.Personal, err = broker.Next(ctx, uid, req.ClientID)
					return reply, err
				}
				return reply, broker.Resolve(ctx, uid, req.ClientID, *req.PersonalResult)
			}
			if req.Action == "info" {
				return network.ResourceReply{Type: "info", Mode: "personal", Role: role}, nil
			}
			if role == "visitor" {
				return network.ResourceReply{}, fmt.Errorf("visitor denied")
			}
			if req.Resource.IsWrite() {
				if role != "roommate" {
					return network.ResourceReply{}, fmt.Errorf("write denied")
				}
				b, _ := json.Marshal(resources.CreateReceipt{RequestID: req.Resource.RequestID, Title: req.Resource.Title, Account: resources.FeishuIdentity{Name: "Browser User", OpenID: req.Resource.AccountID}, State: "completed", URL: "https://example.feishu.cn/docx/BrowserDoc123"})
				return network.ResourceReply{Type: "done", Mode: "personal", Role: role, Text: string(b)}, nil
			}
			if req.Resource.Action == "project.file" {
				text, err := execute(ctx, req.Resource)
				return network.ResourceReply{Type: "done", Mode: "personal", Role: role, Text: text, Answer: "资源回答：" + text}, err
			}
			return network.ResourceReply{Type: "done", Mode: "personal", Role: role, Text: "private evidence for " + r.Name, Answer: "独立资源回答：" + req.Question}, nil
		})

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
		w.Project("/demo/demo-project")
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
			case result := <-commitResults:
				text := "提交未完成：" + result.Error
				if result.State == "completed" {
					text = "已提交，Author/Committer：" + result.Author.Name + " <" + result.Author.Email + ">，commit：" + result.Commit
				}
				w.Add(room.DurableEvent{Seq: seq, CreatedAt: time.Now()}, text)
				seq++
			case result := <-docResults:
				text := "个人创建未完成：" + result.Error
				if result.Receipt != nil {
					text = "已使用 " + result.Receipt.Account.Name + " 的个人权限创建：[打开飞书文档](" + result.Receipt.URL + ")"
				}
				w.Add(room.DurableEvent{Seq: seq, CreatedAt: time.Now()}, text)
				seq++
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
				if strings.HasPrefix(submission.Text, "创建一个") && strings.Contains(submission.Text, "飞书文档") {
					cap, _ := broker.Bind(room.ClientMessageID(submission.ID), room.Actor{UID: uid, Name: r.Name})
					title := submission.Text
					go func() {
						result, err := broker.Call(ctx, cap, title, "模型根据用户要求准备的完整项目规划正文。\n不会在 Host 上创建。")
						if err == nil {
							select {
							case docResults <- result:
							case <-ctx.Done():
							}
						}
					}()
				}
				if gitRoot != "" && strings.Contains(submission.Text, "并提交") {
					head := fixtureGit("rev-parse", "HEAD")
					os.WriteFile(filepath.Join(gitRoot, "code.go"), []byte(fmt.Sprintf("fixture change %d\n", seq)), 0600)
					cap, _ := broker.Bind(room.ClientMessageID(submission.ID), room.Actor{UID: uid, Name: r.Name})
					in := personal.CommitInput{Message: submission.Text, Paths: []string{"code.go"}, ExpectedHead: head}
					go func() {
						result, e := broker.Commit(ctx, cap, gitRoot, filepath.Join(gitRoot, ".git", "personal-receipts"), in)
						if e != nil {
							result.Error = e.Error()
						}
						select {
						case commitResults <- result:
						case <-ctx.Done():
						}
					}()
				}
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
	portal, err := hostview.Start("127.0.0.1:0", hostview.Info{Project: "demo-project", Address: "127.0.0.1:7443", Session: session})
	if err != nil {
		panic(err)
	}
	defer portal.Close()
	fmt.Fprintln(os.Stderr, "HOST_WEB_URL=http://"+portal.Address()+"/")
	fmt.Println(server.URL())
	<-ctx.Done()
}
