#!/usr/bin/env bash
set -euo pipefail
# 受管 direct 模式：不设置 gateway 端点，不使用 fixture CA 或实验认证开关。
: "${QW_ROOT:?export QW_ROOT first}"
HERE=$(cd "$(dirname "$0")" && pwd)
export QW_DAEMON_MODE=direct
export QODERWAKE_HOT_DEPLOY="${QODERWAKE_HOT_DEPLOY:-0}"
export QODER_MEMORY_DISABLE_EMBEDDING="${QODER_MEMORY_DISABLE_EMBEDDING:-1}"
exec bash "$HERE/qw-ctl.sh" cn start
