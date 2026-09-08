# agent_room

A Linux host creates a shared project session. Members on macOS or Linux request access using its IP and session ID, then share a Codex task queue, terminal and browser conversation after host approval.

## Install with npm

Requires Node.js 20+ and npm:

```sh
npm install -g menmu-agent-room
agent_room help
```

This archive includes macOS/Linux x64/arm64 native binaries. It requires no Go compiler, install scripts, or additional download during installation. The launcher selects the platform and checks its SHA-256 before running. `--ignore-scripts` installations also work.

The npm package name is `menmu-agent-room`; the installed command is `agent_room`. For offline installation, use `npm install -g ./menmu-agent-room-0.1.1.tgz`. To install without administrator access, add `--prefix "$HOME/.local"` and put `$HOME/.local/bin` on PATH.

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

The browser supports sending tasks, shared member history, member filters, progress grouped by task, and final answers with collapsible progress. The original terminal remains available.

## Update or uninstall

```sh
npm install -g menmu-agent-room@latest
npm uninstall -g menmu-agent-room
```

Uninstalling the CLI keeps host sessions and client credentials. Native hosts currently running are not replaced until they restart.
