# agent_room

A Linux host creates a shared project session. Members on macOS or Linux request access using its IP and session ID, then share a Codex task queue, terminal and browser conversation after host approval.

## Install with npm

Recommended user setup (Node.js 20+, macOS/Linux, no sudo):

```sh
npm exec --yes --registry=https://registry.npmjs.org/ --package=menmu-agent-room@latest -- agent_room-setup
export PATH="$HOME/.local/bin:$PATH"
agent_room dashboard
```

The setup command ships inside the npm package; no GitHub script download is
required. In a source checkout or extracted bundle, use `sh scripts/setup.sh`.
Setup configures Bash/Zsh and preserves existing global installations. Use
`agent_room doctor` to identify the command and version currently in use.

```sh
agent_room --version
agent_room-update --check
agent_room-update
agent_room-update --rollback
```

Updates validate a staged package before switching the managed entry point.
Failed updates keep the old version. Running clients need to be restarted;
updates do not stop hosts or cancel tasks. No background updates are scheduled.

Alternative: global npm installation (use the same npm prefix for updates):

Requires Node.js 20+ and npm:

```sh
npm install -g menmu-agent-room
agent_room help
```

This archive includes macOS/Linux x64/arm64 native binaries. It requires no Go compiler, install scripts, or additional download during installation. The launcher selects the platform and checks its SHA-256 before running. `--ignore-scripts` installations also work.

The npm package name is `menmu-agent-room`; the installed command is `agent_room`. For offline installation, use `npm install -g ./menmu-agent-room-1.0.4.tgz`. To install without administrator access, add `--prefix "$HOME/.local"` and put `$HOME/.local/bin` on PATH.

## Host (Linux)

The host also needs the compatible Codex runtime and a Codex login:

```sh
npm install -g '@openai/codex@0.153.4'
codex --version
codex login
cd /path/to/your/git/project
agent_room host .
```

If the host already has this runtime and login, reuse them. Every host startup
finds Codex on the current PATH and validates compatibility. It does not prefer
private runtime folders or override the model/reasoning effort: those come from
the selected Codex's environment and configuration, including CODEX_HOME.
Missing or unreviewed runtimes fail startup. Members need neither Codex nor a
host OS account. Work runs with the host execution user's authority.

Existing rooms created with the older runtime need a stopped-host backup and
metadata migration; see [runtime upgrade instructions](https://github.com/Doorwood/agent_room/blob/main/docs/runtime-upgrade.md).

## Join and open the browser (member's computer)

Copy the actual host address, including its port, and full session ID:

```sh
agent_room join HOST_IP SESSION_ID --name alice --answers
```

The host approves in another terminal in the same project:

```sh
agent_room requests
agent_room approve REQUEST_ID
```

Already connected in a terminal? Open a separate browser client with:

```sh
agent_room answers HOST_IP SESSION_ID --name alice
```

Startup prints `Browser URL: http://127.0.0.1:<local-port>/<random-access-id>/`. This address is generated on each member's own computer; it cannot be assembled from the host IP and session ID. Keep the client running. Add `--no-open` to print the URL without launching a browser.

The browser supports sending tasks, shared member history, member filters, progress grouped by task, and final answers with collapsible progress. Press Enter to send a message or Alt+Enter to insert a newline. The original terminal remains available.

## Update or uninstall

```sh
npm install -g menmu-agent-room@latest
npm uninstall -g menmu-agent-room
```

Uninstalling the CLI keeps host sessions and client credentials. Native hosts currently running are not replaced until they restart.

## Local room dashboard

Run `agent_room dashboard` once and keep its terminal running. The local page
lists saved member identities and local host projects. Add a host address,
complete session ID and nickname, then connect; first-time members still need
host approval. Open the conversation from the room card. Disconnect only
closes that dashboard's connection, preserving membership and submitted tasks.
Closing the browser tab keeps connections alive; stopping the dashboard process
closes its connections. Rooms remain available next time, without auto-connecting.
Connections opened in other terminals are not controlled by this dashboard.

For a host using a custom state directory from an older version, use
`agent_room dashboard --host-state /absolute/state`. The dashboard does not
start or stop hosts, create host projects, or grant membership approvals.
Use `--no-open` to print the local URL without opening a browser.

The chat page shows its local client version, renders Markdown safely, and
supports code-block copying. Press Enter to send or Alt+Enter for a newline.

## Removing a managed installation

Managed and global npm installations are separate. `npm uninstall -g
menmu-agent-room` removes the global package only. To remove the managed
installation, stop its local clients, remove its recognized `~/.local/bin/agent_room`
and `~/.local/bin/agent_room-update` wrappers, and remove only
`~/.local/share/agent_room/npm`. Keep the surrounding agent_room directory and
user configuration directory to preserve host state and member credentials.
The marked PATH block can remain if you use `~/.local/bin` for other tools.
