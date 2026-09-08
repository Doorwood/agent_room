//go:build !linux

package identity

import "net"

func (KernelResolver) Resolve(net.Conn) (Peer, error) { return Peer{}, ErrUnsupportedPlatform }
