# Codex runtime upgrades

The reviewed runtime is Codex CLI 0.153.4. Host startup looks up `codex` in
the current PATH on every invocation, resolves it to an absolute path, then
validates its version and App Server identity. Missing or incompatible runtimes
fail startup. Private installation directories do not override PATH.

No model or reasoning-effort override is sent by agent_room. Configure the
selected Codex in the same environment used to start the host, including
CODEX_HOME and project configuration. A tmux session may have a different PATH
or proxy environment from a newly opened login shell.

## Existing rooms

This release does not automatically migrate runtime metadata. Before upgrading:

1. Stop the host gracefully and verify that it and its owned App Server exited.
2. Back up the entire state directory with private permissions. Keep this backup
   outside Git; it includes credentials, certificates, and conversation data.
3. Verify that the new executable reports `codex-cli 0.153.4` and that the
   generated schema matches `internal/codex/testdata/schema-0.153.4.sha256`.
4. With the host still stopped, update only `codexVersion` and `schemaSha256`
   in `private/config.json` to `codex-cli 0.153.4` and
   `e8284c5cb8157554a3dd1e035aadbd4325aea501af56887e9c2e12eb1b9b9448`.
   Use an atomic replacement and retain mode 0600. Do not change IDs, paths,
   members, certificates, or the SQLite database.
5. Start the upgraded host from an environment with the intended Codex on PATH.
   Verify the same session ID, membership, thread recovery, and a real turn.

The old stopped checkpoint is retained as historical evidence; the new runtime
writes its own checkpoint on startup. Do not manually rewrite process IDs.

Do not run an older host against the upgraded state. If rollback is necessary,
stop the upgraded host first and preserve its state separately before restoring
the pre-upgrade backup with the old host/runtime pair. Restoring the backup alone
does not retain messages accepted after that backup.

Older design documents and the older schema fingerprint remain as historical
references, not the current executable contract.
