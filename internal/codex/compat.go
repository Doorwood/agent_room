package codex

import (
	"errors"
	"strings"
)

const (
	SupportedCodexVersion = "0.153.4"
	SupportedCLIOutput    = "codex-cli " + SupportedCodexVersion
	SupportedSchemaSHA256 = "e8284c5cb8157554a3dd1e035aadbd4325aea501af56887e9c2e12eb1b9b9448"
)

var (
	ErrUnsupportedCodexVersion = errors.New("unsupported Codex CLI version")
	ErrIncompatibleUserAgent   = errors.New("incompatible Codex App Server user agent")
)

// AcceptedCodexVersion extracts the version from an already allowlisted CLI
// version line. The exact allowlist check deliberately lives here so callers
// cannot accidentally initialize an unreviewed App Server.
func AcceptedCodexVersion(cliOutput string) (string, error) {
	if strings.TrimSpace(cliOutput) != SupportedCLIOutput {
		return "", ErrUnsupportedCodexVersion
	}
	return SupportedCodexVersion, nil
}

// ValidateUserAgent verifies the integration/version token emitted by the
// pinned App Server. Only the first whitespace-delimited token is relevant;
// matching is otherwise byte exact.
func ValidateUserAgent(cliOutput, userAgent string) error {
	version, err := AcceptedCodexVersion(cliOutput)
	if err != nil {
		return err
	}
	fields := strings.Fields(userAgent)
	if len(fields) == 0 || fields[0] != "agent_romm/"+version {
		return ErrIncompatibleUserAgent
	}
	return nil
}
