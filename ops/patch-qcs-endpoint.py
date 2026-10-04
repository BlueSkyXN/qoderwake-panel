#!/usr/bin/env python3
"""QCS endpoint patch CLI with the same hash and stopped-process gates as Panel."""
import argparse
import json
import os
from pathlib import Path
import sys

PANEL = Path(__file__).resolve().parents[1]/'panel'
sys.path.insert(0, str(PANEL))
from panel_patches import PatchManager


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('status', 'plan', 'apply', 'restore'))
    parser.add_argument('binary', type=Path)
    parser.add_argument('--root', type=Path, default=Path(os.environ.get('QW_ROOT') or Path.home()/'qoderwake-panel'))
    parser.add_argument('--registry', type=Path, default=PANEL/'config/patch-registry.json')
    parser.add_argument('--gateway-port', type=int, default=(
        int(os.environ['QW_GW_PORT']) if os.environ.get('QW_GW_PORT')
        else None))
    parser.add_argument('--confirm-sha256')
    args = parser.parse_args()
    if args.action in ('apply', 'restore') and args.gateway_port is None:
        parser.error('apply/restore requires --gateway-port or QW_GW_PORT')
    manager = PatchManager(
        args.root, args.binary, args.registry,
        args.gateway_port if args.gateway_port is not None else 19840)
    if args.action in ('status', 'plan'):
        print(json.dumps(manager.status(), ensure_ascii=False, indent=2))
        return
    plan = manager.plan(args.action, 'local-cli')
    if args.confirm_sha256 != plan['sha256']:
        parser.error('Read status first, then pass --confirm-sha256 with the full current hash; stop daemon before applying.')
    print(json.dumps(manager.execute(plan['id'], 'local-cli'), ensure_ascii=False))


if __name__ == '__main__':
    main()
