package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"agent_romm/internal/room"
)

var ErrAdmission = errors.New("admission unavailable or not approved")

type JoinRequest struct {
	Role    string   `json:"role,omitempty"`
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	State   string   `json:"state"`
	Address string   `json:"address"`
	UID     room.UID `json:"uid"`
	Created int64    `json:"created"`
}

// Admission tables are additive; opening a legacy SSH room never changes its schema.
func (s *Store) InitAdmission(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS join_requests (
 id TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id),
 token_hash TEXT NOT NULL, name TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('pending','approved','denied','revoked','expired')),
 address TEXT NOT NULL, uid INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL,
 UNIQUE(room_id,token_hash))`)
	return err
}

func tokenHash(token string) (string, error) {
	b, err := hex.DecodeString(token)
	if err != nil || len(b) != 32 {
		return "", ErrAdmission
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func validJoinName(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

func scanJoin(row interface{ Scan(...any) error }) (JoinRequest, error) {
	var r JoinRequest
	err := row.Scan(&r.ID, &r.Name, &r.State, &r.Address, &r.UID, &r.Created, &r.Role)
	return r, err
}

const joinColumns = "id,name,state,address,uid,created,coalesce((SELECT role FROM member_roles WHERE member_roles.room_id=join_requests.room_id AND member_roles.uid=join_requests.uid),'roommate')"

func (s *Store) RequestJoin(ctx context.Context, rid room.RoomID, token, name, address string) (result JoinRequest, err error) {
	hash, err := tokenHash(token)
	if err != nil || !validJoinName(name) || len(address) > 128 {
		return result, ErrAdmission
	}
	err = s.transact(ctx, func(tx *sql.Tx) error {
		now := time.Now().Unix()
		if _, e := tx.ExecContext(ctx, "UPDATE join_requests SET state='expired' WHERE room_id=? AND state='pending' AND created<?", rid, now-86400); e != nil {
			return e
		}
		r, e := scanJoin(tx.QueryRowContext(ctx, "SELECT "+joinColumns+" FROM join_requests WHERE room_id=? AND token_hash=?", rid, hash))
		if e == nil {
			result = r
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var pending int
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM join_requests WHERE room_id=? AND state='pending'", rid).Scan(&pending); e != nil {
			return e
		}
		if pending >= 128 {
			return errors.New("pending admission queue full; host can deny requests to free capacity")
		}
		var count int
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM join_requests WHERE room_id=?", rid).Scan(&count); e != nil {
			return e
		}
		if count >= 4096 {
			// Rejected/expired requests carry no granted authority. Revoked
			// credentials remain tombstones and can never rejoin implicitly.
			if _, e = tx.ExecContext(ctx, "DELETE FROM join_requests WHERE room_id=? AND state IN ('denied','expired')", rid); e != nil {
				return e
			}
			if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM join_requests WHERE room_id=?", rid).Scan(&count); e != nil {
				return e
			}
			if count >= 4096 {
				return errors.New("session member capacity reached")
			}
		}
		var b [12]byte
		if _, e = rand.Read(b[:]); e != nil {
			return e
		}
		result = JoinRequest{ID: hex.EncodeToString(b[:]), Name: name, State: "pending", Address: address, Created: now}
		_, e = tx.ExecContext(ctx, "INSERT INTO join_requests(id,room_id,token_hash,name,state,address,created) VALUES(?,?,?,?,'pending',?,?)", result.ID, rid, hash, name, address, now)
		return e
	})
	return result, err
}

func (s *Store) AuthenticateJoin(ctx context.Context, rid room.RoomID, token string) (room.Member, error) {
	hash, err := tokenHash(token)
	if err != nil {
		return room.Member{}, err
	}
	var m room.Member
	err = s.db.QueryRowContext(ctx, `SELECT m.uid,m.username FROM join_requests j JOIN members m ON m.room_id=j.room_id AND m.uid=j.uid WHERE j.room_id=? AND j.token_hash=? AND j.state='approved'`, rid, hash).Scan(&m.UID, &m.Name)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrAdmission
	}
	return m, err
}

// DecideJoin binds approval to the secret-bearing request, never to its chosen name.
// The virtual identity and approval are published in one transaction.
func (s *Store) DecideJoin(ctx context.Context, rid room.RoomID, id, action string) (JoinRequest, error) {
	return s.decideJoin(ctx, rid, id, action, "roommate")
}
func (s *Store) decideJoin(ctx context.Context, rid room.RoomID, id, action, role string) (result JoinRequest, err error) {
	err = s.transact(ctx, func(tx *sql.Tx) error {
		r, e := scanJoin(tx.QueryRowContext(ctx, "SELECT "+joinColumns+" FROM join_requests WHERE room_id=? AND id=?", rid, id))
		if e != nil {
			return ErrAdmission
		}
		switch action {
		case "approve":
			if r.State != "pending" || r.Created < time.Now().Unix()-86400 {
				return ErrAdmission
			}
			var next uint64
			if e = tx.QueryRowContext(ctx, "SELECT max(1000000000,coalesce(max(uid)+1,1000000000)) FROM members WHERE room_id=?", rid).Scan(&next); e != nil {
				return e
			}
			if next > 4294967295 {
				return ErrAdmission
			}
			r.UID = room.UID(next)
			if _, e = tx.ExecContext(ctx, "INSERT INTO members(room_id,uid,username,added_at) VALUES(?,?,?,?)", rid, r.UID, r.Name, encodeTime(nowUTC())); e != nil {
				return fmt.Errorf("member name already in use or storage unavailable: %w", e)
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO member_roles(room_id,uid,role) VALUES(?,?,?)", rid, r.UID, role); e != nil {
				return e
			}
			r.Role = role
			r.State = "approved"
		case "deny":
			if r.State != "pending" {
				return ErrAdmission
			}
			r.State = "denied"
		case "revoke":
			if r.State != "approved" {
				return ErrAdmission
			}
			r.State = "revoked"
		default:
			return ErrAdmission
		}
		_, e = tx.ExecContext(ctx, "UPDATE join_requests SET state=?,uid=? WHERE room_id=? AND id=?", r.State, r.UID, rid, id)
		result = r
		return e
	})
	return result, err
}

func (s *Store) ListJoins(ctx context.Context, rid room.RoomID) ([]JoinRequest, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+joinColumns+" FROM join_requests WHERE room_id=? ORDER BY created,id", rid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []JoinRequest{}
	for rows.Next() {
		r, e := scanJoin(rows)
		if e != nil {
			return nil, e
		}
		if r.State == "pending" && r.Created < time.Now().Unix()-86400 {
			r.State = "expired"
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
