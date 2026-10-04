#!/usr/bin/env bash
set -euo pipefail
# QoderWake Linux 受管控制：qw-ctl.sh <cn|intl|all> <start|stop|status|health>
: "${QW_ROOT:?export QW_ROOT=/your/deployment/root}"
HERE=$(cd "$(dirname "$0")" && pwd)
export QODER_SDK_CUSTOM_BASE_URL_BYOK="${QODER_SDK_CUSTOM_BASE_URL_BYOK:-1}"
export QODERWAKE_HOT_DEPLOY="${QODERWAKE_HOT_DEPLOY:-0}"
export QODER_MEMORY_DISABLE_EMBEDDING="${QODER_MEMORY_DISABLE_EMBEDDING:-1}"
export QODER_ENV="${QODER_ENV:-prod}"
CONTROL="$HERE/process-control.py"
[ -f "$CONTROL" ] || { echo '{"ok":false,"error":"process_controller_missing"}'; exit 1; }
PYTHON=$(command -v python3)
mkdir -p "$QW_ROOT/logs"

regions() {
  case "$1" in
    cn|intl) printf '%s\n' "$1" ;;
    all) printf '%s\n' cn intl ;;
    *) echo "usage: qw-ctl.sh <cn|intl|all> <start|stop|status|health>" >&2; return 2 ;;
  esac
}

value_for() {
  local region=$1 kind=$2
  case "$region:$kind" in
    cn:bin) printf '%s\n' "${QW_CN_BIN:-qoderwake-cn}" ;;
    cn:home) printf '%s\n' "${QW_CN_HOME:-$QW_ROOT/test-home}" ;;
    cn:port) printf '%s\n' "${QW_CN_PORT:-19830}" ;;
    intl:bin) printf '%s\n' "${QW_INTL_BIN:-qoderwake}" ;;
    intl:home) printf '%s\n' "${QW_INTL_HOME:-$QW_ROOT/intl-home}" ;;
    intl:port) printf '%s\n' "${QW_INTL_PORT:-19820}" ;;
  esac
}

control_one() {
  local region=$1 action=$2 bin home port mode endpoint
  bin=$(value_for "$region" bin)
  home=$(value_for "$region" home)
  port=$(value_for "$region" port)
  mode=${QW_DAEMON_MODE:-direct}
  endpoint=${QW_GW_URL:-http://127.0.0.1:${QW_GW_PORT:-19840}}
  case "$mode" in
    direct|gateway) ;;
    *) echo '{"ok":false,"error":"invalid_daemon_mode"}'; return 1 ;;
  esac
  bin=$(command -v "$bin" 2>/dev/null || true)
  [ -n "$bin" ] || { echo '{"ok":false,"error":"daemon_binary_missing"}'; return 1; }
  local args=(
    "$PYTHON" "$CONTROL" "$action"
    --root "$QW_ROOT"
    --name "daemon-$region"
    --profile daemon
    --launch "$bin"
    --home "$home"
    --port "$port"
    --mode "$mode"
    --log "$QW_ROOT/logs/daemon-$region-$mode.log"
    --health-url "http://127.0.0.1:$port/api/health"
  )
  [ "$mode" != gateway ] || args+=(--endpoint "$endpoint")
  "${args[@]}"
}

health_one() {
  local region=$1 bin home
  control_one "$region" status
  bin=$(value_for "$region" bin)
  home=$(value_for "$region" home)
  bin=$(command -v "$bin" 2>/dev/null || true)
  [ -n "$bin" ] || return 1
  QODERWAKE_HOME="$home" timeout 30 "$bin" whoami 2>&1 |
    grep -E 'Name|Token expires' | sed "s/^/$region /" || true
  local output
  output=$(QODERWAKE_HOME="$home" timeout 30 "$bin" models list 2>/dev/null || true)
  printf '%s models_lines=%s custom=%s\n' "$region" \
    "$(printf '%s\n' "$output" | wc -l | tr -d ' ')" \
    "$(printf '%s\n' "$output" | grep -c qoder-custom || true)"
}

[ "$#" -eq 2 ] || { echo "usage: qw-ctl.sh <cn|intl|all> <start|stop|status|health>" >&2; exit 2; }
case "$2" in
  start|stop|status)
    while IFS= read -r region; do control_one "$region" "$2"; done < <(regions "$1")
    ;;
  health)
    while IFS= read -r region; do health_one "$region"; done < <(regions "$1")
    ;;
  *) echo "usage: qw-ctl.sh <cn|intl|all> <start|stop|status|health>" >&2; exit 2 ;;
esac
