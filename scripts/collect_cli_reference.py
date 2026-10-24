#!/usr/bin/env python3
"""只执行官方 CLI 的 --help / --version，保存逐层命令参考。"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess


def children(help_text):
    block = help_text.split('\nCommands:\n', 1)
    if len(block) == 1:
        return []
    return [m.group(1).split('|')[0] for line in block[1].splitlines()
            if (m := re.match(r'^  ([a-zA-Z][a-zA-Z0-9|_-]*)(?:\s|$)', line))
            and m.group(1) != 'help']


def collect(binary, output, resume=False):
    env = dict(os.environ, NO_COLOR='1', TERM='dumb', COLUMNS='180')
    def read(path):
        result = subprocess.run([binary, *path, '--help'], env=env, capture_output=True,
                                text=True, timeout=45)
        if result.returncode or not result.stdout.startswith('Usage:'):
            raise RuntimeError('help failed: ' + ' '.join(path))
        return {'path': path, 'help': result.stdout}
    nodes = json.loads(output.read_text())['nodes'] if resume and output.is_file() else []
    seen = {tuple(n['path']) for n in nodes}
    pending = ([n['path'] + [child] for n in nodes for child in children(n['help'])
                if tuple(n['path'] + [child]) not in seen] if nodes else [[]])
    with ThreadPoolExecutor(max_workers=4) as pool:
        while pending:
            batch = list(pool.map(read, pending))
            nodes.extend(batch)
            pending = [node['path'] + [child] for node in batch for child in children(node['help'])]
            print('help nodes: %d; next: %d' % (len(nodes), len(pending)), flush=True)
            output.write_text(json.dumps({'nodes': nodes}, ensure_ascii=False, indent=2))
            if len(nodes) > 1500:
                raise RuntimeError('unexpected command tree size')
    version = subprocess.run([binary, '--version'], env=env, capture_output=True,
                             text=True, timeout=45, check=True).stdout.strip()
    digest = hashlib.sha256(json.dumps(nodes, sort_keys=True).encode()).hexdigest()
    output.write_text(json.dumps({'version': version, 'sha256': digest, 'nodes': nodes}, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary')
    parser.add_argument('output', type=Path)
    parser.add_argument('--resume', action='store_true', help='复用同一二进制已经采集的节点')
    args = parser.parse_args()
    collect(args.binary, args.output, args.resume)
