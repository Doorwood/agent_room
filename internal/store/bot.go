package store

import (
	"agent_romm/internal/room"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// BotMember requires an active network membership; the configured Host owner
// is also eligible. Historical/revoked member rows alone never grant access.
func (s *Store) BotMember(ctx context.Context, rid room.RoomID, uid room.UID, work bool) (room.Member, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM rooms r WHERE r.id=? AND (r.execution_owner_uid=? OR EXISTS(SELECT 1 FROM join_requests j WHERE j.room_id=r.id AND j.uid=? AND j.state='approved'))`, rid, uid, uid).Scan(&n)
	if err != nil || n != 1 {
		return room.Member{}, errors.New("bot sender is not an active member")
	}
	role, err := s.MemberRole(ctx, rid, uid)
	if err != nil || (work && role != "roommate") {
		return room.Member{}, errors.New("bot sender cannot arrange work")
	}
	return s.FindMember(ctx, rid, uid)
}
func (s *Store) BotHistory(ctx context.Context, rid room.RoomID, query string) (string, error) {
	if len(query) > 200 {
		return "", errors.New("query too long")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq,kind,payload,created_at,actor_uid FROM room_events WHERE room_id=? AND kind IN ('message/accepted','note/accepted','item/completed') ORDER BY seq DESC LIMIT 200`, rid)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	lines := []string{}
	used := 0
	for rows.Next() {
		var seq int64
		var kind, stamp string
		var payload []byte
		var uid uint32
		if err := rows.Scan(&seq, &kind, &payload, &stamp, &uid); err != nil {
			return "", err
		}
		var v struct {
			Body    string `json:"body"`
			Text    string `json:"text"`
			Payload struct {
				Type  string `json:"type"`
				Phase string `json:"phase"`
				Text  string `json:"text"`
			} `json:"payload"`
		}
		if json.Unmarshal(payload, &v) != nil {
			continue
		}
		text := v.Body
		author := fmt.Sprintf("成员 %d", uid)
		if kind == "item/completed" {
			if v.Payload.Type != "agentMessage" || (v.Payload.Phase != "" && v.Payload.Phase != "final_answer") {
				continue
			}
			text = v.Payload.Text
			author = "Codex Agent"
		}
		if text == "" || !strings.Contains(strings.ToLower(text), strings.ToLower(query)) {
			continue
		}
		if len([]rune(text)) > 1200 {
			text = string([]rune(text)[:1200]) + "…"
		}
		line := fmt.Sprintf("#%d %s %s\n%s", seq, stamp, author, text)
		if used+len(line) > 16000 {
			break
		}
		used += len(line)
		lines = append(lines, line)
		if len(lines) == 20 {
			break
		}
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	if len(lines) == 0 {
		return "最近 200 条候选事件中没有匹配消息。", rows.Err()
	}
	return "最近主聊天记录（最多 20 条；不含私人资源与询问者记录）：\n" + strings.Join(lines, "\n\n"), rows.Err()
}
