#!/usr/bin/env bash
set -euo pipefail
# 受管 gateway 模式：只声明启动模式和 loopback 端点，进程身份由 process-control.py 核验。
: "${QW_ROOT:?export QW_ROOT first}"
HERE=$(cd "$(dirname "$0")" && pwd)
export QW_DAEMON_MODE=gateway
export QW_GW_URL="${QW_GW_URL:-http://127.0.0.1:${QW_GW_PORT:-19840}}"
export QODER_SDK_CUSTOM_BASE_URL_BYOK="${QODER_SDK_CUSTOM_BASE_URL_BYOK:-1}"
export QODERWAKE_HOT_DEPLOY="${QODERWAKE_HOT_DEPLOY:-0}"
export QODER_MEMORY_DISABLE_EMBEDDING="${QODER_MEMORY_DISABLE_EMBEDDING:-1}"
exec bash "$HERE/qw-ctl.sh" cn start
