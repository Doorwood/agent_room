#!/bin/sh
# Install a verified bundled binary, or build from a local source checkout.
set -eu
prefix="${HOME}/.local/bin"
with_codex=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --prefix) [ "$#" -ge 2 ] || { echo '--prefix requires a directory' >&2; exit 2; }; prefix=$2; shift 2 ;;
    --with-codex) with_codex=1; shift ;;
    --help) echo 'Usage: sh scripts/install.sh [--prefix DIR] [--with-codex]'; exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
case "$(uname -s)" in Darwin) os=darwin ;; Linux) os=linux ;; *) echo 'Supported systems: macOS and Linux' >&2; exit 1 ;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) echo 'Supported architectures: arm64 and amd64' >&2; exit 1 ;; esac
mkdir -p "$prefix"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/agent-room-install.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
binary="agent_room-${os}-${arch}"
if [ -f "$root/dist/$binary" ]; then
  [ -f "$root/dist/SHA256SUMS" ] || { echo 'Missing checksum manifest' >&2; exit 1; }
  expected=$(awk -v name="$binary" '$2 == name {print $1}' "$root/dist/SHA256SUMS")
  [ "${#expected}" -eq 64 ] || { echo 'Missing or invalid binary checksum' >&2; exit 1; }
  if command -v sha256sum >/dev/null 2>&1; then actual=$(sha256sum "$root/dist/$binary" | awk '{print $1}')
  else actual=$(shasum -a 256 "$root/dist/$binary" | awk '{print $1}'); fi
  [ "$actual" = "$expected" ] || { echo 'Binary checksum mismatch' >&2; exit 1; }
  cp "$root/dist/$binary" "$tmp/agent_room"
else
  command -v go >/dev/null 2>&1 || { echo 'This source install needs Go 1.24+. Use the binary bundle for installation without Go.' >&2; exit 1; }
  (cd "$root" && go build -trimpath -o "$tmp/agent_room" ./cmd/agent_romm)
fi
# Stage in the destination directory so replacement is atomic on its filesystem.
staged=$(mktemp "$prefix/.agent-room.XXXXXX")
if ! cp "$tmp/agent_room" "$staged" || ! chmod 755 "$staged" || ! mv -f "$staged" "$prefix/agent_room"; then
  rm -f "$staged"; exit 1
fi
if [ "$with_codex" -eq 1 ]; then
  [ "$os" = linux ] || { echo 'Codex host runtime requires Linux.' >&2; exit 1; }
  command -v npm >/dev/null 2>&1 || { echo 'Install Node.js/npm, then rerun with --with-codex.' >&2; exit 1; }
  (cd "$tmp" && npm install --prefix "$HOME/.local/share/agent_room/codex" --registry=https://registry.npmjs.org --no-audit --no-fund '@openai/codex@0.153.4')
  echo 'Host runtime installed. If needed, log in with:'
  echo '  ~/.local/share/agent_room/codex/node_modules/.bin/codex login'
  echo 'To use this runtime, add it to the host environment before starting:'
  echo '  export PATH="$HOME/.local/share/agent_room/codex/node_modules/.bin:$PATH"'
fi
"$prefix/agent_room" help
printf '\nInstalled: %s/agent_room\n' "$prefix"
case ":$PATH:" in *":$prefix:"*) ;; *) printf 'Add to this terminal: export PATH="%s:$PATH"\n' "$prefix" ;; esac
printf 'Host: agent_room host /path/to/project\nJoin: agent_room join HOST_IP SESSION_ID --name YOUR_NAME\n'
