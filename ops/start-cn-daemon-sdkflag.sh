#!/usr/bin/env bash
set -euo pipefail
: "${QW_ROOT:?export QW_ROOT first}"
HERE=$(cd "$(dirname "$0")" && pwd)
export QW_DAEMON_MODE=direct
export QODER_SDK_CUSTOM_BASE_URL_BYOK="${QODER_SDK_CUSTOM_BASE_URL_BYOK:-1}"
export QODERWAKE_HOT_DEPLOY="${QODERWAKE_HOT_DEPLOY:-0}"
export QODER_MEMORY_DISABLE_EMBEDDING="${QODER_MEMORY_DISABLE_EMBEDDING:-1}"
exec bash "$HERE/qw-ctl.sh" cn start
