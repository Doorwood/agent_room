package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agent_romm/internal/room"
)

func (s *Store) LoadRecoveryImage(ctx context.Context, roomID room.RoomID) (room.RecoveryImage, error) {
	if err := validateRoomID(roomID); err != nil {
		return room.RecoveryImage{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return room.RecoveryImage{}, fmt.Errorf("begin recovery image transaction: %w", err)
	}
	defer tx.Rollback()
	var image room.RecoveryImage
	var threadID sql.NullString
	err = tx.QueryRowContext(ctx, "SELECT status, codex_thread_id FROM rooms WHERE id = ?", roomID).Scan(&image.Status, &threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return room.RecoveryImage{}, ErrRoomNotFound
	}
	if err != nil {
		return room.RecoveryImage{}, fmt.Errorf("load recovery room: %w", err)
	}
	if threadID.Valid {
		image.ThreadID = room.ThreadID(threadID.String)
	}

	var active room.TurnBinding
	var activeTurn sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT m.client_message_id, b.codex_turn_id, b.state
		FROM messages m JOIN turn_bindings b ON b.room_id = m.room_id AND b.message_id = m.id
		WHERE m.room_id = ? AND m.kind IN ('prompt','recovery-prompt')
		  AND m.state IN ('dispatching','running','needs-review') AND b.state = m.state
		ORDER BY m.accepted_seq LIMIT 1`, roomID).Scan(&active.MessageID, &activeTurn, &active.State)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return room.RecoveryImage{}, fmt.Errorf("load active binding: %w", err)
	}
	if err == nil {
		if activeTurn.Valid {
			active.TurnID = room.TurnID(activeTurn.String)
		}
		image.Active = &active
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT m.client_message_id, b.codex_turn_id, b.state
		FROM messages m JOIN turn_bindings b ON b.room_id = m.room_id AND b.message_id = m.id
		WHERE m.room_id = ? AND m.kind IN ('steer','cancel')
		  AND m.state IN ('dispatching','needs-review') AND b.state = m.state
		ORDER BY m.accepted_seq`, roomID)
	if err != nil {
		return room.RecoveryImage{}, fmt.Errorf("load pending controls: %w", err)
	}
	for rows.Next() {
		var control room.TurnBinding
		var expectedTurn sql.NullString
		if err := rows.Scan(&control.MessageID, &expectedTurn, &control.State); err != nil {
			rows.Close()
			return room.RecoveryImage{}, fmt.Errorf("scan pending control: %w", err)
		}
		if expectedTurn.Valid {
			control.TurnID = room.TurnID(expectedTurn.String)
		}
		image.PendingControls = append(image.PendingControls, control)
	}
	if err := rows.Close(); err != nil {
		return room.RecoveryImage{}, fmt.Errorf("close pending controls: %w", err)
	}
	if err := rows.Err(); err != nil {
		return room.RecoveryImage{}, fmt.Errorf("iterate pending controls: %w", err)
	}

	rows, err = tx.QueryContext(ctx, `
		SELECT m.client_message_id, m.body, m.actor_uid, members.username, m.accepted_seq
		FROM messages m JOIN members ON members.room_id = m.room_id AND members.uid = m.actor_uid
		WHERE m.room_id = ? AND m.kind IN ('prompt','recovery-prompt') AND m.state = 'queued'
		ORDER BY m.accepted_seq`, roomID)
	if err != nil {
		return room.RecoveryImage{}, fmt.Errorf("load recovery queue: %w", err)
	}
	for rows.Next() {
		var queued room.QueuedMessage
		if err := rows.Scan(&queued.Input.ClientMessageID, &queued.Input.Text, &queued.Actor.UID, &queued.Actor.Name, &queued.AcceptedSeq); err != nil {
			rows.Close()
			return room.RecoveryImage{}, fmt.Errorf("scan recovery queue: %w", err)
		}
		image.Queue = append(image.Queue, queued)
	}
	if err := rows.Close(); err != nil {
		return room.RecoveryImage{}, fmt.Errorf("close recovery queue: %w", err)
	}
	if err := rows.Err(); err != nil {
		return room.RecoveryImage{}, fmt.Errorf("iterate recovery queue: %w", err)
	}

	err = tx.QueryRowContext(ctx, `
		SELECT process_state, generation, pid, pgid, process_start, codex_version, schema_sha256
		FROM runtime_checkpoints WHERE room_id = ?`, roomID).Scan(
		&image.Checkpoint.State, &image.Checkpoint.Generation, &image.Checkpoint.PID, &image.Checkpoint.PGID,
		&image.Checkpoint.ProcessStart, &image.Checkpoint.CodexVersion, &image.Checkpoint.SchemaSHA256,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return room.RecoveryImage{}, fmt.Errorf("load runtime checkpoint: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return room.RecoveryImage{}, fmt.Errorf("commit recovery image transaction: %w", err)
	}
	return image, nil
}

func (s *Store) SaveRuntimeCheckpoint(ctx context.Context, roomID room.RoomID, checkpoint room.RuntimeCheckpoint) error {
	if err := validateRoomID(roomID); err != nil {
		return err
	}
	if err := validateCheckpoint(checkpoint); err != nil {
		return err
	}
	return s.transact(ctx, func(tx *sql.Tx) error {
		if err := requireRoom(ctx, tx, roomID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO runtime_checkpoints(room_id, generation, pid, pgid, process_start, codex_version, schema_sha256, process_state)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(room_id) DO UPDATE SET generation = excluded.generation, pid = excluded.pid,
				pgid = excluded.pgid, process_start = excluded.process_start, codex_version = excluded.codex_version,
				schema_sha256 = excluded.schema_sha256, process_state = excluded.process_state`, roomID,
			checkpoint.Generation, checkpoint.PID, checkpoint.PGID, checkpoint.ProcessStart,
			checkpoint.CodexVersion, checkpoint.SchemaSHA256, checkpoint.State)
		if err != nil {
			return fmt.Errorf("save runtime checkpoint: %w", err)
		}
		return nil
	})
}

func validateCheckpoint(checkpoint room.RuntimeCheckpoint) error {
	if checkpoint.State != room.RuntimeProcessRunning && checkpoint.State != room.RuntimeProcessStopped {
		return fmt.Errorf("%w: invalid runtime process state", ErrInvalidInput)
	}
	if strings.TrimSpace(checkpoint.Generation) == "" || strings.TrimSpace(checkpoint.ProcessStart) == "" || strings.TrimSpace(checkpoint.CodexVersion) == "" || !validDigest(checkpoint.SchemaSHA256) {
		return fmt.Errorf("%w: incomplete runtime checkpoint", ErrInvalidInput)
	}
	switch checkpoint.State {
	case room.RuntimeProcessRunning:
		if checkpoint.PID <= 1 || checkpoint.PGID <= 1 {
			return fmt.Errorf("%w: running checkpoint requires process ids greater than one", ErrInvalidInput)
		}
	case room.RuntimeProcessStopped:
		if checkpoint.PID != 0 || checkpoint.PGID != 0 {
			return fmt.Errorf("%w: stopped checkpoint requires zero process ids", ErrInvalidInput)
		}
	}
	return nil
}

func (s *Store) BindThread(ctx context.Context, roomID room.RoomID, snapshot room.ThreadSnapshot) (room.DurableEvent, error) {
	if err := validateRoomID(roomID); err != nil {
		return room.DurableEvent{}, err
	}
	if err := validateThreadSnapshot(snapshot); err != nil {
		return room.DurableEvent{}, err
	}
	var event room.DurableEvent
	err := s.transact(ctx, func(tx *sql.Tx) error {
		var project string
		var oldThread sql.NullString
		err := tx.QueryRowContext(ctx, "SELECT project_path, codex_thread_id FROM rooms WHERE id = ?", roomID).Scan(&project, &oldThread)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRoomNotFound
		}
		if err != nil {
			return fmt.Errorf("load thread binding: %w", err)
		}
		if snapshot.CWD != project {
			return fmt.Errorf("%w: thread cwd does not match room project", ErrInvalidInput)
		}
		result, err := tx.ExecContext(ctx, "UPDATE rooms SET codex_thread_id = ? WHERE id = ? AND codex_thread_id IS ?", snapshot.ID, roomID, nullStringValue(oldThread))
		if err != nil {
			return fmt.Errorf("bind room thread: %w", err)
		}
		if err := requireOneRow(result); err != nil {
			return err
		}
		event, err = appendEvent(ctx, tx, roomID, 0, "thread/bound", struct {
			PreviousThreadID string        `json:"previous_thread_id,omitempty"`
			ThreadID         room.ThreadID `json:"thread_id"`
			CWD              string        `json:"cwd"`
		}{oldThread.String, snapshot.ID, snapshot.CWD})
		return err
	})
	return event, err
}

func validateThreadSnapshot(snapshot room.ThreadSnapshot) error {
	if strings.TrimSpace(string(snapshot.ID)) == "" || strings.TrimSpace(snapshot.CWD) == "" {
		return fmt.Errorf("%w: incomplete thread snapshot", ErrInvalidInput)
	}
	for _, turn := range snapshot.Turns {
		if strings.TrimSpace(string(turn.ID)) == "" || !validRequestState(turn.State) {
			return fmt.Errorf("%w: invalid turn snapshot", ErrInvalidInput)
		}
		for _, item := range turn.Items {
			if item.ThreadID != snapshot.ID || item.TurnID != turn.ID || strings.TrimSpace(string(item.ItemID)) == "" || !json.Valid(item.Payload) {
				return fmt.Errorf("%w: invalid completed item snapshot", ErrInvalidInput)
			}
		}
	}
	return nil
}

func validRequestState(state room.RequestState) bool {
	switch state {
	case room.RequestQueued, room.RequestDispatching, room.RequestRunning, room.RequestCompleted, room.RequestFailed, room.RequestInterrupted, room.RequestNeedsReview:
		return true
	default:
		return false
	}
}

func nullStringValue(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

func (s *Store) MarkThreadNeedsRepair(ctx context.Context, roomID room.RoomID, reason room.RepairReason) (room.DurableEvent, error) {
	if err := validateRoomID(roomID); err != nil {
		return room.DurableEvent{}, err
	}
	if err := validateReason(reason.Code, reason.DetailDigest); err != nil {
		return room.DurableEvent{}, err
	}
	var event room.DurableEvent
	err := s.transact(ctx, func(tx *sql.Tx) error {
		var oldStatus room.RoomStatus
		var threadID sql.NullString
		err := tx.QueryRowContext(ctx, "SELECT status, codex_thread_id FROM rooms WHERE id = ?", roomID).Scan(&oldStatus, &threadID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRoomNotFound
		}
		if err != nil {
			return fmt.Errorf("load room repair state: %w", err)
		}
		if oldStatus == room.RoomThreadNeedsRepair {
			return ErrInvalidState
		}
		result, err := tx.ExecContext(ctx, "UPDATE rooms SET status = ? WHERE id = ? AND status = ?", room.RoomThreadNeedsRepair, roomID, oldStatus)
		if err != nil {
			return fmt.Errorf("mark thread needs repair: %w", err)
		}
		if err := requireOneRow(result); err != nil {
			return err
		}
		event, err = appendEvent(ctx, tx, roomID, 0, "thread/needs-repair", struct {
			ThreadID     string `json:"thread_id,omitempty"`
			Code         string `json:"code"`
			DetailDigest string `json:"detail_digest"`
		}{threadID.String, reason.Code, reason.DetailDigest})
		return err
	})
	return event, err
}

func (s *Store) SetRoomStatus(ctx context.Context, roomID room.RoomID, status room.RoomStatus) (*room.DurableEvent, error) {
	if err := validateRoomID(roomID); err != nil {
		return nil, err
	}
	if status != room.RoomReady && status != room.RoomRecovering && status != room.RoomThreadNeedsRepair {
		return nil, fmt.Errorf("%w: invalid room status", ErrInvalidInput)
	}
	var event *room.DurableEvent
	wrote := false
	err := s.transactConditional(ctx, func(tx *sql.Tx) error {
		var oldStatus room.RoomStatus
		err := tx.QueryRowContext(ctx, "SELECT status FROM rooms WHERE id = ?", roomID).Scan(&oldStatus)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRoomNotFound
		}
		if err != nil {
			return fmt.Errorf("load room status: %w", err)
		}
		if oldStatus == status {
			return nil
		}
		wrote = true
		result, err := tx.ExecContext(ctx, "UPDATE rooms SET status = ? WHERE id = ? AND status = ?", status, roomID, oldStatus)
		if err != nil {
			return fmt.Errorf("set room status: %w", err)
		}
		if err := requireOneRow(result); err != nil {
			return err
		}
		created, err := appendEvent(ctx, tx, roomID, 0, "room/status", struct {
			Previous room.RoomStatus `json:"previous"`
			Current  room.RoomStatus `json:"current"`
		}{oldStatus, status})
		if err != nil {
			return err
		}
		event = &created
		return nil
	}, func() bool { return wrote })
	return event, err
}

func (s *Store) ResolveReview(ctx context.Context, roomID room.RoomID, actor room.Actor, input room.RecoverInput) (room.RecoveryResult, error) {
	if err := input.Validate(); err != nil {
		return room.RecoveryResult{}, err
	}
	if err := validateRoomID(roomID); err != nil {
		return room.RecoveryResult{}, err
	}
	bodyJSON, err := json.Marshal(struct {
		TargetMessageID      room.ClientMessageID `json:"target_message_id"`
		Action               room.RecoveryAction  `json:"action"`
		ReplacementMessageID room.ClientMessageID `json:"replacement_message_id,omitempty"`
		Instruction          string               `json:"instruction,omitempty"`
	}{input.TargetMessageID, input.Action, input.ReplacementMessageID, input.Instruction})
	if err != nil {
		return room.RecoveryResult{}, fmt.Errorf("marshal recovery identity: %w", err)
	}
	hash := canonicalHash("recover", actor.UID, bodyJSON)
	var recovery room.RecoveryResult
	wrote := false
	err = s.transactConditional(ctx, func(tx *sql.Tx) error {
		existing, err := loadStoredMessage(ctx, tx, roomID, input.ClientMessageID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("load recovery idempotency record: %w", err)
		}
		if err == nil {
			if existing.kind != "recover" || existing.actorUID != actor.UID || !bytes.Equal(existing.payloadHash, hash) {
				return ErrIdempotencyConflict
			}
			events, err := loadRecoveryEvents(ctx, tx, roomID, existing.id)
			if err != nil {
				return err
			}
			recovery = room.RecoveryResult{Duplicate: true, Events: events}
			return nil
		}
		if err := requireActor(ctx, tx, roomID, actor); err != nil {
			return err
		}
		target, err := loadStoredMessage(ctx, tx, roomID, input.TargetMessageID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMessageNotFound
		}
		if err != nil {
			return fmt.Errorf("load recovery target: %w", err)
		}
		if target.state != room.RequestNeedsReview || target.kind == "note" || target.kind == "recover" {
			return ErrStaleRecovery
		}
		if input.Action == room.RecoveryContinue {
			var existingReplacement int
			err := tx.QueryRowContext(ctx, "SELECT 1 FROM messages WHERE room_id = ? AND client_message_id = ?", roomID, input.ReplacementMessageID).Scan(&existingReplacement)
			if err == nil {
				return ErrIdempotencyConflict
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("check recovery replacement id: %w", err)
			}
		}

		wrote = true
		now := nowUTC()
		acceptedSeq, err := allocateSeq(ctx, tx, roomID)
		if err != nil {
			return err
		}
		inserted, err := tx.ExecContext(ctx, `
			INSERT INTO messages(room_id, client_message_id, actor_uid, kind, body, payload_hash, state, accepted_seq, created_at)
			VALUES (?, ?, ?, 'recover', ?, ?, ?, ?, ?)`, roomID, input.ClientMessageID, actor.UID, string(bodyJSON), hash, room.RequestCompleted, acceptedSeq, encodeTime(now))
		if err != nil {
			return fmt.Errorf("insert recovery command: %w", err)
		}
		recoveryMessageID, err := inserted.LastInsertId()
		if err != nil {
			return fmt.Errorf("load recovery command id: %w", err)
		}
		acceptedEvent, err := insertEventAt(ctx, tx, roomID, acceptedSeq, actor.UID, "recovery/accepted", struct {
			ClientMessageID room.ClientMessageID `json:"client_message_id"`
			TargetMessageID room.ClientMessageID `json:"target_message_id"`
			Action          room.RecoveryAction  `json:"action"`
		}{input.ClientMessageID, input.TargetMessageID, input.Action}, now)
		if err != nil {
			return err
		}
		recovery.Events = append(recovery.Events, acceptedEvent)

		switch input.Action {
		case room.RecoveryRetry:
			if target.kind == "prompt" || target.kind == "recovery-prompt" {
				if err := resetReviewedPrompt(ctx, tx, roomID, target.id); err != nil {
					return err
				}
			} else {
				if err := resetReviewedControl(ctx, tx, roomID, target.id); err != nil {
					return err
				}
				retry, err := retryCommand(ctx, tx, roomID, input.TargetMessageID, target)
				if err != nil {
					return err
				}
				recovery.Retry = retry
			}
			event, err := appendEvent(ctx, tx, roomID, actor.UID, "recovery/retried", struct {
				TargetMessageID          room.ClientMessageID `json:"target_message_id"`
				State                    room.RequestState    `json:"state"`
				DuplicateEffectsPossible bool                 `json:"duplicate_effects_possible"`
			}{input.TargetMessageID, recoveryRetryState(target.kind), true})
			if err != nil {
				return err
			}
			recovery.Events = append(recovery.Events, event)
		case room.RecoverySkip, room.RecoveryContinue:
			code := "recovery-skipped"
			kind := "recovery/skipped"
			if input.Action == room.RecoveryContinue {
				code = "recovery-continued"
				kind = "recovery/continued"
			}
			if err := terminalizeReviewed(ctx, tx, roomID, target.id, code, encodeTime(now)); err != nil {
				return err
			}
			event, err := appendEvent(ctx, tx, roomID, actor.UID, kind, struct {
				TargetMessageID room.ClientMessageID `json:"target_message_id"`
				State           room.RequestState    `json:"state"`
				ErrorCode       string               `json:"error_code"`
			}{input.TargetMessageID, room.RequestFailed, code})
			if err != nil {
				return err
			}
			recovery.Events = append(recovery.Events, event)
			if input.Action == room.RecoveryContinue {
				replacement, err := insertRecoveryPrompt(ctx, tx, roomID, actor, input.ReplacementMessageID, input.Instruction)
				if err != nil {
					return err
				}
				recovery.Events = append(recovery.Events, replacement)
			}
		default:
			return fmt.Errorf("%w: invalid recovery action", ErrInvalidInput)
		}

		if _, err := tx.ExecContext(ctx, "INSERT INTO recovery_results(recovery_message_id, room_id, target_message_id, action) VALUES (?, ?, ?, ?)", recoveryMessageID, roomID, target.id, input.Action); err != nil {
			return fmt.Errorf("insert recovery result: %w", err)
		}
		for index, event := range recovery.Events {
			if _, err := tx.ExecContext(ctx, "INSERT INTO recovery_result_events(recovery_message_id, ordinal, room_id, seq) VALUES (?, ?, ?, ?)", recoveryMessageID, index, roomID, event.Seq); err != nil {
				return fmt.Errorf("link recovery result event: %w", err)
			}
		}
		return nil
	}, func() bool { return wrote })
	return recovery, err
}

func resetReviewedPrompt(ctx context.Context, tx *sql.Tx, roomID room.RoomID, messageID int64) error {
	result, err := tx.ExecContext(ctx, "UPDATE messages SET state = ? WHERE room_id = ? AND id = ? AND state = ?", room.RequestQueued, roomID, messageID, room.RequestNeedsReview)
	if err != nil {
		return fmt.Errorf("queue reviewed prompt: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return ErrStaleRecovery
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE turn_bindings SET state = ?, codex_turn_id = NULL, started_at = NULL, completed_at = NULL,
			error_code = NULL, error_digest = NULL
		WHERE room_id = ? AND message_id = ? AND state = ?`, room.RequestQueued, roomID, messageID, room.RequestNeedsReview)
	if err != nil {
		return fmt.Errorf("reset reviewed prompt binding: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return ErrStaleRecovery
	}
	return nil
}

func resetReviewedControl(ctx context.Context, tx *sql.Tx, roomID room.RoomID, messageID int64) error {
	result, err := tx.ExecContext(ctx, "UPDATE messages SET state = ? WHERE room_id = ? AND id = ? AND state = ?", room.RequestDispatching, roomID, messageID, room.RequestNeedsReview)
	if err != nil {
		return fmt.Errorf("dispatch reviewed control: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return ErrStaleRecovery
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE turn_bindings SET state = ?, completed_at = NULL, error_code = NULL, error_digest = NULL
		WHERE room_id = ? AND message_id = ? AND state = ?`, room.RequestDispatching, roomID, messageID, room.RequestNeedsReview)
	if err != nil {
		return fmt.Errorf("reset reviewed control binding: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return ErrStaleRecovery
	}
	return nil
}

func terminalizeReviewed(ctx context.Context, tx *sql.Tx, roomID room.RoomID, messageID int64, code, completedAt string) error {
	result, err := tx.ExecContext(ctx, "UPDATE messages SET state = ? WHERE room_id = ? AND id = ? AND state = ?", room.RequestFailed, roomID, messageID, room.RequestNeedsReview)
	if err != nil {
		return fmt.Errorf("terminalize reviewed message: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return ErrStaleRecovery
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE turn_bindings SET state = ?, completed_at = ?, error_code = ?, error_digest = NULL
		WHERE room_id = ? AND message_id = ? AND state = ?`, room.RequestFailed, completedAt, code, roomID, messageID, room.RequestNeedsReview)
	if err != nil {
		return fmt.Errorf("terminalize reviewed binding: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return ErrStaleRecovery
	}
	return nil
}

func retryCommand(ctx context.Context, tx *sql.Tx, roomID room.RoomID, clientID room.ClientMessageID, target storedMessage) (*room.RetryCommand, error) {
	var body struct {
		ExpectedTurnID room.TurnID `json:"expected_turn_id"`
		Text           string      `json:"text"`
	}
	if err := json.Unmarshal([]byte(target.body), &body); err != nil {
		return nil, fmt.Errorf("decode persisted control command: %w", err)
	}
	var actor room.Actor
	err := tx.QueryRowContext(ctx, "SELECT uid, username FROM members WHERE room_id = ? AND uid = ?", roomID, target.actorUID).Scan(&actor.UID, &actor.Name)
	if err != nil {
		return nil, fmt.Errorf("load persisted control actor: %w", err)
	}
	kind := room.RetrySteer
	if target.kind == "cancel" {
		kind = room.RetryCancel
	}
	return &room.RetryCommand{Kind: kind, ClientMessageID: clientID, Actor: actor, ExpectedTurnID: body.ExpectedTurnID, Text: body.Text}, nil
}

func recoveryRetryState(kind string) room.RequestState {
	if kind == "prompt" || kind == "recovery-prompt" {
		return room.RequestQueued
	}
	return room.RequestDispatching
}

func insertRecoveryPrompt(ctx context.Context, tx *sql.Tx, roomID room.RoomID, actor room.Actor, clientID room.ClientMessageID, instruction string) (room.DurableEvent, error) {
	bodyJSON, _ := json.Marshal(struct {
		Text string `json:"text"`
	}{instruction})
	hash := canonicalHash("recovery-prompt", actor.UID, bodyJSON)
	seq, err := allocateSeq(ctx, tx, roomID)
	if err != nil {
		return room.DurableEvent{}, err
	}
	now := nowUTC()
	inserted, err := tx.ExecContext(ctx, `
		INSERT INTO messages(room_id, client_message_id, actor_uid, kind, body, payload_hash, state, accepted_seq, created_at)
		VALUES (?, ?, ?, 'recovery-prompt', ?, ?, ?, ?, ?)`, roomID, clientID, actor.UID, instruction, hash, room.RequestQueued, seq, encodeTime(now))
	if err != nil {
		if isUniqueConstraint(err) {
			return room.DurableEvent{}, ErrIdempotencyConflict
		}
		return room.DurableEvent{}, fmt.Errorf("insert recovery prompt: %w", err)
	}
	messageID, err := inserted.LastInsertId()
	if err != nil {
		return room.DurableEvent{}, fmt.Errorf("read recovery prompt id: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO turn_bindings(room_id, message_id, state) VALUES (?, ?, ?)", roomID, messageID, room.RequestQueued); err != nil {
		return room.DurableEvent{}, fmt.Errorf("insert recovery prompt binding: %w", err)
	}
	return insertEventAt(ctx, tx, roomID, seq, actor.UID, "message/accepted", struct {
		ClientMessageID room.ClientMessageID `json:"client_message_id"`
		MessageID       int64                `json:"message_id"`
		Kind            string               `json:"kind"`
		Body            string               `json:"body"`
		State           room.RequestState    `json:"state"`
	}{clientID, messageID, "recovery-prompt", instruction, room.RequestQueued}, now)
}

func loadRecoveryEvents(ctx context.Context, tx *sql.Tx, roomID room.RoomID, recoveryMessageID int64) ([]room.DurableEvent, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT e.seq, e.kind, e.actor_uid, e.payload, e.created_at
		FROM recovery_result_events r JOIN room_events e ON e.room_id = r.room_id AND e.seq = r.seq
		WHERE r.recovery_message_id = ? AND r.room_id = ? ORDER BY r.ordinal`, recoveryMessageID, roomID)
	if err != nil {
		return nil, fmt.Errorf("load recovery result events: %w", err)
	}
	defer rows.Close()
	var events []room.DurableEvent
	for rows.Next() {
		var event room.DurableEvent
		var created string
		if err := rows.Scan(&event.Seq, &event.Kind, &event.ActorUID, &event.Payload, &created); err != nil {
			return nil, fmt.Errorf("scan recovery result event: %w", err)
		}
		parsed, err := decodeTime(created)
		if err != nil {
			return nil, fmt.Errorf("decode recovery result event time: %w", err)
		}
		event.CreatedAt = parsed
		event.Payload = append(json.RawMessage(nil), event.Payload...)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recovery result events: %w", err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("stored recovery result has no events")
	}
	return events, nil
}
