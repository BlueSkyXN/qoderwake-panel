#!/usr/bin/env python3
"""Read-only snapshot hygiene check; never copies, commits or publishes."""
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
REQUIRED = ['README.md','LICENSE','.gitignore','panel/qoderwake-panel.py','panel/panel_security.py',
            'panel/static/index.html','panel/static/panel.js','panel/static/management.js','panel/static/advanced.js','panel/static/panel.css',
            'panel/panel_patches.py','panel/provider_config.py','panel/provider_transport.py','panel/daemon_transport.py','panel/deletion_preflight.py','panel/gateway_runtime.py','panel/config/patch-registry.json',
            'panel/gateway_policy.py','ops/gateway-manager.py','ops/process-control.py','ops/uplink-gw.py','ops/uplink-gw-launch.sh',
            'ops/gateway-policy.py','docs/acceptance.md','docs/development-012.md']
FORBIDDEN = {'admin-token.txt','viewer-token.txt','access.db','usage.db','panel.env','whitelist.json',
             'uplink-gw.json','cp-ips.txt','settings.json','provider-settings.previous.json',
             'runtime-policy.json','daemon-start-mode.json','current.json','operation.json'}
PATTERNS = [re.compile(x, re.I) for x in [r'sk-[a-zA-Z0-9]{20,}',r'(?:gh[pousr]|github_pat)_[A-Za-z0-9_]{20,}',r'root@',r'/root/',r'/Users/',r'/Volumes/',r'-----BEGIN .*PRIVATE KEY-----']]


def issues(root):
    result=[]
    for f in REQUIRED:
        if not (root/f).is_file():
            result.append((f,'required file missing'))
    for f in root.rglob('*'):
        rel=f.relative_to(root).as_posix()
        if '.git' in f.relative_to(root).parts:
            continue
        if f.is_symlink():
            result.append((rel,'symlink excluded from release'));continue
        if f.is_dir():
            continue
        if not f.is_file():
            result.append((rel,'nonregular entry excluded from release'));continue
        if f.name in FORBIDDEN or f.match('settings.panel-bak-*.json') or f.suffix in ('.db','.sqlite','.sqlite3','.log','.jsonl','.bak','.pyc') or any(part in ('__pycache__','gateway-runtime','process-state','backups','patches') for part in f.relative_to(root).parts) or any(f.name.endswith(x) for x in ('.tar.gz','.tgz','.tar','.bak-p1')):
            result.append((rel,'local/generated artifact must not be published'));continue
        if rel == 'scripts/check_release.py':
            continue
        try:
            data=f.read_text(encoding='utf-8')
        except UnicodeDecodeError:
            result.append((rel,'unreviewed binary'));continue
        for i,line in enumerate(data.splitlines(),1):
            if any(p.search(line) for p in PATTERNS):
                result.append((f'{rel}:{i}','sensitive pattern; content intentionally omitted'))
    return result


if __name__=='__main__':
    found=issues(ROOT)
    for path,reason in found:
        print(f'FAIL {path}: {reason}')
    print('PASS: snapshot hygiene checks passed (not a security certification)' if not found else f'FAIL: {len(found)} issue(s)')
    sys.exit(bool(found))
