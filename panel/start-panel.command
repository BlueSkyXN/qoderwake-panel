#!/bin/bash
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
set -a
[ ! -f "$HERE/panel.env" ] || . "$HERE/panel.env"
set +a
PANEL_PYTHON="${QW_PYTHON:-$(command -v python3 || true)}"
if [ -z "$PANEL_PYTHON" ]; then
  echo '未找到 Python。请将 QW_PYTHON 指向已有的 Python 3.10+。' >&2
  exit 1
fi
if [ "$PANEL_PYTHON" = /usr/bin/python3 ] && [ "$(uname -s)" = Darwin ] && ! xcode-select -p >/dev/null 2>&1; then
  echo '系统 Python 尚不可用。请将 QW_PYTHON 指向已有的 Python 3.10+，再启动 Panel。' >&2
  exit 1
fi
exec "$PANEL_PYTHON" "$HERE/run-panel.py" "$@"
