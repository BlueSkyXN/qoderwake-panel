"""Hash-pinned, opt-in binary patch transactions; profiles are local operator input."""
import hashlib
import json
import os
import re
from pathlib import Path
import secrets
import stat
import tempfile
import threading
import time
from contextlib import contextmanager

ORIGINAL = b'"https://openapi.qoder.com.cn"'
REPLACEMENT = b'"http://qwgw.local.test:19840"'
MODULES = {'qcs-endpoint': {'name': 'QCS 本机网关端点', 'before': ORIGINAL, 'after': REPLACEMENT}}


def sha(data):
    return hashlib.sha256(data).hexdigest()


def open_directory(path):
    path = Path(os.path.abspath(os.fspath(path)))
    flags = os.O_RDONLY | getattr(os, 'O_DIRECTORY', 0) | getattr(os, 'O_NOFOLLOW', 0)
    fd = os.open(path.anchor or '/', flags)
    try:
        for part in path.parts[1:]:
            child = os.open(part, flags, dir_fd=fd)
            os.close(fd)
            fd = child
        return fd
    except Exception:
        os.close(fd)
        raise


def read_regular_at(directory, name, limit=512 * 1024 * 1024):
    fd = os.open(name, os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0),
                 dir_fd=directory)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
            raise ValueError('patch_backup_mismatch')
        with os.fdopen(fd, 'rb') as stream:
            fd = -1
            data = stream.read(limit + 1)
    finally:
        if fd >= 0:
            os.close(fd)
    if len(data) > limit:
        raise ValueError('patch_backup_mismatch')
    return data


class PatchManager:
    def __init__(self, root, binary, registry, gateway_port):
        self.base = Path(root).resolve(strict=False)
        self.root = self.base / 'patches'
        self.binary = Path(os.path.abspath(os.fspath(binary)))
        self.registry = Path(registry)
        if type(gateway_port) is not int or not 1 <= gateway_port <= 65535:
            raise ValueError('invalid_gateway_port')
        self.gateway_port = gateway_port
        self.lock = threading.Lock()
        self.plans = {}

    def profiles(self):
        if not self.registry.exists():
            return []
        data = json.loads(self.registry.read_text())
        rows = data.get('profiles', [])
        if not isinstance(rows, list):
            raise ValueError('invalid_patch_registry')
        for row in rows:
            if not isinstance(row, dict) or any(not isinstance(row.get(k), str) or not re.fullmatch(r'[a-f0-9]{64}', row[k]) for k in ('original_sha256', 'patched_sha256')):
                raise ValueError('invalid_patch_registry')
            if row['original_sha256'] == row['patched_sha256']:
                raise ValueError('invalid_patch_registry')
        return rows

    def inspect(self):
        if self.binary.is_symlink() or not self.binary.is_file():
            raise ValueError('patch_binary_unavailable')
        data = self.binary.read_bytes()
        hashed = sha(data)
        for profile in self.profiles():
            if (profile.get('module') in MODULES and profile.get('version') and
                    hashed in (profile.get('original_sha256'), profile.get('patched_sha256'))):
                return data, profile, 'original' if hashed == profile['original_sha256'] else 'patched'
        return data, None, 'unsupported'

    def status(self):
        try:
            data, profile, state = self.inspect()
            return {'state': state, 'sha256': sha(data), 'verified': bool(profile),
                    'version': profile.get('version') if profile else None,
                    'module': profile.get('module') if profile else None,
                    'modules': [{'id': k, 'name': v['name']} for k,v in MODULES.items()],
                    'note': '完整文件哈希须匹配本机审核注册表；不执行未知版本，不证明封网或离线登录。'}
        except (OSError, ValueError):
            return {'state': 'unavailable', 'verified': False, 'note': '目标或注册表不可用；不会修改文件。'}

    def plan(self, action, principal):
        if action not in ('apply', 'restore'):
            raise ValueError('invalid_patch_action')
        if action == 'apply' and self.gateway_port != 19840:
            raise ValueError('patch_requires_gateway_port_19840')
        data, profile, state = self.inspect()
        if not profile:
            raise ValueError('unsupported_binary_hash')
        if state != ('original' if action == 'apply' else 'patched'):
            raise ValueError('patch_state_mismatch')
        token = secrets.token_urlsafe(32)
        plan = {'id': token, 'action': action, 'principal': principal, 'expires': time.time()+300,
                'sha256': sha(data), 'profile': profile}
        with self.lock:
            self.plans = {k:v for k,v in self.plans.items() if v['expires'] > time.time()}
            self.plans[token] = plan
        return {'id': token, 'action': action, 'sha256': plan['sha256'], 'version': profile['version'],
                'module': profile['module'], 'expires': plan['expires'],
                'warning': '需要先停止目标 daemon；会修改二进制。QCS 模块另需已配置本机网关和域名解析，不自动改 hosts。'}

    @contextmanager
    def transaction(self):
        import fcntl
        if self.base.is_symlink() or not self.base.is_dir():
            raise ValueError('patch_root_unavailable')
        for directory, mode in (
                (self.base / 'process-state', 0o700),
                (self.root, 0o700)):
            try:
                directory.mkdir(mode=mode)
            except FileExistsError:
                pass
            if directory.is_symlink() or not directory.is_dir():
                raise ValueError('symlink_rejected')
        state_dir = open_directory(self.base / 'process-state')
        patch_dir = -1
        state_fd = fd = -1
        try:
            state_fd = os.open(
                'daemon-cn.lock',
                os.O_CREAT | os.O_RDWR | getattr(os, 'O_NOFOLLOW', 0),
                0o600, dir_fd=state_dir)
            fcntl.flock(state_fd, fcntl.LOCK_EX)
            patch_dir = open_directory(self.root)
            fd = os.open(
                'transaction.lock',
                os.O_CREAT | os.O_RDWR | getattr(os, 'O_NOFOLLOW', 0),
                0o600, dir_fd=patch_dir)
            fcntl.flock(fd, fcntl.LOCK_EX)
            yield patch_dir
        finally:
            if fd >= 0:
                os.close(fd)
            if state_fd >= 0:
                os.close(state_fd)
            if patch_dir >= 0:
                os.close(patch_dir)
            os.close(state_dir)

    def require_stopped(self):
        proc = Path('/proc')
        if not proc.is_dir():
            raise ValueError('patch_platform_unsupported')
        target = self.binary.stat()
        for item in proc.iterdir():
            if not item.name.isdigit():
                continue
            try:
                info = (item/'exe').stat()
            except FileNotFoundError:
                continue
            except PermissionError:
                raise ValueError('patch_process_visibility_required')
            if (info.st_dev, info.st_ino) == (target.st_dev, target.st_ino):
                raise ValueError('stop_daemon_before_patch')

    def replace(self, data, expected):
        self.require_stopped()
        if self.binary.is_symlink() or sha(self.binary.read_bytes()) != expected:
            raise ValueError('binary_changed_since_plan')
        info = self.binary.stat()
        fd, tmp = tempfile.mkstemp(prefix='.qw-patch-', dir=self.binary.parent)
        try:
            with os.fdopen(fd, 'wb') as stream:
                stream.write(data)
                os.fchmod(stream.fileno(), stat.S_IMODE(info.st_mode))
                if os.geteuid() == 0:
                    os.fchown(stream.fileno(), info.st_uid, info.st_gid)
                stream.flush()
                os.fsync(stream.fileno())
            if self.binary.is_symlink() or sha(self.binary.read_bytes()) != expected:
                raise ValueError('binary_changed_since_plan')
            self.require_stopped()
            os.replace(tmp, self.binary)
            dfd = os.open(self.binary.parent, os.O_RDONLY)
            try:
                os.fsync(dfd)
            finally:
                os.close(dfd)
        finally:
            if os.path.exists(tmp):
                os.unlink(tmp)

    def execute(self, token, principal):
        if not isinstance(token, str):
            raise ValueError('invalid_patch_plan')
        with self.lock:
            plan = self.plans.get(token)
            if not plan or plan['principal'] != principal or plan['expires'] < time.time():
                raise ValueError('invalid_patch_plan')
            del self.plans[token]
        with self.transaction() as patch_dir:
            self.require_stopped()
            data, profile, state = self.inspect()
            if sha(data) != plan['sha256'] or profile != plan['profile']:
                raise ValueError('binary_changed_since_plan')
            module = MODULES[profile['module']]
            backup_name = profile['original_sha256'] + '.bin'
            if plan['action'] == 'apply':
                if data.count(module['before']) != 1 or data.count(module['after']) != 0:
                    raise ValueError('patch_anchor_mismatch')
                output = data.replace(module['before'], module['after'])
                if len(output) != len(data) or sha(output) != profile['patched_sha256']:
                    raise ValueError('patch_output_hash_mismatch')
                try:
                    backup_data = read_regular_at(patch_dir, backup_name)
                except FileNotFoundError:
                    fd = os.open(
                        backup_name, os.O_WRONLY | os.O_CREAT | os.O_EXCL |
                        getattr(os, 'O_NOFOLLOW', 0), 0o600,
                        dir_fd=patch_dir)
                    with os.fdopen(fd, 'wb') as stream:
                        stream.write(data)
                        stream.flush()
                        os.fsync(stream.fileno())
                    os.fsync(patch_dir)
                else:
                    if sha(backup_data) != profile['original_sha256']:
                        raise ValueError('patch_backup_mismatch')
            else:
                try:
                    output = read_regular_at(patch_dir, backup_name)
                except FileNotFoundError:
                    candidates = sorted(self.binary.parent.glob(
                        self.binary.name + '.bak-p1-*'))
                    legacy = next((p for p in candidates if not p.is_symlink() and p.is_file()
                                  and sha(p.read_bytes()) == profile['original_sha256']), None)
                    if legacy is None:
                        raise ValueError('patch_backup_missing')
                    output = legacy.read_bytes()
                if sha(output) != profile['original_sha256']:
                    raise ValueError('patch_backup_mismatch')
            self.replace(output, plan['sha256'])
            return {'ok': True, 'sha256': sha(output), 'state': 'patched' if plan['action'] == 'apply' else 'original'}
