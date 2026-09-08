package store

import (
	"agent_romm/internal/room"
	"context"
	"testing"
)

func TestListMembersIncludesOfflineIdentities(t *testing.T) {
	s := openTestStore(t)
	err := s.InitializeRoom(context.Background(), RoomSeed{ID: "r", DisplayName: "room", HostID: "host", ProjectRoot: "/project", ExecutionOwnerUID: alice.UID, Members: []room.Member{{UID: alice.UID, Name: alice.Name}, {UID: bob.UID, Name: bob.Name}}})
	if err != nil {
		t.Fatal(err)
	}
	members, err := s.ListMembers(context.Background(), "r")
	if err != nil || len(members) != 2 || members[1].Name != "bob" {
		t.Fatal(members, err)
	}
}
