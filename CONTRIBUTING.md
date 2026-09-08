# Contributing

Open an issue to discuss substantial changes before implementation. Keep pull
requests focused and include regression tests for changed behavior.

Use Go 1.24 or newer and Git. Run these checks before submitting:

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/agent_romm
```

Tests require local socket access. Default tests use a fake Codex App Server;
do not use real model credentials or paid execution in automated tests.
Production hosting requires Linux. Describe which platforms you tested.

Never commit credentials, room state, transcripts, deployment details, or
private logs. See SECURITY.md for the trust model and vulnerability reporting.
Contributions are made under the project's MIT license.
