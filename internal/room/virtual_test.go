package room

import "testing"

func TestVirtualMemberDoesNotBecomeAnAuthorizingUID(t *testing.T) {
	for _, tt := range []struct {
		connected  bool
		turn, want string
	}{{false, "", "offline"}, {true, "", "idle"}, {true, "turn-1", "working"}, {false, "turn-1", "offline"}} {
		m := CodexMember(tt.connected, tt.turn)
		if m.ID != "agent:codex" || m.Kind != "agent" || m.Status != tt.want {
			t.Fatal(m)
		}
	}
}
