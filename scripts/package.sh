#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
mkdir -p dist
for os in linux darwin; do
  for arch in amd64 arm64; do
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -o "dist/agent_room-${os}-${arch}" ./cmd/agent_romm
  done
done
(cd dist && shasum -a 256 agent_room-linux-amd64 agent_room-linux-arm64 agent_room-darwin-amd64 agent_room-darwin-arm64 > SHA256SUMS)
COPYFILE_DISABLE=1 tar --no-xattrs -czf dist/agent-room-bundle.tar.gz scripts/install.sh scripts/setup.sh dist/agent_room-linux-amd64 dist/agent_room-linux-arm64 dist/agent_room-darwin-amd64 dist/agent_room-darwin-arm64 dist/SHA256SUMS README.md docs/QUICKSTART.zh-CN.md docs/legacy-core-mvp.md
printf 'Bundle: %s/dist/agent-room-bundle.tar.gz\n' "$root"
