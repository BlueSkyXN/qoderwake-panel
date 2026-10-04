#!/usr/bin/env bash
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
set -a
[ ! -f "$HERE/../panel/panel.env" ] || . "$HERE/../panel/panel.env"
[ ! -f "$HERE/panel.env" ] || . "$HERE/panel.env"
set +a
: "${QW_ROOT:?Set QW_ROOT in panel.env or the environment}"
export QW_GW_CONFIG="${QW_GW_CONFIG:-$QW_ROOT/config/uplink-gw.json}"
exec python3 -B "$HERE/gateway-manager.py" "${1:-status}"
