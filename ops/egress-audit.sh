#!/usr/bin/env bash
set -euo pipefail
# 出网审计：只读取受管 daemon state 的精确 PID；连接快照不证明全部流量或零上行。
: "${QW_ROOT:?export QW_ROOT=/your/deployment/root}"
HERE=$(cd "$(dirname "$0")" && pwd)
WL_FILE=""
for candidate in "${QW_WHITELIST:-}" "$QW_ROOT/config/whitelist.json" "$HERE/../panel/config/whitelist.json" "$HERE/../panel/config/whitelist.example.json"; do
  [ -n "$candidate" ] && [ -f "$candidate" ] && WL_FILE=$candidate && break
done
[ -n "$WL_FILE" ] || { echo "未找到白名单 JSON（可设 QW_WHITELIST）"; exit 1; }

label() {
  python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get(sys.argv[2],"UNKNOWN-需人工判定"))' "$WL_FILE" "$1"
}

for region in cn intl; do
  state="$QW_ROOT/process-state/daemon-$region.json"
  pid=$(python3 - "$state" <<'PY'
import json
from pathlib import Path
import sys
path = Path(sys.argv[1])
if not path.is_file() or path.is_symlink():
    raise SystemExit(0)
try:
    value = json.loads(path.read_text())
except (OSError, ValueError):
    raise SystemExit(0)
if (isinstance(value, dict) and value.get('schemaVersion') == 1 and
        value.get('name') == path.stem and value.get('status') == 'running' and
        type(value.get('pid')) is int and value['pid'] > 0):
    print(value['pid'])
PY
)
  echo "== ${region^^} daemon pid=${pid:-unmanaged-or-stopped}（白名单: $WL_FILE） =="
  [ -n "$pid" ] || continue
  ss -tnp 2>/dev/null | grep "pid=$pid," | awk '{print $5}' |
    sed 's/:443$//' | sort | uniq -c | sort -rn |
    while read -r count ip; do
      printf '%4d x %-24s %s\n' "$count" "$ip" "$(label "$ip")"
    done
done
echo "== 回环连接不计入；此快照不代表历史或全部进程流量 =="
