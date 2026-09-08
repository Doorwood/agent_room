#!/bin/sh
# User installation/update entry point. Requires Node.js 20+ and npm.
set -eu
mode=install
case "$(basename "$0")" in agent_room-update) mode=update ;; esac
while [ "$#" -gt 0 ]; do
 case "$1" in
  --update) mode=update ;; --check) mode=check ;; --rollback) mode=rollback ;;
  --help) echo 'Usage: sh setup.sh [--update | --check | --rollback]'; exit 0 ;;
  *) echo "Unknown option: $1" >&2; exit 2 ;;
 esac
 shift
done
case "$(uname -s)/$(uname -m)" in
 Linux/x86_64|Linux/amd64|Linux/aarch64|Linux/arm64|Darwin/x86_64|Darwin/arm64) ;;
 *) echo 'Supported systems: macOS/Linux x64/arm64' >&2; exit 1 ;;
esac
command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1 || { echo 'Install Node.js 20+ and npm first.' >&2; exit 1; }
node -e 'if (+process.versions.node.split(".")[0] < 20) process.exit(1)' || { echo 'Node.js 20+ is required.' >&2; exit 1; }
install_root="$HOME/.local/share/agent_room/npm"
user_bin="$HOME/.local/bin"
registry=https://registry.npmjs.org/
current="$install_root/current"
if [ -f "$current/node_modules/menmu-agent-room/package.json" ]; then active_root=$current; else active_root=$install_root; fi
installed=$(node -e 'try {console.log(require(process.argv[1]).version)} catch {console.log("none")}' "$active_root/node_modules/menmu-agent-room/package.json")
active_command=$(command -v agent_room || true)
printf 'Current command: %s\nManaged installation: %s\n' "${active_command:-not on PATH}" "$installed"
if [ "$mode" = install ] && [ "$installed" = none ] && [ -n "$active_command" ]; then
 echo 'An existing installation was detected. Setup will add a user-managed installation and preserve the other package.'
fi
if [ "$mode" != check ]; then
 # Preserve unknown executables. Recognize our wrapper and npm launcher only.
 for entry in agent_room agent_room-update; do
 if [ -e "$user_bin/$entry" ] || [ -L "$user_bin/$entry" ]; then
  node -e 'const fs=require("fs");const p=process.argv[1];let s;try{if(fs.statSync(p).size>1048576)process.exit(1);s=fs.readFileSync(p,"utf8")}catch{process.exit(1)};if(!s.includes(".local/share/agent_room/npm") && !(s.includes("binaryPath") && s.includes("verifyBinary")))process.exit(1)' "$user_bin/$entry" || {
   echo "Unknown executable at $user_bin/$entry; move it to a backup path before installing. Nothing was replaced." >&2; exit 1;
  }
 fi
 done
fi
if [ "$mode" != rollback ]; then
 latest=$(npm view menmu-agent-room dist-tags.latest --registry="$registry" --prefer-online --fetch-retries=1 --fetch-timeout=15000) || {
  echo 'Cannot check the official npm registry. Check your network; existing installation is unchanged.' >&2; exit 1;
 }
 node -e 'if(!/^\d+\.\d+\.\d+$/.test(process.argv[1]))process.exit(1)' "$latest" || { echo 'Invalid stable registry version.' >&2; exit 1; }
 printf 'Latest: %s\n' "$latest"
fi
if [ "$mode" = check ]; then exit 0; fi
if [ "$mode" != install ] && [ "$installed" = none ]; then echo 'Run setup first to configure a managed installation.' >&2; exit 1; fi
mkdir -p "$install_root" "$user_bin"
lock="$install_root/.update-lock"
if ! mkdir "$lock" 2>/dev/null; then echo "Another update is in progress. If it was interrupted, verify no updater is running before removing $lock." >&2; exit 1; fi
stage=''
cleanup() { if [ -n "$stage" ]; then rm -rf "$stage"; fi; rmdir "$lock"; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
# Atomic symlink replacement, including macOS where mv follows directory symlinks.
switch_to() {
 node -e 'const fs=require("fs"),path=require("path");const [root,target]=process.argv.slice(1);const tmp=path.join(root,".pointer-"+process.pid);try{fs.symlinkSync(target,tmp);fs.renameSync(tmp,path.join(root,"current"))}finally{try{fs.unlinkSync(tmp)}catch{}}' "$install_root" "$1"
}
if [ "$mode" = rollback ]; then
 [ -L "$install_root/previous" ] || { echo 'No previous managed installation is available.' >&2; exit 1; }
 previous=$(node -e 'console.log(require("fs").realpathSync(process.argv[1]))' "$install_root/previous")
 node "$previous/node_modules/menmu-agent-room/bin/agent_room.cjs" help >/dev/null
 switch_to "$previous"
 echo 'Previous version restored. Restart your local clients to use it.'
 exit 0
fi
if [ "$installed" != "$latest" ]; then
 if [ "$installed" != none ]; then
  node -e 'const a=process.argv[1].split(".").map(Number),b=process.argv[2].split(".").map(Number);for(let i=0;i<3;i++){if(a[i]>b[i])process.exit(1);if(a[i]<b[i])break}' "$installed" "$latest" || { echo 'Installed version is newer; no downgrade performed.'; exit 0; }
 fi
 mkdir -p "$install_root/releases"
 stage=$(mktemp -d "$install_root/releases/.stage-XXXXXX")
 npm install --prefix "$stage" --registry="$registry" --ignore-scripts --no-audit --no-fund --fetch-retries=1 --fetch-timeout=30000 "menmu-agent-room@$latest"
 # The npm launcher validates the native checksum before starting help.
 node "$stage/node_modules/menmu-agent-room/bin/agent_room.cjs" help >/dev/null
 node -e 'if(require(process.argv[1]).version!==process.argv[2])process.exit(1)' "$stage/node_modules/menmu-agent-room/package.json" "$latest"
 if [ "${latest%%.*}" -ge 1 ]; then
  node "$stage/node_modules/menmu-agent-room/bin/agent_room.cjs" --version --json > "$stage/version.json"
  node -e 'if(require(process.argv[1]).version!==process.argv[2])process.exit(1)' "$stage/version.json" "$latest"
 fi
 # Keep the prior entry for explicit rollback. Never remove a running version.
 if [ "$installed" != none ]; then
  old_root=$(node -e 'console.log(require("fs").realpathSync(process.argv[1]))' "$active_root")
  node -e 'const fs=require("fs"),p=require("path");const [root,target]=process.argv.slice(1),tmp=p.join(root,".previous-"+process.pid);try{fs.symlinkSync(target,tmp);fs.renameSync(tmp,p.join(root,"previous"))}finally{try{fs.unlinkSync(tmp)}catch{}}' "$install_root" "$old_root"
 fi
 release="$install_root/releases/$latest-$(basename "$stage" | cut -c8-)"
 mv "$stage" "$release"
 stage=''
 switch_to "$release"
fi
# This wrapper reads current at startup. 0.1.3 legacy installs remain a fallback.
stage=$(mktemp -d "$user_bin/.agent-room-setup.XXXXXX")
cat > "$stage/agent_room" <<'WRAPPER'
#!/bin/sh
root="$HOME/.local/share/agent_room/npm"
if [ -f "$root/current/node_modules/menmu-agent-room/bin/agent_room.cjs" ]; then root="$root/current"; fi
exec node "$root/node_modules/menmu-agent-room/bin/agent_room.cjs" "$@"
WRAPPER
# Prefer the newly installed updater so future bug fixes take effect too.
updater="$install_root/current/node_modules/menmu-agent-room/scripts/setup.sh"
if [ -f "$updater" ] && grep -Fq .update-lock "$updater"; then cp "$updater" "$stage/agent_room-update"; else cp "$0" "$stage/agent_room-update"; fi
chmod 755 "$stage/agent_room" "$stage/agent_room-update"
mv -f "$stage/agent_room" "$user_bin/agent_room"
mv -f "$stage/agent_room-update" "$user_bin/agent_room-update"
if [ "$mode" = install ]; then
 zsh_root=${ZDOTDIR:-$HOME};mkdir -p "$zsh_root"
 bash_login="$HOME/.profile"
 if [ -f "$HOME/.bash_profile" ]; then bash_login="$HOME/.bash_profile"; elif [ -f "$HOME/.bash_login" ]; then bash_login="$HOME/.bash_login"; fi
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
printf '\nInstalled managed version: %s\nCommand: %s/agent_room\n' "$latest" "$user_bin"
echo 'Restart running answers/join/dashboard clients to use this version; do not restart the host for a client-only UI update.'
echo 'For this terminal: export PATH="$HOME/.local/bin:$PATH"'
echo 'Verify: command -v agent_room; agent_room --version; agent_room doctor'
echo 'Open your rooms: agent_room dashboard'
echo 'Check: agent_room-update --check | Upgrade: agent_room-update | Restore: agent_room-update --rollback'
