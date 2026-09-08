#!/bin/sh
# Standalone user installation and update entry point (macOS/Linux, Bash/Zsh).
set -eu
mode=install
case "$(basename "$0")" in agent_room-update) mode=update ;; esac
while [ "$#" -gt 0 ]; do
  case "$1" in
    --update) mode=update ;;
    --check) mode=check ;;
    --help) echo 'Usage: sh setup.sh [--update | --check]'; exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done
case "$(uname -s)/$(uname -m)" in
  Linux/x86_64|Linux/amd64|Linux/aarch64|Linux/arm64|Darwin/x86_64|Darwin/arm64) ;;
  *) echo 'Supported systems: macOS/Linux x64/arm64' >&2; exit 1 ;;
esac
command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1 || {
  echo 'Install Node.js 20+ and npm first, then rerun this script.' >&2; exit 1;
}
node -e 'if (Number(process.versions.node.split(".")[0]) < 20) process.exit(1)' || {
  echo 'Node.js 20+ is required.' >&2; exit 1;
}
install_root="$HOME/.local/share/agent_room/npm"
user_bin="$HOME/.local/bin"
registry=https://registry.npmjs.org/
installed=$(node -e 'try { console.log(require(process.argv[1]).version) } catch { console.log("none") }' "$install_root/node_modules/menmu-agent-room/package.json")
latest=$(npm view menmu-agent-room dist-tags.latest --registry="$registry" --prefer-online)
# Validate registry output before using it as a package selector.
node -e 'if (!/^\d+\.\d+\.\d+$/.test(process.argv[1])) process.exit(1)' "$latest" || {
  echo 'Registry returned an invalid stable version.' >&2; exit 1;
}
printf 'Installed: %s\nLatest: %s\n' "$installed" "$latest"
if [ "$mode" = check ]; then exit 0; fi
if [ "$mode" = update ] && [ "$installed" = none ]; then
  echo 'Run the setup script first to install agent_room.' >&2; exit 1
fi
if [ "$installed" != "$latest" ]; then
  # Never downgrade a newer installation if the registry rolls latest back.
  if [ "$installed" != none ]; then
    node -e 'const a=process.argv[1].split(".").map(Number), b=process.argv[2].split(".").map(Number); for(let i=0;i<3;i++){if(a[i]>b[i])process.exit(1);if(a[i]<b[i])break;}' "$installed" "$latest" || {
      echo 'Installed version is newer than latest; leaving it unchanged.'; exit 0;
    }
  fi
  mkdir -p "$install_root"
  npm install --prefix "$install_root" --registry="$registry" --ignore-scripts --no-audit --no-fund "menmu-agent-room@$latest"
fi
node "$install_root/node_modules/menmu-agent-room/bin/agent_room.cjs" help
if [ "$mode" = install ]; then
  mkdir -p "$user_bin"
  # Both entry points are staged beside their destination for atomic replacement.
  stage=$(mktemp -d "$user_bin/.agent-room-setup.XXXXXX")
  trap 'rm -rf "$stage"' EXIT HUP INT TERM
  cat > "$stage/agent_room" <<'WRAPPER'
#!/bin/sh
exec node "$HOME/.local/share/agent_room/npm/node_modules/menmu-agent-room/bin/agent_room.cjs" "$@"
WRAPPER
  cp "$0" "$stage/agent_room-update"
  chmod 755 "$stage/agent_room" "$stage/agent_room-update"
  mv -f "$stage/agent_room" "$user_bin/agent_room"
  mv -f "$stage/agent_room-update" "$user_bin/agent_room-update"
  # Cover interactive shells and login shells. Respect Zsh's configured directory.
  zsh_root=${ZDOTDIR:-$HOME}
  mkdir -p "$zsh_root"
  bash_login="$HOME/.profile"
  if [ -f "$HOME/.bash_profile" ]; then bash_login="$HOME/.bash_profile"
  elif [ -f "$HOME/.bash_login" ]; then bash_login="$HOME/.bash_login"; fi
  for rc in "$bash_login" "$HOME/.bashrc" "$zsh_root/.zprofile" "$zsh_root/.zshrc"; do
    if ! test -f "$rc" || ! grep -Fq '# agent_room user PATH' "$rc"; then
      cat >> "$rc" <<'PROFILE'

# agent_room user PATH
case ":$PATH:" in
  *":$HOME/.local/bin:"*) ;;
  *) export PATH="$HOME/.local/bin:$PATH" ;;
esac
PROFILE
    fi
  done
fi
printf '\nagent_room %s is ready. Restart running clients to use the updated version.\n' "$latest"
if [ "$mode" = install ]; then
  echo 'New Bash/Zsh terminals will find agent_room automatically.'
  echo 'For this terminal, run: export PATH="$HOME/.local/bin:$PATH"'
  echo 'Check updates: agent_room-update --check'
  echo 'Upgrade to latest: agent_room-update'
fi
