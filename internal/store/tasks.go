package store

import (
	"agent_romm/internal/room"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

type taskQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const taskColumns = `t.id,t.number,t.title,t.acceptance,t.source_seq,m.client_message_id,t.revision,t.created_at,t.updated_at,t.completed_at,t.completed_by`

func scanTask(row interface{ Scan(...any) error }) (room.ProjectTask, error) {
	var t room.ProjectTask
	err := row.Scan(&t.ID, &t.Number, &t.Title, &t.Acceptance, &t.SourceSeq, &t.SourceClientID, &t.Revision, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt, &t.CompletedBy)
	return t, err
}
func taskState(ctx context.Context, q taskQuerier, rid room.RoomID, id int64, completed bool) (string, error) {
	if completed {
		return "completed", nil
	}
	rows, err := q.QueryContext(ctx, `SELECT m.state FROM task_messages tm JOIN messages m ON m.room_id=tm.room_id AND m.id=tm.message_id WHERE tm.room_id=? AND tm.task_id=? ORDER BY m.id DESC`, rid, id)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	latest := ""
	running, queued, uncertain := false, false, false
	for rows.Next() {
		var state string
		if err = rows.Scan(&state); err != nil {
			return "", err
		}
		if latest == "" {
			latest = state
		}
		running = running || state == "running"
		queued = queued || state == "queued"
		uncertain = uncertain || state == "dispatching" || state == "needs-review"
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	switch {
	case uncertain:
		return "needs_confirmation", nil
	case running:
		return "running", nil
	case queued:
		return "queued", nil
	case latest == "completed":
		return "review", nil
	case latest != "":
		return "blocked", nil
	default:
		return "todo", nil
	}
}
func loadTask(ctx context.Context, q taskQuerier, rid room.RoomID, id int64) (room.ProjectTask, error) {
	t, err := scanTask(q.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM project_tasks t JOIN messages m ON m.room_id=t.room_id AND m.id=t.source_message_id WHERE t.room_id=? AND t.id=?`, rid, id))
	if err != nil {
		return t, err
	}
	t.State, err = taskState(ctx, q, rid, id, t.CompletedAt != "")
	return t, err
}
func requireTaskWriter(ctx context.Context, tx *sql.Tx, rid room.RoomID, actor room.Actor) error {
	if err := requireActor(ctx, tx, rid, actor); err != nil {
		return err
	}
	var role string
	if err := tx.QueryRowContext(ctx, `SELECT coalesce((SELECT role FROM member_roles WHERE room_id=? AND uid=?),'roommate')`, rid, actor.UID).Scan(&role); err != nil {
		return err
	}
	if role != "roommate" {
		return errors.New("此身份只能查看任务")
	}
	return nil
}
func (s *Store) Tasks(ctx context.Context, rid room.RoomID, req room.TaskRequest) (room.TaskReply, error) {
	reply := room.TaskReply{}
	if req.Action == "get" {
		// A read transaction keeps status, runs and outcomes at one consistent revision.
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return reply, err
		}
		defer tx.Rollback()
		t, err := loadTask(ctx, tx, rid, req.TaskID)
		if err != nil {
			return reply, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT CASE WHEN e.kind='item/completed' THEN coalesce(json_extract(e.payload,'$.payload.text'),m.body) ELSE m.body END FROM project_tasks t JOIN messages m ON m.room_id=t.room_id AND m.id=t.source_message_id LEFT JOIN room_events e ON e.room_id=t.room_id AND e.seq=t.source_seq WHERE t.room_id=? AND t.id=?`, rid, t.ID).Scan(&t.SourceText); err != nil {
			return reply, err
		}
		rows, err := tx.QueryContext(ctx, `SELECT m.id,m.client_message_id,coalesce(b.codex_turn_id,''),m.state,m.body,m.created_at,coalesce(b.started_at,''),coalesce(b.completed_at,'') FROM task_messages tm JOIN messages m ON m.room_id=tm.room_id AND m.id=tm.message_id LEFT JOIN turn_bindings b ON b.room_id=m.room_id AND b.message_id=m.id WHERE tm.room_id=? AND tm.task_id=? AND (?=0 OR m.id<?) ORDER BY m.id DESC LIMIT 11`, rid, t.ID, req.Before, req.Before)
		if err != nil {
			return reply, err
		}
		for rows.Next() {
			var r room.TaskRun
			if err = rows.Scan(&r.MessageID, &r.ClientID, &r.Turn, &r.State, &r.Text, &r.CreatedAt, &r.StartedAt, &r.CompletedAt); err != nil {
				rows.Close()
				return reply, err
			}
			t.Runs = append(t.Runs, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return reply, err
		}
		if len(t.Runs) > 10 {
			reply.More = true
			t.Runs = t.Runs[:10]
		}
		for i := range t.Runs {
			run := &t.Runs[i]
			if run.Turn == "" {
				continue
			}
			rows, err = tx.QueryContext(ctx, `SELECT coalesce(json_extract(payload,'$.payload.text'),''),created_at FROM room_events WHERE room_id=? AND kind='item/completed' AND json_extract(payload,'$.turn_id')=? AND json_extract(payload,'$.payload.type')='agentMessage' ORDER BY seq`, rid, run.Turn)
			if err != nil {
				return reply, err
			}
			total := 0
			for rows.Next() {
				var a room.TaskAnswer
				if err = rows.Scan(&a.Text, &a.Time); err != nil {
					rows.Close()
					return reply, err
				}
				total += len(a.Text)
				if total > 64<<10 || len(run.Answers) >= 100 {
					run.Answers = append(run.Answers, room.TaskAnswer{Text: "其余输出请在项目聊天历史中查看。"})
					break
				}
				run.Answers = append(run.Answers, a)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return reply, err
			}
		}
		rows, err = tx.QueryContext(ctx, `SELECT action,actor,created_at,note FROM task_changes WHERE room_id=? AND task_id=? ORDER BY id DESC LIMIT 50`, rid, t.ID)
		if err != nil {
			return reply, err
		}
		for rows.Next() {
			var c room.TaskChange
			if err = rows.Scan(&c.Action, &c.Actor, &c.Time, &c.Note); err != nil {
				rows.Close()
				return reply, err
			}
			t.History = append(t.History, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return reply, err
		}
		if err = tx.Commit(); err != nil {
			return reply, err
		}
		reply.Task = &t
		return reply, nil
	}
	if req.Action != "list" {
		return reply, errors.New("invalid task query")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM project_tasks t JOIN messages m ON m.room_id=t.room_id AND m.id=t.source_message_id WHERE t.room_id=? AND (?=0 OR t.id<?) ORDER BY t.id DESC LIMIT 51`, rid, req.Before, req.Before)
	if err != nil {
		return reply, err
	}
	for rows.Next() {
		t, e := scanTask(rows)
		if e != nil {
			rows.Close()
			return reply, e
		}
		reply.Tasks = append(reply.Tasks, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return reply, err
	}
	if len(reply.Tasks) > 50 {
		reply.More = true
		reply.Tasks = reply.Tasks[:50]
	}
	for i := range reply.Tasks {
		reply.Tasks[i].State, err = taskState(ctx, s.db, rid, reply.Tasks[i].ID, reply.Tasks[i].CompletedAt != "")
		if err != nil {
			return reply, err
		}
	}
	return reply, nil
}
func (s *Store) ChangeTask(ctx context.Context, rid room.RoomID, actor room.Actor, req room.TaskRequest) (room.ProjectTask, error) {
	var result room.ProjectTask
	raw, err := hex.DecodeString(req.RequestID)
	if err != nil || len(raw) != 16 || req.TaskID < 0 || req.SourceSeq < 0 || len(req.Title) > 240 || len(req.Acceptance) > 4000 || len(req.Note) > 2000 || !utf8.ValidString(req.Title) || !utf8.ValidString(req.Acceptance) || !utf8.ValidString(req.Note) {
		return result, errors.New("任务请求无效或内容过长")
	}
	if req.Action != "convert" && req.Action != "complete" && req.Action != "reopen" {
		return result, errors.New("invalid task action")
	}
	body, _ := json.Marshal(req)
	hash := sha256.Sum256(body)
	err = s.transact(ctx, func(tx *sql.Tx) error {
		if err := requireTaskWriter(ctx, tx, rid, actor); err != nil {
			return err
		}
		var oldHash []byte
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT task_id,payload_hash FROM task_receipts WHERE room_id=? AND uid=? AND request_id=?`, rid, actor.UID, req.RequestID).Scan(&id, &oldHash)
		if err == nil {
			if !bytes.Equal(hash[:], oldHash) {
				return ErrIdempotencyConflict
			}
			result, err = loadTask(ctx, tx, rid, id)
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		now := encodeTime(nowUTC())
		if req.Action == "convert" {
			if strings.TrimSpace(req.Title) == "" || req.SourceSeq <= 0 {
				return errors.New("请填写任务标题并选择来源消息")
			}
			var messageID int64
			var kind string
			err = tx.QueryRowContext(ctx, `SELECT id,kind FROM messages WHERE room_id=? AND accepted_seq=? AND kind IN ('prompt','note','recovery-prompt')`, rid, req.SourceSeq).Scan(&messageID, &kind)
			if errors.Is(err, sql.ErrNoRows) {
				err = tx.QueryRowContext(ctx, `SELECT m.id,m.kind FROM room_events e JOIN turn_bindings b ON b.room_id=e.room_id AND b.codex_turn_id=json_extract(e.payload,'$.turn_id') JOIN messages m ON m.room_id=b.room_id AND m.id=b.message_id WHERE e.room_id=? AND e.seq=? AND e.kind='item/completed' AND json_extract(e.payload,'$.payload.type')='agentMessage' AND m.kind IN ('prompt','recovery-prompt') ORDER BY m.id LIMIT 1`, rid, req.SourceSeq).Scan(&messageID, &kind)
			}
			if err != nil {
				return errors.New("来源消息不存在或不能转为任务")
			}
			err = tx.QueryRowContext(ctx, `SELECT task_id FROM task_messages WHERE room_id=? AND message_id=? UNION SELECT id FROM project_tasks WHERE room_id=? AND source_message_id=? LIMIT 1`, rid, messageID, rid, messageID).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				r, e := tx.ExecContext(ctx, `INSERT INTO project_tasks(room_id,number,source_message_id,source_seq,title,acceptance,created_at,updated_at) SELECT ?,coalesce(max(number),0)+1,?,?,?,?,?,? FROM project_tasks WHERE room_id=?`, rid, messageID, req.SourceSeq, strings.TrimSpace(req.Title), req.Acceptance, now, now, rid)
				if e != nil {
					return e
				}
				id, e = r.LastInsertId()
				if e != nil {
					return e
				}
				if kind != "note" {
					if _, e = tx.ExecContext(ctx, `INSERT INTO task_messages(room_id,task_id,message_id)VALUES(?,?,?)`, rid, id, messageID); e != nil {
						return e
					}
				}
				if _, e = tx.ExecContext(ctx, `INSERT INTO task_changes(room_id,task_id,action,actor,created_at,note)VALUES(?,?,'created',?,?,?)`, rid, id, actor.Name, now, req.Note); e != nil {
					return e
				}
			} else if err != nil {
				return err
			}
		} else {
			id = req.TaskID
			t, e := loadTask(ctx, tx, rid, id)
			if e != nil {
				return e
			}
			if t.Revision != req.ExpectedRevision {
				return errors.New("任务已更新，请刷新后操作")
			}
			completedAt, completedBy := "", ""
			if req.Action == "complete" {
				if t.State != "review" && t.State != "todo" {
					return errors.New("任务尚未结束或需要处理异常，不能标记完成")
				}
				completedAt, completedBy = now, actor.Name
			} else if t.State != "completed" {
				return errors.New("只有已完成任务可以重新打开")
			}
			if _, e = tx.ExecContext(ctx, `UPDATE project_tasks SET completed_at=?,completed_by=?,revision=revision+1,updated_at=? WHERE room_id=? AND id=?`, completedAt, completedBy, now, rid, id); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO task_changes(room_id,task_id,action,actor,created_at,note)VALUES(?,?,?,?,?,?)`, rid, id, req.Action, actor.Name, now, req.Note); e != nil {
				return e
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_receipts(room_id,uid,request_id,task_id,payload_hash)VALUES(?,?,?,?,?)`, rid, actor.UID, req.RequestID, id, hash[:]); err != nil {
			return err
		}
		result, err = loadTask(ctx, tx, rid, id)
		return err
	})
	return result, err
}

// Link the new execution in the same transaction that accepts its message.
func attachTaskMessage(ctx context.Context, tx *sql.Tx, rid room.RoomID, actor room.Actor, taskID, messageID int64) error {
	if err := requireTaskWriter(ctx, tx, rid, actor); err != nil {
		return err
	}
	t, err := loadTask(ctx, tx, rid, taskID)
	if err != nil {
		return errors.New("任务不存在")
	}
	if t.State == "completed" {
		return errors.New("请先重新打开已完成任务")
	}
	if t.State == "queued" || t.State == "running" || t.State == "needs_confirmation" {
		return errors.New("此任务仍在执行或等待确认，请等待结束后继续")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_messages(room_id,task_id,message_id)VALUES(?,?,?)`, rid, taskID, messageID); err != nil {
		return err
	}
	now := encodeTime(nowUTC())
	if _, err = tx.ExecContext(ctx, `UPDATE project_tasks SET revision=revision+1,updated_at=? WHERE room_id=? AND id=?`, now, rid, taskID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_changes(room_id,task_id,action,actor,created_at)VALUES(?,?,'run_added',?,?)`, rid, taskID, actor.Name, now)
	return err
}
