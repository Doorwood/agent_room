package codex

import (
	"errors"
	"strings"
)

const (
	SupportedCodexVersion = "0.151.0-alpha.7.2"
	SupportedCLIOutput    = "codex-cli " + SupportedCodexVersion
	SupportedSchemaSHA256 = "31ae67beb2c94cc9509f6a71968600062dc8c6d7fe45437ed3a9129838f4d2d9"
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
