//go:build darwin

package admin

import "errors"

// Production process supervision is Linux-only. Embedders can inject an
// identity reader for deterministic Darwin tests, never via a CLI bypass.
func processIdentity(int) (string, error) {
	return "", errors.New("process identity unsupported on this platform")
}
