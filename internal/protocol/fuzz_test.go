package protocol

import (
	"bytes"
	"testing"
)

func FuzzReader(f *testing.F) {
	f.Add([]byte{0, 0, 0, 2, '{', '}'})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = NewReader(bytes.NewReader(data), MaxFrameBytes).Read()
	})
}
