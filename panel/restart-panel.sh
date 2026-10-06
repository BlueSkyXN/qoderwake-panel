#!/usr/bin/env bash
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
set -a
[ ! -f "$HERE/panel.env" ] || . "$HERE/panel.env"
set +a
: "${QW_ROOT:?Set QW_ROOT in panel.env}"
export QW_BIND="${QW_BIND:-127.0.0.1}"
export QW_PORT="${QW_PORT:-19831}"
export QW_HOME="${QW_HOME:-$QW_ROOT/test-home}"
export QODERWAKE_HOT_DEPLOY="${QODERWAKE_HOT_DEPLOY:-0}"
export QODER_MEMORY_DISABLE_EMBEDDING="${QODER_MEMORY_DISABLE_EMBEDDING:-1}"
CONTROL="$HERE/../ops/process-control.py"
[ -f "$CONTROL" ] || CONTROL="$HERE/process-control.py"
[ -f "$CONTROL" ] || { echo '{"ok":false,"error":"process_controller_missing"}'; exit 1; }
PYTHON=$(command -v python3)
HEALTH_ARGS=(--health-url "http://127.0.0.1:$QW_PORT/api/health")
if [ -n "${QW_TLS_CERT:-}" ] || [ -n "${QW_TLS_KEY:-}" ]; then
  [ -n "${QW_TLS_CERT:-}" ] && [ -n "${QW_TLS_KEY:-}" ] || { printf '%s\n' '{"ok":false,"error":"both_tls_files_required"}'; exit 1; }
  HEALTH_ARGS=(--health-url "https://127.0.0.1:$QW_PORT/api/health")
  [ -z "${QW_HEALTH_CA_FILE:-}" ] || HEALTH_ARGS+=(--health-ca-file "$QW_HEALTH_CA_FILE")
  [ -z "${QW_HEALTH_SERVER_NAME:-}" ] || HEALTH_ARGS+=(--health-server-name "$QW_HEALTH_SERVER_NAME")
fi
mkdir -p "$QW_ROOT/logs"
exec "$PYTHON" "$CONTROL" start \
  --root "$QW_ROOT" \
  --name panel \
  --profile panel \
  --launch "$PYTHON" \
  --script "$HERE/qoderwake-panel.py" \
  --home "$QW_HOME" \
  --port "$QW_PORT" \
  --mode panel \
  --log "$QW_ROOT/logs/qw-panel.log" \
  "${HEALTH_ARGS[@]}" \
  --expected-version 0.12.2
