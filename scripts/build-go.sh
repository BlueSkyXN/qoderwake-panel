#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
npm --prefix web ci --ignore-scripts --no-audit --no-fund
npm --prefix web run build
mkdir -p build
PANEL_TARGET_OS="${GOOS:-$(go env GOOS)}"
PANEL_TARGET_ARCH="${GOARCH:-$(go env GOARCH)}"
CGO_ENABLED=0 GOOS="$PANEL_TARGET_OS" GOARCH="$PANEL_TARGET_ARCH" go build -trimpath -o "build/panel-${PANEL_TARGET_OS}-${PANEL_TARGET_ARCH}" ./cmd/panel
