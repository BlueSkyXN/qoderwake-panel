#!/usr/bin/env python3
"""单实例前台启动入口，支持 macOS 和 Linux；不接管官方 daemon。"""
import argparse
import fcntl
import hashlib
import os
from pathlib import Path
import platform
import plistlib
import runpy
import sys
from panel_security import Security


def daemon_url(home, explicit=None):
    if explicit:
        return explicit
    port_file = home/'run/daemon.port'
    if port_file.is_file() and port_file.stat().st_size <= 32:
        raw = port_file.read_text().strip()
        if raw.isdigit() and 1 <= int(raw) <= 65535:
            return 'http://127.0.0.1:' + raw
    return 'http://127.0.0.1:19830'


def acquire_instance(root):
    root.mkdir(parents=True, exist_ok=True)
    fd = os.open(root/'panel-service.lock', os.O_RDWR | os.O_CREAT | getattr(os, 'O_NOFOLLOW', 0), 0o600)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        os.close(fd)
        raise RuntimeError('该数据目录已有 Panel 运行，请使用现有实例或选择其他数据目录。') from None
    return os.fdopen(fd, 'w')


def launchagent(args, env):
    label = 'local.qoderwake.panel.' + hashlib.sha256(str(args.root).encode()).hexdigest()[:12]
    logs = args.root/'logs'
    logs.mkdir(parents=True, exist_ok=True)
    return {'Label':label, 'ProgramArguments':[sys.executable, str(Path(__file__).resolve()),
             '--root',str(args.root),'--home',str(args.home),'--bind',args.bind,'--port',str(args.port),
             '--daemon-url',env['QW_DAEMON_URL'],'--daemon-bin',env['QW_DAEMON_BIN']]+(['--legacy'] if args.legacy else []),
            'EnvironmentVariables':{k:v for k,v in env.items() if k in (
                'QW_PUBLIC_ORIGIN','QW_TLS_CERT','QW_TLS_KEY','QW_REQUIRE_TLS','QW_ALLOWED_CLIENTS',
                'QW_DAEMON_FRONTEND_TOKEN_FILE','QW_DAEMON_CREDENTIAL_SERVICE','QW_EXPERIMENTAL_PLUGIN_WRITES','QW_WHITELIST',
                'PYTHONHOME','PYTHONNOUSERSITE','DYLD_FRAMEWORK_PATH','DYLD_LIBRARY_PATH')},
            'RunAtLoad':True, 'KeepAlive':{'SuccessfulExit':False}, 'ThrottleInterval':10,
            'StandardOutPath':str(logs/'panel.log'), 'StandardErrorPath':str(logs/'panel-error.log')}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=Path(os.environ.get('QW_ROOT') or Path.home()/'qoderwake-panel'))
    parser.add_argument('--home', type=Path, default=os.environ.get('QW_HOME'))
    parser.add_argument('--bind', default=os.environ.get('QW_BIND','127.0.0.1'))
    parser.add_argument('--port', type=int, default=int(os.environ.get('QW_PORT','19831')))
    parser.add_argument('--daemon-url', default=os.environ.get('QW_DAEMON_URL'))
    parser.add_argument('--daemon-bin', default=os.environ.get('QW_DAEMON_BIN'))
    parser.add_argument('--legacy', action='store_true', help='显式允许旧共享令牌登录模式')
    parser.add_argument('--write-launchagent', type=Path, help='生成用户 LaunchAgent 文件，不加载服务')
    args = parser.parse_args()
    if sys.version_info < (3,10):
        parser.error('需要 Python 3.10 或更新版本')
    args.root = args.root.expanduser().resolve()
    args.home = (args.home or (Path.home()/'.qoderwake' if platform.system()=='Darwin' else args.root/'test-home')).expanduser().resolve()
    if not 1 <= args.port <= 65535:
        parser.error('端口必须在 1–65535 之间')
    env = dict(os.environ, QW_ROOT=str(args.root), QW_HOME=str(args.home), QW_BIND=args.bind,
               QW_PORT=str(args.port), QW_DAEMON_URL=daemon_url(args.home,args.daemon_url),
               QW_DAEMON_BIN=args.daemon_bin or (str(args.home/'qoderwake') if platform.system()=='Darwin' else 'qoderwake-cn'))
    if not Security(args.root).accounts_enabled() and not args.legacy:
        parser.error('请先运行 panel-admin.py --root <同一数据目录> init-admin --username <用户名> --name <显示名>')
    if args.write_launchagent:
        if platform.system() != 'Darwin':
            parser.error('LaunchAgent 仅适用于 macOS')
        with os.fdopen(os.open(args.write_launchagent.expanduser(),os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600),'wb') as stream:
            plistlib.dump(launchagent(args,env),stream)
        print('LaunchAgent 文件已生成；尚未加载。')
        return
    with acquire_instance(args.root) as lock:
        lock.write(str(os.getpid())+'\n');lock.flush()
        os.environ.update(env)
        try:
            runpy.run_path(str(Path(__file__).with_name('qoderwake-panel.py')), run_name='__main__')
        except KeyboardInterrupt:
            pass


if __name__ == '__main__':
    try:
        main()
    except RuntimeError as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(1)
