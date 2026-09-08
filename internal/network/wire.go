// Package network provides certificate-pinned session admission and room streams.
package network

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const DefaultPort = "7443"
const maxHello = 8192

type Hello struct {
	Session string `json:"session"`
	Token   string `json:"token"`
	Name    string `json:"name"`
}
type Reply struct {
	State     string `json:"state"`
	RequestID string `json:"request_id,omitempty"`
}

func writeJSON(w io.Writer, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if len(b) > maxHello {
		return errors.New("admission frame too large")
	}
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	packet := append(n[:], b...)
	for len(packet) > 0 {
		count, err := w.Write(packet)
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
		packet = packet[count:]
	}
	return nil
}
func readJSON(r io.Reader, v any) error {
	var n [4]byte
	if _, e := io.ReadFull(r, n[:]); e != nil {
		return e
	}
	size := binary.BigEndian.Uint32(n[:])
	if size == 0 || size > maxHello {
		return errors.New("invalid admission frame")
	}
	b := make([]byte, size)
	if _, e := io.ReadFull(r, b); e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing admission data")
	}
	return nil
}
func ParseSession(s string) (string, string, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 2 {
		return "", "", errors.New("use the complete session_id printed by host")
	}
	for i, n := range []int{16, 32} {
		b, e := hex.DecodeString(parts[i])
		if e != nil || len(b) != n || strings.ToLower(parts[i]) != parts[i] {
			return "", "", errors.New("invalid session_id")
		}
	}
	return parts[0], parts[1], nil
}
