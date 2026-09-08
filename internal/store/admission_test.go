package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestAdmissionRequiresDecisionAndPersistsIdentity(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	ctx := context.Background()
	if err := s.InitAdmission(ctx); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	r, err := s.RequestJoin(ctx, "team", token, "visitor", "127.0.0.1")
	if err != nil || r.State != "pending" {
		t.Fatal(r, err)
	}
	if _, err = s.AuthenticateJoin(ctx, "team", token); err == nil {
		t.Fatal("pending token authorized")
	}
	if _, err = s.DecideJoin(ctx, "team", r.ID, "approve"); err != nil {
		t.Fatal(err)
	}
	m, err := s.AuthenticateJoin(ctx, "team", token)
	if err != nil || m.Name != "visitor" || m.UID < 1000000000 {
		t.Fatal(m, err)
	}
	again, err := s.RequestJoin(ctx, "team", token, "spoofed-name", "127.0.0.1")
	if err != nil || again.Name != "visitor" {
		t.Fatal(again, err)
	}
	if _, err = s.AuthenticateJoin(ctx, "team", strings.Repeat("b", 64)); err == nil {
		t.Fatal("wrong token authorized")
	}
	if _, err = s.DecideJoin(ctx, "team", r.ID, "revoke"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateJoin(ctx, "team", token); err == nil {
		t.Fatal("revoked token authorized")
	}
	if _, err = s.DecideJoin(ctx, "team", r.ID, "approve"); err == nil {
		t.Fatal("revoked token reapproved")
	}
}

func TestAdmissionDenialAndDuplicateNames(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	ctx := context.Background()
	if err := s.InitAdmission(ctx); err != nil {
		t.Fatal(err)
	}
	a, _ := s.RequestJoin(ctx, "team", strings.Repeat("c", 64), "visitor", "local")
	b, _ := s.RequestJoin(ctx, "team", strings.Repeat("d", 64), "visitor", "local")
	if _, err := s.DecideJoin(ctx, "team", a.ID, "approve"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideJoin(ctx, "team", b.ID, "approve"); err == nil {
		t.Fatal("duplicate identity accepted")
	}
	if _, err := s.DecideJoin(ctx, "team", b.ID, "deny"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateJoin(ctx, "team", strings.Repeat("d", 64)); err == nil {
		t.Fatal("denied token accepted")
	}
	if _, err := s.RequestJoin(ctx, "team", "short", "bad", "local"); err == nil {
		t.Fatal("short token accepted")
	}
	if _, err := s.RequestJoin(ctx, "team", strings.Repeat("e", 64), "\033[31mbad", "local"); err == nil {
		t.Fatal("control name accepted")
	}
}

func TestAdmissionCapacityCanRecoverAfterDenial(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	ctx := context.Background()
	if err := s.InitAdmission(ctx); err != nil {
		t.Fatal(err)
	}
	var first JoinRequest
	for i := 0; i < 128; i++ {
		r, err := s.RequestJoin(ctx, "team", fmt.Sprintf("%064x", i+1), fmt.Sprintf("visitor-%d", i), "local")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = r
		}
	}
	if _, err := s.RequestJoin(ctx, "team", strings.Repeat("f", 64), "extra", "local"); err == nil {
		t.Fatal("pending queue unbounded")
	}
	if _, err := s.DecideJoin(ctx, "team", first.ID, "deny"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestJoin(ctx, "team", strings.Repeat("f", 64), "extra", "local"); err != nil {
		t.Fatal("denial did not free capacity", err)
	}
}

func TestAdmissionPrunesExpiredButNeverRevoked(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	ctx := context.Background()
	if err := s.InitAdmission(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := s.RequestJoin(ctx, "team", strings.Repeat("a", 64), "visitor", "local")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DecideJoin(ctx, "team", r.ID, "approve"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DecideJoin(ctx, "team", r.ID, "revoke"); err != nil {
		t.Fatal(err)
	}
	_, err = s.db.ExecContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<4095)
 INSERT INTO join_requests(id,room_id,token_hash,name,state,address,uid,created)
 SELECT 'old-'||x,'team','hash-'||x,'old','expired','local',0,0 FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestJoin(ctx, "team", strings.Repeat("b", 64), "fresh", "local"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateJoin(ctx, "team", strings.Repeat("a", 64)); err == nil {
		t.Fatal("revoked identity lost")
	}
	rows, err := s.ListJoins(ctx, "team")
	if err != nil || len(rows) != 2 {
		t.Fatal(len(rows), err)
	}
}
