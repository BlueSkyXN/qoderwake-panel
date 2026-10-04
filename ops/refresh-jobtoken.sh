#!/usr/bin/env bash
set -euo pipefail
# 刷新派生 job token：受管停止 -> 删除两项派生缓存 -> 按原受管模式启动。
: "${QW_ROOT:?export QW_ROOT first}"
HERE=$(cd "$(dirname "$0")" && pwd)
STATE="$QW_ROOT/process-state/daemon-cn.json"
readarray -t VALUES < <(python3 - "$STATE" "${QW_CN_HOME:-$QW_ROOT/test-home}" <<'PY'
import json
from pathlib import Path
import sys
path = Path(sys.argv[1])
home = str(Path(sys.argv[2]).resolve(strict=False))
if not path.is_file() or path.is_symlink():
    raise SystemExit('managed daemon state missing')
value = json.loads(path.read_text())
if (not isinstance(value, dict) or value.get('schemaVersion') != 1 or
        value.get('name') != 'daemon-cn' or value.get('status') != 'running' or
        value.get('home') != home or value.get('mode') not in ('direct', 'gateway')):
    raise SystemExit('managed daemon state invalid')
print(value['mode'])
print(value.get('endpoint') or '')
PY
)
export QW_DAEMON_MODE="${VALUES[0]}"
[ "$QW_DAEMON_MODE" != gateway ] || export QW_GW_URL="${VALUES[1]}"
bash "$HERE/qw-ctl.sh" cn stop
HOME_DIR="${QW_CN_HOME:-$QW_ROOT/test-home}"
rm -f -- "$HOME_DIR/.auth/job_token" "$HOME_DIR/.auth/conversation-session-claims"
echo cache_cleared
bash "$HERE/qw-ctl.sh" cn start
[ -f "$HOME_DIR/.auth/job_token" ] && echo jt_recreated || echo jt_absent_yet
