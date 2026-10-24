#!/usr/bin/env python3
"""Read-only snapshot hygiene check; never copies, commits or publishes."""
import argparse
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
REQUIRED = ['README.md','LICENSE','.gitignore','panel/qoderwake-panel.py','panel/panel_security.py',
            'panel/panel_accounts.py','panel/panel_permissions.py','panel/panel-admin.py','panel/run-panel.py','panel/start-panel.command','panel/static/collaboration.js','panel/native-credential.mjs','docs/collaboration.md',
            'panel/cli_catalog.py','panel/cli_jobs.py','panel/cli_files.py','panel/config/cli-reference.json','panel/config/cli-policy.json','panel/static/native.js','docs/cli-operations.md','docs/cli-coverage.md',
            'panel/static/index.html','panel/static/panel.js','panel/static/management.js','panel/static/advanced.js','panel/static/panel.css',
            'panel/panel_patches.py','panel/provider_config.py','panel/provider_transport.py','panel/daemon_transport.py','panel/deletion_preflight.py','panel/gateway_runtime.py','panel/config/patch-registry.json',
            'panel/gateway_policy.py','panel/log_io.py','panel/usage_store.py','ops/gateway-manager.py','ops/process-control.py','ops/uplink-gw.py','ops/uplink-gw-launch.sh',
            'ops/gateway-policy.py','docs/acceptance.md','docs/development-012.md',
            'go.mod','go.sum','cmd/panel/main.go','internal/server/server.go','internal/webui/assets.go',
            'web/package.json','web/package-lock.json','web/src/main.tsx','web/src/App.tsx',
            'docs/model-management.md','docs/react-go-migration.md','scripts/build-go.sh']
FORBIDDEN = {'admin-token.txt','viewer-token.txt','access.db','usage.db','panel.env','whitelist.json',
             'uplink-gw.json','cp-ips.txt','settings.json','provider-settings.previous.json',
             'runtime-policy.json','daemon-start-mode.json','current.json','operation.json','digest-key'}
PATTERNS = [re.compile(x, re.I) for x in [r'sk-[a-zA-Z0-9]{20,}',r'(?:gh[pousr]|github_pat)_[A-Za-z0-9_]{20,}',r'root@',r'(?<![A-Za-z0-9_./-])/(?:root|Users|Volumes)/',r'-----BEGIN .*PRIVATE KEY-----']]


def issues(root, files=None):
    result=[]
    for f in REQUIRED:
        if not (root/f).is_file():
            result.append((f,'required file missing'))
    for f in (root.rglob('*') if files is None else (root/name for name in files)):
        rel=f.relative_to(root).as_posix()
        if '.git' in f.relative_to(root).parts:
            continue
        if f.is_symlink():
            result.append((rel,'symlink excluded from release'));continue
        if f.is_dir():
            continue
        if not f.is_file():
            result.append((rel,'nonregular entry excluded from release'));continue
        parts=f.relative_to(root).parts
        patch_source=len(parts)==3 and parts[:2]==('internal','patches') and f.suffix=='.go'
        forbidden_dir=any(part in ('__pycache__','gateway-runtime','process-state','backups','native-files','native-operations','node_modules','local','build','dist') for part in parts)
        if f.name in FORBIDDEN or f.name=='.env' or f.match('settings.panel-bak-*.json') or f.suffix in ('.db','.sqlite','.sqlite3','.log','.jsonl','.bak','.pyc') or forbidden_dir or ('patches' in parts and not patch_source) or any(f.name.endswith(x) for x in ('.tar.gz','.tgz','.tar','.bak-p1')):
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
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root',type=Path,default=ROOT)
    parser.add_argument('--tracked',action='store_true',help='check Git source files, excluding ignored build outputs')
    args=parser.parse_args()
    files=None
    if args.tracked:
        files=sorted(set(filter(None,subprocess.check_output(['git','ls-files','-z','--cached','--others','--exclude-standard'],cwd=args.root).decode().split('\0'))))
        if not files:
            parser.error('no source files found')
    found=issues(args.root,files)
    for path,reason in found:
        print(f'FAIL {path}: {reason}')
    print('PASS: snapshot hygiene checks passed (not a security certification)' if not found else f'FAIL: {len(found)} issue(s)')
    sys.exit(bool(found))
