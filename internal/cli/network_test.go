package cli

import (
	"net"
	"reflect"
	"testing"
)

func TestJoinFlagsWorkAfterPositionals(t *testing.T) {
	got := interspersed([]string{"10.1.2.3", "session", "--name", "me"})
	want := []string{"--name", "me", "--", "10.1.2.3", "session"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}
func TestAdvertisedAddressNeverWildcard(t *testing.T) {
	for _, bound := range []string{"0.0.0.0:7443", "[::]:7443"} {
		got := advertisedAddress(bound)
		host, port, err := net.SplitHostPort(got)
		if err != nil || port != "7443" || host == "" {
			t.Fatal(got, err)
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			t.Fatal(got)
		}
	}
	if got := advertisedAddress("127.0.0.1:7443"); got != "127.0.0.1:7443" {
		t.Fatal(got)
	}
}
