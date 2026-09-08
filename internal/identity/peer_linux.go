//go:build linux

package identity

import (
	"agent_romm/internal/room"
	"golang.org/x/sys/unix"
	"net"
)

func (KernelResolver) Resolve(conn net.Conn) (Peer, error) {
	c, ok := conn.(*net.UnixConn)
	if !ok {
		return Peer{}, ErrPeerCredentials
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return Peer{}, ErrPeerCredentials
	}
	var cred *unix.Ucred
	var sockErr error
	err = raw.Control(func(fd uintptr) { cred, sockErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if err != nil || sockErr != nil || cred == nil || cred.Pid <= 0 || cred.Uid == ^uint32(0) {
		return Peer{}, ErrPeerCredentials
	}
	return Peer{UID: room.UID(cred.Uid), PID: int(cred.Pid)}, nil
}
