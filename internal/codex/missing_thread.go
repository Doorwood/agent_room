package codex

import (
	"agent_romm/internal/room"
	"errors"
)

var ErrThreadNotFound = errors.New("Codex thread has no saved rollout")

func readThreadError(err error, id room.ThreadID) error {
	var rpcErr *RPCError
	// Exact response from the pinned CLI; generic RPC failures/timeouts are not
	// evidence that a thread can be replaced.
	if errors.As(err, &rpcErr) && rpcErr.Code == -32600 && rpcErr.Message == "thread not loaded: "+string(id) {
		return ErrThreadNotFound
	}
	return stableAdapterError(err)
}
