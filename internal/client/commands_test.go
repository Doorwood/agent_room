package client

import "testing"

func TestParseCommands(t *testing.T) {
	for _, s := range []string{"work", "/steer more", "/note hi", "/queue", "/status", "/who", "/cancel", "/diff", "/quit", "/recover retry 00000000000000000000000000000001", "/recover skip 00000000000000000000000000000001", "/recover continue 00000000000000000000000000000001 inspect"} {
		if _, e := ParseCommand(s); e != nil {
			t.Fatal(s, e)
		}
	}
	for _, s := range []string{"", "/steer", "/note", "/status extra", "/unknown", "/recover retry nope", "/recover continue 00000000000000000000000000000001"} {
		if _, e := ParseCommand(s); e == nil {
			t.Fatal(s)
		}
	}
}
