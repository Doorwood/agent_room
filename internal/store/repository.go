package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"agent_romm/internal/protocol"
	"agent_romm/internal/room"
)

type RoomSeed struct {
	ID                room.RoomID
	DisplayName       string
	HostID            string
	ProjectRoot       string
	ExecutionOwnerUID room.UID
	Members           []room.Member
}

type ConnectionStore interface {
	Connected(context.Context, room.ConnectionRecord) error
	Ack(context.Context, room.ConnectionID, room.Seq) error
	Disconnected(context.Context, room.ConnectionID, time.Time) error
	CloseStale(context.Context, room.RoomID, time.Time) error
}

var (
	_ room.Repository = (*Store)(nil)
	_ ConnectionStore = (*Store)(nil)
)

var normalizedCodePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)

type storedMessage struct {
	id          int64
	kind        string
	actorUID    room.UID
	body        string
	payloadHash []byte
	state       room.RequestState
	acceptedSeq room.Seq
	errorCode   string
}

func (s *Store) InitializeRoom(ctx context.Context, seed RoomSeed) error {
	if err := validateRoomSeed(seed); err != nil {
		return err
	}
	now := encodeTime(nowUTC())
	return s.transact(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM rooms").Scan(&count); err != nil {
			return fmt.Errorf("count initialized rooms: %w", err)
		}
		if count != 0 {
			return ErrAlreadyInitialized
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO rooms(id, display_name, host_id, project_path, execution_owner_uid, status, schema_version)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, seed.ID, seed.DisplayName, seed.HostID, seed.ProjectRoot, seed.ExecutionOwnerUID, room.RoomReady, schemaVersion); err != nil {
			return fmt.Errorf("insert room: %w", err)
		}
		for _, member := range seed.Members {
			if _, err := tx.ExecContext(ctx, "INSERT INTO members(room_id, uid, username, added_at) VALUES (?, ?, ?, ?)", seed.ID, member.UID, member.Name, now); err != nil {
				return fmt.Errorf("insert room member: %w", err)
			}
		}
		return nil
	})
}

func validateRoomSeed(seed RoomSeed) error {
	if strings.TrimSpace(string(seed.ID)) == "" || strings.TrimSpace(seed.DisplayName) == "" || strings.TrimSpace(seed.HostID) == "" || strings.TrimSpace(seed.ProjectRoot) == "" || seed.ExecutionOwnerUID == 0 || len(seed.Members) == 0 {
		return fmt.Errorf("%w: incomplete room seed", ErrInvalidInput)
	}
	uids := make(map[room.UID]struct{}, len(seed.Members))
	names := make(map[string]struct{}, len(seed.Members))
	ownerPresent := false
	for _, member := range seed.Members {
		if member.UID == 0 || strings.TrimSpace(member.Name) == "" || strings.TrimSpace(member.Name) != member.Name {
			return fmt.Errorf("%w: invalid member", ErrInvalidInput)
		}
		if _, found := uids[member.UID]; found {
			return fmt.Errorf("%w: duplicate member uid", ErrInvalidInput)
		}
		if _, found := names[member.Name]; found {
			return fmt.Errorf("%w: duplicate member name", ErrInvalidInput)
		}
		uids[member.UID] = struct{}{}
		names[member.Name] = struct{}{}
		ownerPresent = ownerPresent || member.UID == seed.ExecutionOwnerUID
	}
	if !ownerPresent {
		return fmt.Errorf("%w: execution owner is not a member", ErrInvalidInput)
	}
	return nil
}

func (s *Store) FindMember(ctx context.Context, roomID room.RoomID, uid room.UID) (room.Member, error) {
	if err := validateRoomID(roomID); err != nil {
		return room.Member{}, err
	}
	if uid == 0 {
		return room.Member{}, fmt.Errorf("%w: member uid is zero", ErrInvalidInput)
	}
	var member room.Member
	err := s.db.QueryRowContext(ctx, "SELECT uid, username FROM members WHERE room_id = ? AND uid = ?", roomID, uid).Scan(&member.UID, &member.Name)
	if errors.Is(err, sql.ErrNoRows) {
		if err := s.requireRoom(ctx, s.db, roomID); err != nil {
			return room.Member{}, err
		}
		return room.Member{}, ErrMemberNotFound
	}
	if err != nil {
		return room.Member{}, fmt.Errorf("find member: %w", err)
	}
	return member, nil
}

func (s *Store) AcceptMessage(ctx context.Context, roomID room.RoomID, actor room.Actor, input room.SubmitInput) (room.Acceptance, error) {
	if err := input.Validate(); err != nil {
		return room.Acceptance{}, err
	}
	bodyJSON, _ := json.Marshal(struct {
		Text   string `json:"text"`
		TaskID int64  `json:"taskId,omitempty"`
	}{Text: input.Text, TaskID: input.TaskID})
	return s.accept(ctx, roomID, actor, input.ClientMessageID, "prompt", input.Text, canonicalHash("prompt", actor.UID, bodyJSON), room.RequestQueued, true, "message/accepted", input.TaskID)
}

func (s *Store) AppendNote(ctx context.Context, roomID room.RoomID, actor room.Actor, input room.SubmitInput) (room.Acceptance, error) {
	if input.TaskID != 0 {
		return room.Acceptance{}, ErrInvalidInput
	}
	if err := input.Validate(); err != nil {
		return room.Acceptance{}, err
	}
	bodyJSON, _ := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: input.Text})
	return s.accept(ctx, roomID, actor, input.ClientMessageID, "note", input.Text, canonicalHash("note", actor.UID, bodyJSON), room.RequestCompleted, false, "note/accepted")
}

func (s *Store) AcceptControl(ctx context.Context, roomID room.RoomID, actor room.Actor, input room.ControlInput) (room.Acceptance, error) {
	if err := input.Validate(); err != nil {
		return room.Acceptance{}, err
	}
	bodyJSON, err := json.Marshal(struct {
		ExpectedTurnID room.TurnID `json:"expected_turn_id"`
		Text           string      `json:"text"`
	}{ExpectedTurnID: input.ExpectedTurnID, Text: input.Text})
	if err != nil {
		return room.Acceptance{}, fmt.Errorf("marshal control identity: %w", err)
	}
	return s.accept(ctx, roomID, actor, input.ClientMessageID, string(input.Kind), string(bodyJSON), canonicalHash(string(input.Kind), actor.UID, bodyJSON), room.RequestDispatching, true, "control/accepted")
}

func canonicalHash(kind string, actorUID room.UID, semanticBody []byte) []byte {
	encoded, err := json.Marshal(struct {
		Version  int             `json:"version"`
		Kind     string          `json:"kind"`
		ActorUID room.UID        `json:"actor_uid"`
		Body     json.RawMessage `json:"body"`
	}{Version: 1, Kind: kind, ActorUID: actorUID, Body: semanticBody})
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(encoded)
	return digest[:]
}

func (s *Store) accept(ctx context.Context, roomID room.RoomID, actor room.Actor, clientID room.ClientMessageID, kind, body string, payloadHash []byte, initial room.RequestState, binding bool, eventKind string, taskIDs ...int64) (room.Acceptance, error) {
	if err := validateRoomID(roomID); err != nil {
		return room.Acceptance{}, err
	}
	var acceptance room.Acceptance
	wrote := false
	err := s.transactConditional(ctx, func(tx *sql.Tx) error {
		existing, err := loadStoredMessage(ctx, tx, roomID, clientID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			if existing.kind != kind || existing.actorUID != actor.UID || !bytes.Equal(existing.payloadHash, payloadHash) {
				return ErrIdempotencyConflict
			}
			event, err := loadEventTx(ctx, tx, roomID, existing.acceptedSeq)
			if err != nil {
				return err
			}
			acceptance = room.Acceptance{MessageID: existing.id, ClientMessageID: clientID, Seq: existing.acceptedSeq, State: existing.state, ErrorCode: existing.errorCode, Duplicate: true, Event: event}
			return nil
		}
		if err := requireActor(ctx, tx, roomID, actor); err != nil {
			return err
		}
		wrote = true
		seq, err := allocateSeq(ctx, tx, roomID)
		if err != nil {
			return err
		}
		now := nowUTC()
		result, err := tx.ExecContext(ctx, `
			INSERT INTO messages(room_id, client_message_id, actor_uid, kind, body, payload_hash, state, accepted_seq, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, roomID, clientID, actor.UID, kind, body, payloadHash, initial, seq, encodeTime(now))
		if err != nil {
			return fmt.Errorf("insert message: %w", err)
		}
		messageID, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("read inserted message id: %w", err)
		}
		if len(taskIDs) > 0 && taskIDs[0] > 0 {
			if err := attachTaskMessage(ctx, tx, roomID, actor, taskIDs[0], messageID); err != nil {
				return err
			}
		}
		if binding {
			var turnID any
			if kind == string(room.ControlSteer) || kind == string(room.ControlCancel) {
				var control struct {
					ExpectedTurnID room.TurnID `json:"expected_turn_id"`
				}
				if err := json.Unmarshal([]byte(body), &control); err != nil {
					return fmt.Errorf("decode canonical control: %w", err)
				}
				turnID = control.ExpectedTurnID
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO turn_bindings(room_id, message_id, codex_turn_id, state) VALUES (?, ?, ?, ?)", roomID, messageID, turnID, initial); err != nil {
				return fmt.Errorf("insert turn binding: %w", err)
			}
		}
		payload := struct {
			ClientMessageID room.ClientMessageID `json:"client_message_id"`
			MessageID       int64                `json:"message_id"`
			Kind            string               `json:"kind"`
			Body            string               `json:"body"`
			State           room.RequestState    `json:"state"`
		}{clientID, messageID, kind, body, initial}
		event, err := insertEventAt(ctx, tx, roomID, seq, actor.UID, eventKind, payload, now)
		if err != nil {
			return err
		}
		acceptance = room.Acceptance{MessageID: messageID, ClientMessageID: clientID, Seq: seq, State: initial, Event: event}
		return nil
	}, func() bool { return wrote })
	return acceptance, err
}

func loadStoredMessage(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, roomID room.RoomID, clientID room.ClientMessageID) (storedMessage, error) {
	var result storedMessage
	var errorCode sql.NullString
	err := queryer.QueryRowContext(ctx, `
		SELECT m.id, m.kind, m.actor_uid, m.body, m.payload_hash, m.state, m.accepted_seq, b.error_code
		FROM messages m
		LEFT JOIN turn_bindings b ON b.room_id = m.room_id AND b.message_id = m.id
		WHERE m.room_id = ? AND m.client_message_id = ?`, roomID, clientID).Scan(
		&result.id, &result.kind, &result.actorUID, &result.body, &result.payloadHash, &result.state, &result.acceptedSeq, &errorCode,
	)
	if err != nil {
		return storedMessage{}, err
	}
	if errorCode.Valid {
		result.errorCode = errorCode.String
	}
	return result, nil
}

func (s *Store) BeginDispatch(ctx context.Context, roomID room.RoomID, clientID room.ClientMessageID) error {
	if err := validateIdentifiers(roomID, clientID); err != nil {
		return err
	}
	return s.transact(ctx, func(tx *sql.Tx) error {
		message, err := requireMessageState(ctx, tx, roomID, clientID, []string{"prompt", "recovery-prompt"}, room.RequestQueued)
		if err != nil {
			return err
		}
		return updateMessageAndBinding(ctx, tx, roomID, message.id, room.RequestQueued, room.RequestDispatching, nil, nil, nil)
	})
}

func (s *Store) FinishControl(ctx context.Context, roomID room.RoomID, clientID room.ClientMessageID, outcome room.ControlOutcome) (room.DurableEvent, error) {
	if err := validateIdentifiers(roomID, clientID); err != nil {
		return room.DurableEvent{}, err
	}
	if err := validateTerminalOutcome(outcome.State, outcome.ErrorCode, outcome.ErrorDigest, true); err != nil {
		return room.DurableEvent{}, err
	}
	var event room.DurableEvent
	err := s.transact(ctx, func(tx *sql.Tx) error {
		message, err := requireMessageState(ctx, tx, roomID, clientID, []string{"steer", "cancel"}, room.RequestDispatching)
		if err != nil {
			return err
		}
		now := nowUTC()
		completedAt := nullableCompletedAt(outcome.State, now)
		if err := updateMessageAndBinding(ctx, tx, roomID, message.id, room.RequestDispatching, outcome.State, completedAt, nullableString(outcome.ErrorCode), nullableString(outcome.ErrorDigest)); err != nil {
			return err
		}
		event, err = appendEvent(ctx, tx, roomID, message.actorUID, "control/"+string(outcome.State), struct {
			ClientMessageID room.ClientMessageID `json:"client_message_id"`
			State           room.RequestState    `json:"state"`
			ErrorCode       string               `json:"error_code,omitempty"`
			ErrorDigest     string               `json:"error_digest,omitempty"`
		}{clientID, outcome.State, outcome.ErrorCode, outcome.ErrorDigest})
		return err
	})
	return event, err
}

func (s *Store) FailDispatch(ctx context.Context, roomID room.RoomID, clientID room.ClientMessageID, outcome room.FailureOutcome) (room.DurableEvent, error) {
	if err := validateIdentifiers(roomID, clientID); err != nil {
		return room.DurableEvent{}, err
	}
	if err := validateTerminalOutcome(outcome.State, outcome.ErrorCode, outcome.ErrorDigest, false); err != nil {
		return room.DurableEvent{}, err
	}
	var event room.DurableEvent
	err := s.transact(ctx, func(tx *sql.Tx) error {
		message, err := requireMessageState(ctx, tx, roomID, clientID, []string{"prompt", "recovery-prompt"}, room.RequestDispatching)
		if err != nil {
			return err
		}
		now := nowUTC()
		if err := updateMessageAndBinding(ctx, tx, roomID, message.id, room.RequestDispatching, outcome.State, nullableCompletedAt(outcome.State, now), nullableString(outcome.ErrorCode), nullableString(outcome.ErrorDigest)); err != nil {
			return err
		}
		event, err = appendEvent(ctx, tx, roomID, message.actorUID, "message/"+string(outcome.State), struct {
			ClientMessageID room.ClientMessageID `json:"client_message_id"`
			State           room.RequestState    `json:"state"`
			ErrorCode       string               `json:"error_code"`
			ErrorDigest     string               `json:"error_digest,omitempty"`
		}{clientID, outcome.State, outcome.ErrorCode, outcome.ErrorDigest})
		return err
	})
	return event, err
}

func (s *Store) BindRunningTurn(ctx context.Context, roomID room.RoomID, clientID room.ClientMessageID, turnID room.TurnID) (room.DurableEvent, error) {
	if err := validateIdentifiers(roomID, clientID); err != nil {
		return room.DurableEvent{}, err
	}
	if strings.TrimSpace(string(turnID)) == "" {
		return room.DurableEvent{}, fmt.Errorf("%w: turn id is blank", ErrInvalidInput)
	}
	var event room.DurableEvent
	err := s.transact(ctx, func(tx *sql.Tx) error {
		message, err := requireMessageState(ctx, tx, roomID, clientID, []string{"prompt", "recovery-prompt"}, room.RequestDispatching)
		if err != nil {
			return err
		}
		now := nowUTC()
		turn := string(turnID)
		if err := updateMessageAndBinding(ctx, tx, roomID, message.id, room.RequestDispatching, room.RequestRunning, nil, nil, nil, withTurnID(turn), withStartedAt(encodeTime(now))); err != nil {
			return err
		}
		event, err = appendEvent(ctx, tx, roomID, message.actorUID, "turn/running", struct {
			ClientMessageID room.ClientMessageID `json:"client_message_id"`
			TurnID          room.TurnID          `json:"turn_id"`
		}{clientID, turnID})
		return err
	})
	return event, err
}

func (s *Store) RecordCompletedItem(ctx context.Context, roomID room.RoomID, item room.CompletedItem) (room.DurableEvent, error) {
	if err := validateRoomID(roomID); err != nil {
		return room.DurableEvent{}, err
	}
	if strings.TrimSpace(string(item.ThreadID)) == "" || strings.TrimSpace(string(item.TurnID)) == "" || strings.TrimSpace(string(item.ItemID)) == "" || !json.Valid(item.Payload) {
		return room.DurableEvent{}, fmt.Errorf("%w: invalid completed item", ErrInvalidInput)
	}
	// Reserve space for identifiers, durable metadata and the framed envelope.
	// Marshal now: RawMessage may grow sixfold when HTML-safe JSON is encoded.
	for _, id := range []string{string(item.ThreadID), string(item.TurnID), string(item.ItemID)} {
		if len(id) > 4096 {
			return room.DurableEvent{}, ErrInvalidInput
		}
	}
	payloadCopy, err := json.Marshal(item.Payload)
	if err != nil {
		return room.DurableEvent{}, err
	}
	if len(payloadCopy) > int(protocol.MaxFrameBytes)-(128<<10) {
		digest := sha256.Sum256(payloadCopy)
		payloadCopy, err = json.Marshal(struct {
			Type          string `json:"type"`
			OriginalBytes int    `json:"originalBytes"`
			SHA256        string `json:"sha256"`
		}{"output-omitted", len(item.Payload), fmt.Sprintf("%x", digest)})
		if err != nil {
			return room.DurableEvent{}, err
		}
	}
	var event room.DurableEvent
	wrote := false
	err = s.transactConditional(ctx, func(tx *sql.Tx) error {
		var thread sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT codex_thread_id FROM rooms WHERE id = ?", roomID).Scan(&thread); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrRoomNotFound
			}
			return err
		}
		if !thread.Valid || thread.String != string(item.ThreadID) {
			return ErrInvalidState
		}
		// Completed history is immutable, including turns whose start reply was
		// lost before a binding existed. The current thread is the authority.
		var created string
		err := tx.QueryRowContext(ctx, `
			SELECT seq, kind, actor_uid, payload, created_at FROM room_events
			WHERE room_id = ? AND kind = 'item/completed'
			AND json_extract(payload, '$.thread_id') = ?
			AND json_extract(payload, '$.turn_id') = ?
			AND json_extract(payload, '$.item_id') = ? ORDER BY seq LIMIT 1`,
			roomID, item.ThreadID, item.TurnID, item.ItemID).Scan(&event.Seq, &event.Kind, &event.ActorUID, &event.Payload, &created)
		if err == nil {
			var stored struct {
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(event.Payload, &stored); err != nil {
				return err
			}
			var oldJSON, newJSON bytes.Buffer
			if err := json.Compact(&oldJSON, stored.Payload); err != nil {
				return err
			}
			if err := json.Compact(&newJSON, payloadCopy); err != nil {
				return err
			}
			if !bytes.Equal(oldJSON.Bytes(), newJSON.Bytes()) {
				var oldMarker, newMarker struct {
					Type   string `json:"type"`
					SHA256 string `json:"sha256"`
				}
				_ = json.Unmarshal(oldJSON.Bytes(), &oldMarker)
				_ = json.Unmarshal(newJSON.Bytes(), &newMarker)
				// Preserve the first observed upstream byte count while allowing
				// equivalent notification/read JSON with different whitespace.
				if oldMarker.Type != "output-omitted" || newMarker.Type != "output-omitted" || oldMarker.SHA256 == "" || oldMarker.SHA256 != newMarker.SHA256 {
					return ErrInvalidState
				}
			}
			event.CreatedAt, err = decodeTime(created)
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		wrote = true
		eventPayload := struct {
			ThreadID room.ThreadID   `json:"thread_id"`
			TurnID   room.TurnID     `json:"turn_id"`
			ItemID   room.ItemID     `json:"item_id"`
			Payload  json.RawMessage `json:"payload"`
		}{item.ThreadID, item.TurnID, item.ItemID, payloadCopy}
		event, err = appendEvent(ctx, tx, roomID, 0, "item/completed", eventPayload)
		return err
	}, func() bool { return wrote })
	return event, err
}

func (s *Store) FinishTurn(ctx context.Context, roomID room.RoomID, input room.FinishTurnInput) ([]room.DurableEvent, error) {
	if err := validateRoomID(roomID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(input.TurnID)) == "" {
		return nil, fmt.Errorf("%w: turn id is blank", ErrInvalidInput)
	}
	if err := validateFinishTurn(input); err != nil {
		return nil, err
	}
	var events []room.DurableEvent
	err := s.transact(ctx, func(tx *sql.Tx) error {
		var message storedMessage
		err := tx.QueryRowContext(ctx, `
			SELECT m.id, m.client_message_id, m.actor_uid, m.state
			FROM messages m JOIN turn_bindings b ON b.message_id = m.id AND b.room_id = m.room_id
			WHERE m.room_id = ? AND m.kind IN ('prompt','recovery-prompt') AND m.state IN (?, ?) AND b.state = m.state AND b.codex_turn_id = ?`, roomID, room.RequestRunning, room.RequestNeedsReview, input.TurnID).Scan(&message.id, new(room.ClientMessageID), &message.actorUID, &message.state)
		if errors.Is(err, sql.ErrNoRows) {
			if err := requireRoom(ctx, tx, roomID); err != nil {
				return err
			}
			return ErrInvalidState
		}
		if err != nil {
			return fmt.Errorf("find running turn: %w", err)
		}
		now := nowUTC()
		if err := updateMessageAndBinding(ctx, tx, roomID, message.id, message.state, input.State, nullableCompletedAt(input.State, now), nullableString(input.ErrorCode), nullableString(input.ErrorDigest)); err != nil {
			return err
		}
		event, err := appendEvent(ctx, tx, roomID, message.actorUID, "turn/"+string(input.State), struct {
			TurnID      room.TurnID       `json:"turn_id"`
			State       room.RequestState `json:"state"`
			ErrorCode   string            `json:"error_code,omitempty"`
			ErrorDigest string            `json:"error_digest,omitempty"`
		}{input.TurnID, input.State, input.ErrorCode, input.ErrorDigest})
		if err != nil {
			return err
		}
		events = []room.DurableEvent{event}
		return nil
	})
	return events, err
}

func (s *Store) MarkNeedsReview(ctx context.Context, roomID room.RoomID, clientID room.ClientMessageID, reason room.ReviewReason) (room.DurableEvent, error) {
	if err := validateIdentifiers(roomID, clientID); err != nil {
		return room.DurableEvent{}, err
	}
	if err := validateReason(reason.Code, reason.DetailDigest); err != nil {
		return room.DurableEvent{}, err
	}
	var event room.DurableEvent
	err := s.transact(ctx, func(tx *sql.Tx) error {
		message, err := loadStoredMessage(ctx, tx, roomID, clientID)
		if errors.Is(err, sql.ErrNoRows) {
			if err := requireRoom(ctx, tx, roomID); err != nil {
				return err
			}
			return ErrMessageNotFound
		}
		if err != nil {
			return fmt.Errorf("load review target: %w", err)
		}
		if message.state != room.RequestDispatching && message.state != room.RequestRunning {
			return ErrInvalidState
		}
		if message.kind == "note" || message.kind == "recover" {
			return ErrInvalidState
		}
		if err := updateMessageAndBinding(ctx, tx, roomID, message.id, message.state, room.RequestNeedsReview, nil, nullableString(reason.Code), nullableString(reason.DetailDigest)); err != nil {
			return err
		}
		event, err = appendEvent(ctx, tx, roomID, message.actorUID, "message/needs-review", struct {
			ClientMessageID room.ClientMessageID `json:"client_message_id"`
			Code            string               `json:"code"`
			DetailDigest    string               `json:"detail_digest"`
		}{clientID, reason.Code, reason.DetailDigest})
		return err
	})
	return event, err
}

type bindingUpdate func(*bindingUpdateValues)

type bindingUpdateValues struct {
	turnID, startedAt *string
}

func withTurnID(value string) bindingUpdate {
	return func(values *bindingUpdateValues) { values.turnID = &value }
}
func withStartedAt(value string) bindingUpdate {
	return func(values *bindingUpdateValues) { values.startedAt = &value }
}

func updateMessageAndBinding(ctx context.Context, tx *sql.Tx, roomID room.RoomID, messageID int64, oldState, newState room.RequestState, completedAt, errorCode, errorDigest any, options ...bindingUpdate) error {
	result, err := tx.ExecContext(ctx, "UPDATE messages SET state = ? WHERE room_id = ? AND id = ? AND state = ?", newState, roomID, messageID, oldState)
	if err != nil {
		return fmt.Errorf("update message state: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return err
	}
	values := bindingUpdateValues{}
	for _, option := range options {
		option(&values)
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE turn_bindings SET state = ?,
			codex_turn_id = COALESCE(?, codex_turn_id),
			started_at = COALESCE(?, started_at),
			completed_at = ?, error_code = ?, error_digest = ?
		WHERE room_id = ? AND message_id = ? AND state = ?`, newState, values.turnID, values.startedAt, completedAt, errorCode, errorDigest, roomID, messageID, oldState)
	if err != nil {
		return fmt.Errorf("update turn binding state: %w", err)
	}
	return requireOneRow(result)
}

func requireMessageState(ctx context.Context, tx *sql.Tx, roomID room.RoomID, clientID room.ClientMessageID, kinds []string, expected room.RequestState) (storedMessage, error) {
	message, err := loadStoredMessage(ctx, tx, roomID, clientID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := requireRoom(ctx, tx, roomID); err != nil {
			return storedMessage{}, err
		}
		return storedMessage{}, ErrMessageNotFound
	}
	if err != nil {
		return storedMessage{}, fmt.Errorf("load state transition target: %w", err)
	}
	if message.state != expected || !contains(kinds, message.kind) {
		return storedMessage{}, ErrInvalidState
	}
	return message, nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func requireOneRow(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected row count: %w", err)
	}
	if count != 1 {
		return ErrInvalidState
	}
	return nil
}

func validateTerminalOutcome(state room.RequestState, code, digest string, controls bool) error {
	allowed := state == room.RequestFailed || state == room.RequestNeedsReview
	if controls {
		allowed = allowed || state == room.RequestCompleted
	} else {
		allowed = allowed || state == room.RequestInterrupted
	}
	if !allowed {
		return fmt.Errorf("%w: invalid terminal state %q", ErrInvalidInput, state)
	}
	if state == room.RequestCompleted {
		if code != "" || digest != "" {
			return fmt.Errorf("%w: completed outcome contains diagnostic", ErrInvalidInput)
		}
		return nil
	}
	if !validErrorCode(code) || !validOptionalDigest(digest) {
		return fmt.Errorf("%w: invalid normalized diagnostic", ErrInvalidInput)
	}
	return nil
}

func validateFinishTurn(input room.FinishTurnInput) error {
	if input.State != room.RequestCompleted && input.State != room.RequestFailed && input.State != room.RequestInterrupted {
		return fmt.Errorf("%w: invalid finish state", ErrInvalidInput)
	}
	if input.State == room.RequestCompleted {
		if input.ErrorCode != "" || input.ErrorDigest != "" {
			return fmt.Errorf("%w: completed turn contains diagnostic", ErrInvalidInput)
		}
		return nil
	}
	if !validErrorCode(input.ErrorCode) || !validOptionalDigest(input.ErrorDigest) {
		return fmt.Errorf("%w: invalid normalized diagnostic", ErrInvalidInput)
	}
	return nil
}

func validateReason(code, digest string) error {
	if !validErrorCode(code) || !validDigest(digest) {
		return fmt.Errorf("%w: invalid reason", ErrInvalidInput)
	}
	return nil
}

func validErrorCode(value string) bool {
	return len(value) <= 64 && normalizedCodePattern.MatchString(value)
}

func validOptionalDigest(value string) bool { return value == "" || validDigest(value) }

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range []byte(value) {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableCompletedAt(state room.RequestState, now time.Time) any {
	if state == room.RequestNeedsReview {
		return nil
	}
	return encodeTime(now)
}

func allocateSeq(ctx context.Context, tx *sql.Tx, roomID room.RoomID) (room.Seq, error) {
	var seq room.Seq
	err := tx.QueryRowContext(ctx, "UPDATE rooms SET next_seq = next_seq + 1 WHERE id = ? RETURNING next_seq - 1", roomID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrRoomNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("allocate room sequence: %w", err)
	}
	return seq, nil
}

func appendEvent(ctx context.Context, tx *sql.Tx, roomID room.RoomID, actorUID room.UID, kind string, payload any) (room.DurableEvent, error) {
	seq, err := allocateSeq(ctx, tx, roomID)
	if err != nil {
		return room.DurableEvent{}, err
	}
	return insertEventAt(ctx, tx, roomID, seq, actorUID, kind, payload, nowUTC())
}

func insertEventAt(ctx context.Context, tx *sql.Tx, roomID room.RoomID, seq room.Seq, actorUID room.UID, kind string, payload any, createdAt time.Time) (room.DurableEvent, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return room.DurableEvent{}, fmt.Errorf("marshal durable event: %w", err)
	}
	encoded = append([]byte(nil), encoded...)
	if _, err := tx.ExecContext(ctx, "INSERT INTO room_events(room_id, seq, actor_uid, kind, payload, created_at) VALUES (?, ?, ?, ?, ?, ?)", roomID, seq, actorUID, kind, encoded, encodeTime(createdAt)); err != nil {
		return room.DurableEvent{}, fmt.Errorf("insert durable event: %w", err)
	}
	return room.DurableEvent{Seq: seq, Kind: kind, ActorUID: actorUID, Payload: encoded, CreatedAt: createdAt.UTC()}, nil
}

func loadEventTx(ctx context.Context, tx *sql.Tx, roomID room.RoomID, seq room.Seq) (room.DurableEvent, error) {
	var event room.DurableEvent
	var created string
	if err := tx.QueryRowContext(ctx, "SELECT seq, kind, actor_uid, payload, created_at FROM room_events WHERE room_id = ? AND seq = ?", roomID, seq).Scan(&event.Seq, &event.Kind, &event.ActorUID, &event.Payload, &created); err != nil {
		return room.DurableEvent{}, fmt.Errorf("load durable event: %w", err)
	}
	parsed, err := decodeTime(created)
	if err != nil {
		return room.DurableEvent{}, fmt.Errorf("decode durable event time: %w", err)
	}
	event.CreatedAt = parsed
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	return event, nil
}

func validateRoomID(roomID room.RoomID) error {
	if strings.TrimSpace(string(roomID)) == "" {
		return fmt.Errorf("%w: room id is blank", ErrInvalidInput)
	}
	return nil
}

func validateIdentifiers(roomID room.RoomID, clientID room.ClientMessageID) error {
	if err := validateRoomID(roomID); err != nil {
		return err
	}
	if !room.ValidClientMessageID(clientID) {
		return room.ErrInvalidClientMessageID
	}
	return nil
}

func requireActor(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, roomID room.RoomID, actor room.Actor) error {
	if actor.UID == 0 || strings.TrimSpace(actor.Name) == "" {
		return fmt.Errorf("%w: invalid actor", ErrInvalidInput)
	}
	var name string
	err := queryer.QueryRowContext(ctx, "SELECT username FROM members WHERE room_id = ? AND uid = ?", roomID, actor.UID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		if err := requireRoom(ctx, queryer, roomID); err != nil {
			return err
		}
		return ErrMemberNotFound
	}
	if err != nil {
		return fmt.Errorf("load actor membership: %w", err)
	}
	if name != actor.Name {
		return ErrMemberNotFound
	}
	return nil
}

func (s *Store) requireRoom(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, roomID room.RoomID) error {
	return requireRoom(ctx, queryer, roomID)
}

func requireRoom(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, roomID room.RoomID) error {
	var one int
	err := queryer.QueryRowContext(ctx, "SELECT 1 FROM rooms WHERE id = ?", roomID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrRoomNotFound
	}
	if err != nil {
		return fmt.Errorf("load room: %w", err)
	}
	return nil
}

func (s *Store) LatestSeq(ctx context.Context, roomID room.RoomID) (room.Seq, error) {
	if err := validateRoomID(roomID); err != nil {
		return 0, err
	}
	var next room.Seq
	err := s.db.QueryRowContext(ctx, "SELECT next_seq FROM rooms WHERE id = ?", roomID).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrRoomNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("load latest room sequence: %w", err)
	}
	return next - 1, nil
}

// Events returns events in (afterExclusive, throughInclusive], ordered by
// sequence. A throughInclusive value of zero means the latest durable event.
func (s *Store) Events(ctx context.Context, roomID room.RoomID, afterExclusive, throughInclusive room.Seq, limit int) ([]room.DurableEvent, error) {
	if err := validateRoomID(roomID); err != nil {
		return nil, err
	}
	if limit <= 0 || (throughInclusive != 0 && throughInclusive < afterExclusive) {
		return nil, fmt.Errorf("%w: invalid event range", ErrInvalidInput)
	}
	if err := s.requireRoom(ctx, s.db, roomID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT seq, kind, actor_uid, payload, created_at FROM room_events
		WHERE room_id = ? AND seq > ? AND (? = 0 OR seq <= ?)
		ORDER BY seq LIMIT ?`, roomID, afterExclusive, throughInclusive, throughInclusive, limit)
	if err != nil {
		return nil, fmt.Errorf("query durable events: %w", err)
	}
	defer rows.Close()
	events := make([]room.DurableEvent, 0)
	for rows.Next() {
		var event room.DurableEvent
		var created string
		if err := rows.Scan(&event.Seq, &event.Kind, &event.ActorUID, &event.Payload, &created); err != nil {
			return nil, fmt.Errorf("scan durable event: %w", err)
		}
		parsed, err := decodeTime(created)
		if err != nil {
			return nil, fmt.Errorf("decode durable event time: %w", err)
		}
		event.CreatedAt = parsed
		event.Payload = append(json.RawMessage(nil), event.Payload...)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate durable events: %w", err)
	}
	return events, nil
}

func (s *Store) Connected(ctx context.Context, record room.ConnectionRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	if err := validateRoomID(record.RoomID); err != nil {
		return err
	}
	if record.UID == 0 || record.ConnectedAt.IsZero() || (record.DisconnectedAt != nil && record.DisconnectedAt.Before(record.ConnectedAt)) {
		return fmt.Errorf("%w: invalid connection record", ErrInvalidInput)
	}
	return s.transact(ctx, func(tx *sql.Tx) error {
		if err := requireMemberUID(ctx, tx, record.RoomID, record.UID); err != nil {
			return err
		}
		var disconnected any
		if record.DisconnectedAt != nil {
			disconnected = encodeTime(*record.DisconnectedAt)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO client_connections(id, room_id, member_uid, connected_at, disconnected_at, last_ack_seq) VALUES (?, ?, ?, ?, ?, ?)`, record.ID, record.RoomID, record.UID, encodeTime(record.ConnectedAt), disconnected, record.LastAck)
		if err != nil {
			if isUniqueConstraint(err) {
				return ErrInvalidState
			}
			return fmt.Errorf("insert connection: %w", err)
		}
		return nil
	})
}

func requireMemberUID(ctx context.Context, tx *sql.Tx, roomID room.RoomID, uid room.UID) error {
	var one int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM members WHERE room_id = ? AND uid = ?", roomID, uid).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		if err := requireRoom(ctx, tx, roomID); err != nil {
			return err
		}
		return ErrMemberNotFound
	}
	if err != nil {
		return fmt.Errorf("load connection member: %w", err)
	}
	return nil
}

func (s *Store) Ack(ctx context.Context, connectionID room.ConnectionID, seq room.Seq) error {
	if !room.ValidConnectionID(connectionID) {
		return room.ErrInvalidConnectionID
	}
	return s.transact(ctx, func(tx *sql.Tx) error {
		var current room.Seq
		err := tx.QueryRowContext(ctx, "SELECT last_ack_seq FROM client_connections WHERE id = ?", connectionID).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConnectionNotFound
		}
		if err != nil {
			return fmt.Errorf("load connection ack: %w", err)
		}
		if seq < current {
			return ErrInvalidState
		}
		result, err := tx.ExecContext(ctx, "UPDATE client_connections SET last_ack_seq = ? WHERE id = ? AND last_ack_seq = ?", seq, connectionID, current)
		if err != nil {
			return fmt.Errorf("update connection ack: %w", err)
		}
		return requireOneRow(result)
	})
}

func (s *Store) Disconnected(ctx context.Context, connectionID room.ConnectionID, at time.Time) error {
	if !room.ValidConnectionID(connectionID) {
		return room.ErrInvalidConnectionID
	}
	if at.IsZero() {
		return fmt.Errorf("%w: disconnect time is zero", ErrInvalidInput)
	}
	return s.transact(ctx, func(tx *sql.Tx) error {
		var connectedAt string
		var disconnectedAt sql.NullString
		err := tx.QueryRowContext(ctx, "SELECT connected_at, disconnected_at FROM client_connections WHERE id = ?", connectionID).Scan(&connectedAt, &disconnectedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConnectionNotFound
		}
		if err != nil {
			return fmt.Errorf("load connection timestamps: %w", err)
		}
		if disconnectedAt.Valid {
			return ErrInvalidState
		}
		connected, err := decodeTime(connectedAt)
		if err != nil {
			return fmt.Errorf("decode connection time: %w", err)
		}
		if at.Before(connected) {
			return fmt.Errorf("%w: disconnect precedes connection", ErrInvalidInput)
		}
		result, err := tx.ExecContext(ctx, "UPDATE client_connections SET disconnected_at = ? WHERE id = ? AND connected_at = ? AND disconnected_at IS NULL", encodeTime(at), connectionID, connectedAt)
		if err != nil {
			return fmt.Errorf("disconnect connection: %w", err)
		}
		return requireOneRow(result)
	})
}

func (s *Store) CloseStale(ctx context.Context, roomID room.RoomID, at time.Time) error {
	if err := validateRoomID(roomID); err != nil {
		return err
	}
	if at.IsZero() {
		return fmt.Errorf("%w: stale close time is zero", ErrInvalidInput)
	}
	return s.transact(ctx, func(tx *sql.Tx) error {
		if err := requireRoom(ctx, tx, roomID); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT connected_at FROM client_connections WHERE room_id = ? AND disconnected_at IS NULL ORDER BY rowid", roomID)
		if err != nil {
			return fmt.Errorf("load stale connection times: %w", err)
		}
		corruptTime := false
		invalidChronology := false
		for rows.Next() {
			var encoded string
			if err := rows.Scan(&encoded); err != nil {
				corruptTime = true
				continue
			}
			connectedAt, err := decodeTime(encoded)
			if err != nil {
				corruptTime = true
				continue
			}
			if connectedAt.After(at) {
				invalidChronology = true
			}
		}
		closeErr := rows.Close()
		iterationErr := rows.Err()
		if corruptTime {
			return fmt.Errorf("%w: invalid connection timestamp", ErrCorruptStore)
		}
		if closeErr != nil {
			return fmt.Errorf("close stale connection times: %w", closeErr)
		}
		if iterationErr != nil {
			return fmt.Errorf("iterate stale connection times: %w", iterationErr)
		}
		if invalidChronology {
			return fmt.Errorf("%w: stale close precedes connection", ErrInvalidInput)
		}
		_, err = tx.ExecContext(ctx, "UPDATE client_connections SET disconnected_at = ? WHERE room_id = ? AND disconnected_at IS NULL", encodeTime(at), roomID)
		if err != nil {
			return fmt.Errorf("close stale connections: %w", err)
		}
		return nil
	})
}

func isUniqueConstraint(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "constraint failed")
}
