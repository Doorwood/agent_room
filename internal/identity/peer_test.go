package identity

import (
	"errors"
	"net"
	"os"
	"runtime"
	"testing"
)

func TestPeerRejectsOtherTransports(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if _, err := (KernelResolver{}).Resolve(a); err == nil {
		t.Fatal("accepted non-Unix transport")
	}
}

func TestPeerRealUnixCredentials(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "peer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: dir + "/s", Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	conn, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	peer, err := (KernelResolver{}).Resolve(conn)
	if runtime.GOOS != "linux" {
		if !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatal(err)
		}
		return
	}
	if err != nil || uint32(peer.UID) != uint32(os.Getuid()) || peer.PID != os.Getpid() {
		t.Fatal(peer, err)
	}
}
