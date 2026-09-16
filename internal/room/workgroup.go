package room

import "context"

// AgentMember is an executor identity. It deliberately has no human UID or role.
type AgentMember struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
}
type AgentDirectory interface{ AgentMembers() []AgentMember }
type SubmissionValidator interface{ ValidateSubmission(SubmitInput) error }
type assignmentKey struct{}

// AssignmentContext carries the original, durably accepted message separately
// from participant headers and personal authorization instructions.
func AssignmentContext(ctx context.Context, in SubmitInput) context.Context {
	return context.WithValue(ctx, assignmentKey{}, in)
}
func AssignedInput(ctx context.Context) (SubmitInput, bool) {
	in, ok := ctx.Value(assignmentKey{}).(SubmitInput)
	return in, ok
}
