#!/usr/bin/env python3
"""本机初始化与账号恢复。密码只通过终端交互输入。"""
import argparse
import getpass
import os
from pathlib import Path
from panel_security import Security, AccessError


def main():
    parser = argparse.ArgumentParser(description='Panel 本地账号管理')
    parser.add_argument('--root', type=Path, default=Path(os.environ.get('QW_ROOT') or Path.home()/'qoderwake-panel'))
    sub = parser.add_subparsers(dest='command', required=True)
    init = sub.add_parser('init-admin', help='创建首管理员并切换到人员登录，撤销旧共享会话')
    init.add_argument('--username', required=True)
    init.add_argument('--name', required=True)
    reset = sub.add_parser('reset-password', help='重置已有人员密码并撤销其会话')
    reset.add_argument('--username', required=True)
    sub.add_parser('users', help='列出本地用户名、角色及启用状态')
    args = parser.parse_args()
    sec = Security(args.root)
    if args.command == 'users':
        for user in sec.users():
            print(user['username'], user['role'], 'enabled' if user['enabled'] else 'disabled')
        return
    password = getpass.getpass('新密码（至少 10 个字符）：')
    if password != getpass.getpass('再次输入：'):
        parser.error('两次密码不一致')
    try:
        if args.command == 'init-admin':
            sec.bootstrap_admin(args.username, args.name, password)
            print('管理员已创建。浏览器使用用户名和密码登录；旧共享令牌及会话已停用。')
        else:
            user = next((u for u in sec.users() if u['username'] == args.username.lower()), None)
            if not user:
                parser.error('账号不存在')
            sec.reset_password(user['id'], password, user['revision'])
            sec.audit('local', {'principal':'local-recovery','role':'admin'}, 'local/password-reset', user['id'], 200, True)
            print('密码已重置，该账号原会话已撤销。')
    except (ValueError, AccessError) as error:
        parser.error(str(error))


if __name__ == '__main__':
    main()
