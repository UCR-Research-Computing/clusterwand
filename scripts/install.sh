#!/usr/bin/env bash
# Build and install clusterwand (plus the `wand` alias) to ~/.local/bin with the version stamped in.
# Usage: scripts/install.sh            (version from git describe)
set -euo pipefail
cd "$(dirname "$0")/.."
export GOTOOLCHAIN=${GOTOOLCHAIN:-go1.26.8}
VERSION=$(git describe --tags --always --dirty --match 'v[0-9]*' 2>/dev/null || echo dev)
mkdir -p "$HOME/.local/bin"
go build -trimpath -ldflags "-s -w -X github.com/UCR-Research-Computing/clusterwand/internal/version.Version=${VERSION}" \
  -o "$HOME/.local/bin/clusterwand" ./cmd/clusterwand
ln -sf clusterwand "$HOME/.local/bin/wand"
echo "installed $HOME/.local/bin/clusterwand ${VERSION} (alias: wand)"
"$HOME/.local/bin/clusterwand" version
