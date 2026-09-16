package room

// VirtualMember describes the project's executor, not an authorizing human UID.
// It must never be accepted as a submitter, approver or credential owner.
type VirtualMember struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

func CodexMember(connected bool, activeTurn string) VirtualMember {
	state := "offline"
	if connected {
		state = "idle"
		if activeTurn != "" {
			state = "working"
		}
	}
	return VirtualMember{ID: "agent:codex", Name: "Codex Agent", Kind: "agent", Status: state}
}
