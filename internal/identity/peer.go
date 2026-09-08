package identity

import (
	"agent_romm/internal/room"
	"errors"
	"net"
)

type Peer struct {
	UID room.UID
	PID int
}
type Resolver interface{ Resolve(net.Conn) (Peer, error) }
type KernelResolver struct{}

var ErrUnsupportedPlatform = errors.New("peer identity unsupported on this platform")
var ErrPeerCredentials = errors.New("peer credentials unavailable")
