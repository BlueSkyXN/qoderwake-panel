#!/usr/bin/env python3
"""Start, stop, and inspect one locally managed process using exact Linux identity."""
import argparse
from contextlib import contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import select
import signal
import socket
import stat
import subprocess
import sys
import threading
import time
import urllib.request
from urllib.parse import urlsplit

SCHEMA_VERSION = 1
STATE_KEYS = {
    'schemaVersion', 'name', 'profile', 'status', 'pid', 'startTicks',
    'bootId', 'uid', 'executable', 'launch', 'script', 'cmdline',
    'home', 'port', 'mode', 'endpoint', 'healthVersion', 'environment'
}
FILE_KEYS = {'path', 'device', 'inode', 'sha256'}
EXECUTABLE_KEYS = {'path', 'device', 'inode'}
SAFE_NAME = re.compile(r'[a-z][a-z0-9-]{0,31}')
BOOT_ID = re.compile(r'[0-9a-f-]{16,64}')
HASH = re.compile(r'[0-9a-f]{64}')
DAEMON_ENV = {
    'QODER_SDK_CUSTOM_BASE_URL_BYOK', 'QODERWAKE_HOT_DEPLOY',
    'QODER_MEMORY_DISABLE_EMBEDDING', 'QODERWAKE_HOME', 'QODER_ENV',
    'QODERWAKE_ENDPOINT_BASE_URL', 'QODERWAKE_PRODUCT_BASE_URL',
    'QODERWAKE_CENTER_BASE_URL', 'QODERWAKE_OPENAPI_BASE_URL',
    'QODERWAKE_CODEBASE_BASE_URL', 'QODERWAKE_API_INVOKE_BASE_URL',
    'QODER_SERVER_ENDPOINT', 'QODER_CENTER_ENDPOINT',
    'QODER_CONFIG_SERVICE_URL'
}
PANEL_ENV = {
    'QW_ROOT', 'QW_HOME', 'QW_DAEMON_BIN', 'QW_DAEMON_URL',
    'QW_DAEMON_FRONTEND_TOKEN_FILE', 'QW_WHITELIST', 'QW_PORT',
    'QW_BIND', 'QW_PUBLIC_ORIGIN', 'QW_ALLOWED_CLIENTS', 'QW_REQUIRE_TLS',
    'QW_TLS_CERT', 'QW_TLS_KEY', 'QW_GW_CONFIG', 'QW_GW_PORT',
    'QW_PATCH_REGISTRY', 'QW_EXPERIMENTAL_PLUGIN_WRITES',
    'QODERWAKE_HOT_DEPLOY', 'QODER_MEMORY_DISABLE_EMBEDDING'
}
GATEWAY_ENV = {
    'QODERWAKE_ENDPOINT_BASE_URL', 'QODERWAKE_PRODUCT_BASE_URL',
    'QODERWAKE_CENTER_BASE_URL', 'QODERWAKE_OPENAPI_BASE_URL',
    'QODERWAKE_CODEBASE_BASE_URL', 'QODERWAKE_API_INVOKE_BASE_URL',
    'QODER_SERVER_ENDPOINT', 'QODER_CENTER_ENDPOINT',
    'QODER_CONFIG_SERVICE_URL'
}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def _plain_int(value, minimum=0, maximum=None):
    return (type(value) is int and value >= minimum and
            (maximum is None or value <= maximum))


def _sha256(path, limit=512 * 1024 * 1024):
    path = Path(path)
    info = path.stat()
    if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
        raise ValueError('managed_file_unavailable')
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        while True:
            chunk = stream.read(1024 * 1024)
            if not chunk:
                break
            digest.update(chunk)
    return digest.hexdigest()


def file_identity(path, with_hash=True):
    path = Path(path).resolve(strict=True)
    if path.is_symlink() or not path.is_file():
        raise ValueError('managed_file_unavailable')
    info = path.stat()
    return {
        'path': str(path), 'device': info.st_dev, 'inode': info.st_ino,
        'sha256': _sha256(path) if with_hash else '0' * 64
    }


def executable_identity(path):
    path = Path(path).resolve(strict=True)
    info = path.stat()
    if not stat.S_ISREG(info.st_mode):
        raise ValueError('managed_executable_unavailable')
    return {'path': str(path), 'device': info.st_dev, 'inode': info.st_ino}


def file_identity_matches(value):
    try:
        path = Path(value['path'])
        if path.is_symlink() or path.resolve(strict=True) != path:
            return False
        info = path.stat()
        if (info.st_dev, info.st_ino) != (
                value['device'], value['inode']):
            return False
        return (value['sha256'] == '0' * 64 or
                _sha256(path) == value['sha256'])
    except (OSError, ValueError, KeyError):
        return False


def _validate_file(value, code):
    if not isinstance(value, dict) or set(value) != FILE_KEYS:
        raise ValueError(code)
    if (not isinstance(value.get('path'), str) or not Path(value['path']).is_absolute() or
            not _plain_int(value.get('device')) or not _plain_int(value.get('inode'), 1) or
            not isinstance(value.get('sha256'), str) or not HASH.fullmatch(value['sha256'])):
        raise ValueError(code)
    return dict(value)


def _validate_executable(value):
    if not isinstance(value, dict) or set(value) != EXECUTABLE_KEYS:
        raise ValueError('invalid_process_executable')
    if (not isinstance(value.get('path'), str) or not Path(value['path']).is_absolute() or
            not _plain_int(value.get('device')) or not _plain_int(value.get('inode'), 1)):
        raise ValueError('invalid_process_executable')
    return dict(value)


def validate_state(value):
    if (not isinstance(value, dict) or set(value) != STATE_KEYS or
            value.get('schemaVersion') != SCHEMA_VERSION):
        raise ValueError('invalid_process_state')
    if not isinstance(value.get('name'), str) or not SAFE_NAME.fullmatch(value['name']):
        raise ValueError('invalid_process_state')
    if value.get('profile') not in ('panel', 'daemon'):
        raise ValueError('invalid_process_profile')
    if value.get('status') not in ('starting', 'running'):
        raise ValueError('invalid_process_status')
    for key in ('pid', 'startTicks'):
        if not _plain_int(value.get(key), 1):
            raise ValueError('invalid_process_identity')
    if not _plain_int(value.get('uid')):
        raise ValueError('invalid_process_identity')
    if not isinstance(value.get('bootId'), str) or not BOOT_ID.fullmatch(value['bootId']):
        raise ValueError('invalid_process_identity')
    executable = _validate_executable(value.get('executable'))
    launch = _validate_file(value.get('launch'), 'invalid_process_launch')
    if (launch['path'], launch['device'], launch['inode']) != (
            executable['path'], executable['device'], executable['inode']):
        raise ValueError('invalid_process_launch')
    script = value.get('script')
    if script is not None:
        script = _validate_file(script, 'invalid_process_script')
    cmdline = value.get('cmdline')
    if (not isinstance(cmdline, list) or not cmdline or len(cmdline) > 64 or
            any(not isinstance(item, str) or not item or len(item) > 4096
                for item in cmdline)):
        raise ValueError('invalid_process_command')
    expected_cmdline = ([executable['path'], script['path']]
                        if value.get('profile') == 'panel' and script
                        else [executable['path'], 'start', '--foreground'])
    if cmdline != expected_cmdline:
        raise ValueError('invalid_process_command')
    home = value.get('home')
    if not isinstance(home, str) or not Path(home).is_absolute():
        raise ValueError('invalid_process_home')
    if not _plain_int(value.get('port'), 1, 65535):
        raise ValueError('invalid_process_port')
    if value.get('mode') not in ('panel', 'direct', 'gateway'):
        raise ValueError('invalid_process_mode')
    endpoint = value.get('endpoint')
    if endpoint is not None:
        if not isinstance(endpoint, str) or len(endpoint) > 2048:
            raise ValueError('invalid_process_endpoint')
        parsed = urlsplit(endpoint)
        if (parsed.scheme != 'http' or parsed.hostname not in
                ('127.0.0.1', 'localhost', '::1') or parsed.username or
                parsed.password or parsed.query or parsed.fragment):
            raise ValueError('invalid_process_endpoint')
    if (value['mode'] == 'gateway') != bool(endpoint):
        raise ValueError('invalid_process_endpoint')
    version = value.get('healthVersion')
    if version is not None and (not isinstance(version, str) or not 1 <= len(version) <= 128):
        raise ValueError('invalid_process_health')
    environment = value.get('environment')
    allowed = PANEL_ENV if value['profile'] == 'panel' else DAEMON_ENV
    if (not isinstance(environment, dict) or set(environment) - allowed or
            any(not isinstance(key, str) or not isinstance(item, str) or len(item) > 4096
                for key, item in environment.items())):
        raise ValueError('invalid_process_environment')
    if value['profile'] == 'panel':
        if (value['mode'] != 'panel' or script is None or
                environment.get('QW_ROOT') is None or
                environment.get('QW_HOME') != home or
                environment.get('QW_PORT') != str(value['port'])):
            raise ValueError('invalid_process_environment')
    else:
        if (value['mode'] not in ('direct', 'gateway') or script is not None or
                environment.get('QODERWAKE_HOME') != home):
            raise ValueError('invalid_process_environment')
        gateway = {key: item for key, item in environment.items() if key in GATEWAY_ENV}
        if value['mode'] == 'gateway':
            if set(gateway) != GATEWAY_ENV or set(gateway.values()) != {endpoint}:
                raise ValueError('invalid_process_environment')
        elif gateway:
            raise ValueError('invalid_process_environment')
    return dict(value, executable=executable, launch=launch, script=script,
                cmdline=list(cmdline), environment=dict(environment))


def atomic(path, data, mode=0o600):
    path = Path(path)
    if path.is_symlink():
        raise ValueError('symlink_rejected')
    tmp = path.with_name('.' + path.name + '-' + secrets.token_hex(6))
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL |
                 getattr(os, 'O_NOFOLLOW', 0), mode)
    try:
        with os.fdopen(fd, 'wb') as stream:
            fd = -1
            stream.write(data)
            os.fchmod(stream.fileno(), mode)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(tmp, path)
        directory = os.open(path.parent, os.O_RDONLY |
                            getattr(os, 'O_DIRECTORY', 0))
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if fd >= 0:
            os.close(fd)
        if tmp.exists():
            tmp.unlink()


def read_state(path):
    path = Path(path)
    try:
        fd = os.open(path, os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0))
    except FileNotFoundError:
        return None
    except OSError:
        raise ValueError('process_state_unavailable')
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_size > 1024 * 1024:
            raise ValueError('invalid_process_state_file')
        with os.fdopen(fd, 'rb') as stream:
            fd = -1
            raw = stream.read(1024 * 1024 + 1)
    finally:
        if fd >= 0:
            os.close(fd)
    try:
        return validate_state(json.loads(raw))
    except (ValueError, UnicodeDecodeError):
        raise ValueError('invalid_process_state_file')


def _proc_stat_start_ticks(raw):
    close = raw.rfind(')')
    if close < 0:
        raise ValueError('invalid_proc_stat')
    fields = raw[close + 1:].strip().split()
    if len(fields) <= 19 or int(fields[19]) <= 0:
        raise ValueError('invalid_proc_stat')
    return int(fields[19])


def _proc_uid(raw):
    for line in raw.splitlines():
        if line.startswith('Uid:'):
            values = line.split()
            if len(values) >= 2:
                return int(values[1])
    raise ValueError('invalid_proc_status')


def _proc_environment(raw):
    result = {}
    for item in raw.split(b'\0'):
        if b'=' not in item:
            continue
        key, value = item.split(b'=', 1)
        try:
            result[key.decode('ascii')] = value.decode('utf-8')
        except UnicodeDecodeError:
            continue
    return result


def observe_process(pid, proc_root='/proc'):
    proc_root = Path(proc_root)
    process = proc_root / str(pid)
    cmdline = [os.fsdecode(item) for item in
               (process / 'cmdline').read_bytes().split(b'\0') if item]
    executable_path = (process / 'exe').resolve(strict=True)
    executable_info = (process / 'exe').stat()
    boot_id = (proc_root / 'sys/kernel/random/boot_id').read_text().strip().lower()
    if not BOOT_ID.fullmatch(boot_id):
        raise ValueError('invalid_proc_boot_id')
    return {
        'pid': pid,
        'startTicks': _proc_stat_start_ticks((process / 'stat').read_text()),
        'bootId': boot_id,
        'uid': _proc_uid((process / 'status').read_text()),
        'executable': {
            'path': str(executable_path),
            'device': executable_info.st_dev,
            'inode': executable_info.st_ino
        },
        'cmdline': cmdline,
        'environment': _proc_environment((process / 'environ').read_bytes())
    }


def listening_socket_inodes(port, proc_root='/proc'):
    inodes = set()
    proc_root = Path(proc_root)
    for name in ('tcp', 'tcp6'):
        path = proc_root / 'net' / name
        try:
            lines = path.read_text().splitlines()[1:]
        except OSError:
            raise ValueError('process_port_visibility_required')
        for line in lines:
            fields = line.split()
            if len(fields) < 10:
                continue
            try:
                local_port = int(fields[1].rsplit(':', 1)[1], 16)
            except (ValueError, IndexError):
                continue
            if local_port == port and fields[3] == '0A':
                inodes.add(fields[9])
    return inodes


def process_socket_inodes(pid, proc_root='/proc'):
    result = set()
    directory = Path(proc_root) / str(pid) / 'fd'
    try:
        entries = list(directory.iterdir())
    except OSError:
        raise ValueError('process_port_visibility_required')
    for entry in entries:
        try:
            target = os.readlink(entry)
        except (FileNotFoundError, ProcessLookupError):
            continue
        except OSError:
            raise ValueError('process_port_visibility_required')
        match = re.fullmatch(r'socket:\[(\d+)\]', target)
        if match:
            result.add(match.group(1))
    return result


def port_status(state, proc_root='/proc'):
    try:
        listeners = listening_socket_inodes(state['port'], proc_root)
        owned = process_socket_inodes(state['pid'], proc_root)
    except ValueError:
        return 'unknown'
    if not listeners:
        return 'unbound'
    return 'owned' if listeners <= owned else 'other'


def identity_status(state, proc_root='/proc'):
    try:
        state = validate_state(state)
        if not file_identity_matches(state['launch']):
            return 'mismatch'
        if state['script'] is not None and not file_identity_matches(state['script']):
            return 'mismatch'
    except (OSError, ValueError, KeyError, TypeError):
        return 'unknown'
    process = Path(proc_root) / str(state['pid'])
    try:
        observed = observe_process(state['pid'], proc_root)
    except (FileNotFoundError, ProcessLookupError):
        try:
            process.stat()
        except FileNotFoundError:
            try:
                boot_id = (Path(proc_root) / 'sys/kernel/random/boot_id').read_text().strip().lower()
            except OSError:
                return 'unknown'
            if not BOOT_ID.fullmatch(boot_id):
                return 'unknown'
            try:
                process.stat()
            except FileNotFoundError:
                return 'exited'
            except OSError:
                return 'unknown'
            return 'unknown'
        except OSError:
            return 'unknown'
        return 'unknown'
    except (OSError, ValueError, KeyError, TypeError):
        return 'unknown'
    expected_environment = state['environment']
    actual_environment = {
        key: observed['environment'].get(key)
        for key in expected_environment
    }
    expected = {
        'pid': state['pid'], 'startTicks': state['startTicks'],
        'bootId': state['bootId'], 'uid': state['uid'],
        'executable': state['executable'], 'cmdline': state['cmdline'],
        'environment': expected_environment
    }
    actual = dict(observed, environment=actual_environment)
    return 'match' if actual == expected else 'mismatch'


def health_version(url, timeout=2):
    opener = urllib.request.build_opener(
        urllib.request.ProxyHandler({}), NoRedirect())
    try:
        with opener.open(url, timeout=timeout) as response:
            raw = response.read(65537)
    except Exception:
        return None
    if len(raw) > 65536 or getattr(response, 'status', 200) != 200:
        return None
    try:
        value = json.loads(raw)
    except (ValueError, UnicodeDecodeError):
        return None
    data = value.get('data') if isinstance(value, dict) and isinstance(value.get('data'), dict) else value
    version = data.get('version') if isinstance(data, dict) else None
    return version if isinstance(version, str) and 1 <= len(version) <= 128 else None


class Controller:
    def __init__(self, root, name, profile, launch, home, port, mode,
                 log, health_url, endpoint=None, script=None,
                 proc_root='/proc', expected_version=None):
        if not SAFE_NAME.fullmatch(name):
            raise ValueError('invalid_process_name')
        if profile not in ('panel', 'daemon'):
            raise ValueError('invalid_process_profile')
        self.root = Path(root).resolve(strict=False)
        self.name = name
        self.profile = profile
        self.launch = Path(launch).resolve(strict=True)
        self.home = Path(home).resolve(strict=False)
        self.port = int(port)
        if not 1 <= self.port <= 65535:
            raise ValueError('invalid_process_port')
        self.mode = mode
        self.endpoint = endpoint
        self.script = Path(script).resolve(strict=True) if script else None
        self.log = Path(log)
        health = urlsplit(health_url)
        if (health.scheme != 'http' or health.hostname not in
                ('127.0.0.1', 'localhost', '::1') or health.username or
                health.password or health.fragment or
                (health.port or 80) != self.port):
            raise ValueError('invalid_process_health_url')
        self.health_url = health_url
        if endpoint is not None:
            parsed = urlsplit(endpoint)
            if (parsed.scheme != 'http' or parsed.hostname not in
                    ('127.0.0.1', 'localhost', '::1') or parsed.username or
                    parsed.password or parsed.query or parsed.fragment or
                    parsed.path not in ('', '/')):
                raise ValueError('invalid_gateway_endpoint')
        self.expected_version = expected_version
        self.proc_root = Path(proc_root)
        self.launch_identity = None
        self.script_identity = None
        self.directory = self.root / 'process-state'
        self.state_file = self.directory / (name + '.json')
        self.lock_file = self.directory / (name + '.lock')

    @contextmanager
    def locked(self):
        if self.directory.is_symlink():
            raise ValueError('symlink_rejected')
        self.directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        os.chmod(self.directory, 0o700)
        fd = os.open(self.lock_file, os.O_CREAT | os.O_RDWR |
                     getattr(os, 'O_NOFOLLOW', 0), 0o600)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX)
            yield
        finally:
            os.close(fd)

    def environment(self):
        allowed = PANEL_ENV if self.profile == 'panel' else DAEMON_ENV
        environment = {
            key: value for key, value in os.environ.items()
            if key in allowed
        }
        if self.profile == 'panel':
            environment['QW_ROOT'] = str(self.root)
            environment['QW_HOME'] = str(self.home)
            environment['QW_PORT'] = str(self.port)
        else:
            environment['QODERWAKE_HOME'] = str(self.home)
            if self.mode == 'gateway':
                if not isinstance(self.endpoint, str) or not self.endpoint:
                    raise ValueError('gateway_endpoint_required')
                environment.update({key: self.endpoint for key in GATEWAY_ENV})
            else:
                for key in GATEWAY_ENV:
                    environment.pop(key, None)
        return environment

    def child_environment(self):
        return {
            'HOME': str(Path.home()),
            'PATH': '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin',
            'LANG': 'C.UTF-8',
            **self.environment()
        }

    def command(self):
        if self.profile == 'panel':
            if self.script is None:
                raise ValueError('panel_script_required')
            return [str(self.launch), str(self.script)]
        return [str(self.launch), 'start', '--foreground']

    def write_state(self, state):
        state = validate_state(state)
        atomic(self.state_file, json.dumps(
            state, sort_keys=True, separators=(',', ':')).encode())

    def remove_state(self):
        try:
            self.state_file.unlink()
        except FileNotFoundError:
            return
        directory = os.open(self.directory, os.O_RDONLY |
                            getattr(os, 'O_DIRECTORY', 0))
        try:
            os.fsync(directory)
        finally:
            os.close(directory)

    def read_state(self):
        return read_state(self.state_file)

    def capture(self, pid, status='starting', version=None):
        observed = observe_process(pid, self.proc_root)
        environment = self.environment()
        if observed['uid'] != os.geteuid():
            raise ValueError('process_uid_mismatch')
        if observed['environment'].get(
                'QODERWAKE_HOME' if self.profile == 'daemon' else 'QW_HOME') != str(self.home):
            raise ValueError('process_home_mismatch')
        launch = self.launch_identity or file_identity(self.launch)
        script = (self.script_identity or file_identity(self.script)) if self.script else None
        state = {
            'schemaVersion': SCHEMA_VERSION,
            'name': self.name,
            'profile': self.profile,
            'status': status,
            'pid': pid,
            'startTicks': observed['startTicks'],
            'bootId': observed['bootId'],
            'uid': observed['uid'],
            'executable': observed['executable'],
            'launch': launch,
            'script': script,
            'cmdline': observed['cmdline'],
            'home': str(self.home),
            'port': self.port,
            'mode': self.mode,
            'endpoint': self.endpoint,
            'healthVersion': version,
            'environment': environment
        }
        return validate_state(state)

    def status(self):
        state = self.read_state()
        if state is None:
            return {'ok': True, 'name': self.name, 'managed': False,
                    'running': False}
        identity = identity_status(state, self.proc_root)
        port = port_status(state, self.proc_root) if identity == 'match' else 'unknown'
        version = health_version(self.health_url) if port == 'owned' else None
        healthy = (identity == 'match' and port == 'owned' and
                   state['status'] == 'running' and version is not None and
                   version == state['healthVersion'])
        return {
            'ok': True, 'name': self.name, 'managed': True,
            'running': identity == 'match', 'healthy': healthy,
            'identity': identity, 'port': port, 'pid': state['pid'],
            'mode': state['mode'], 'version': version
        }

    def stop(self):
        state = self.read_state()
        if state is None:
            if listening_socket_inodes(self.port, self.proc_root):
                raise ValueError('unmanaged_process_port_occupied')
            return {'ok': True, 'name': self.name, 'stopped': True}
        identity = identity_status(state, self.proc_root)
        if identity == 'exited':
            if listening_socket_inodes(self.port, self.proc_root):
                raise ValueError('managed_process_stop_port_occupied')
            self.remove_state()
            return {'ok': True, 'name': self.name, 'stopped': True}
        if identity != 'match':
            raise ValueError('managed_process_identity_unknown')
        if port_status(state, self.proc_root) == 'other':
            raise ValueError('managed_process_port_conflict')
        if (not hasattr(os, 'pidfd_open') or
                not hasattr(signal, 'pidfd_send_signal')):
            raise ValueError('managed_process_pidfd_required')
        try:
            pidfd = os.pidfd_open(state['pid'], 0)
        except ProcessLookupError:
            if (identity_status(state, self.proc_root) == 'exited' and
                    not listening_socket_inodes(self.port, self.proc_root)):
                self.remove_state()
                return {'ok': True, 'name': self.name, 'stopped': True}
            raise ValueError('managed_process_identity_unknown')
        except OSError:
            raise ValueError('managed_process_pidfd_required')
        try:
            if identity_status(state, self.proc_root) != 'match':
                raise ValueError('managed_process_identity_unknown')
            signal.pidfd_send_signal(pidfd, signal.SIGTERM)
            ready, _, _ = select.select([pidfd], [], [], 10)
            if ready:
                if listening_socket_inodes(self.port, self.proc_root):
                    raise ValueError('managed_process_stop_port_occupied')
                self.remove_state()
                return {'ok': True, 'name': self.name, 'stopped': True}
        finally:
            os.close(pidfd)
        raise ValueError('managed_process_stop_timeout')

    def prepare_log(self):
        if self.log.parent.is_symlink() or self.log.is_symlink():
            raise ValueError('symlink_rejected')
        self.log.parent.mkdir(parents=True, exist_ok=True)
        fd = os.open(self.log, os.O_WRONLY | os.O_CREAT | os.O_APPEND |
                     getattr(os, 'O_NOFOLLOW', 0), 0o600)
        os.fchmod(fd, 0o600)
        return fd

    def start(self):
        previous = self.read_state()
        if previous is not None:
            identity = identity_status(previous, self.proc_root)
            if identity == 'match':
                self.stop()
            elif identity != 'exited':
                raise ValueError('managed_process_identity_unknown')
            elif listening_socket_inodes(self.port, self.proc_root):
                raise ValueError('managed_process_identity_unknown')
            else:
                self.remove_state()
        elif listening_socket_inodes(self.port, self.proc_root):
            raise ValueError('unmanaged_process_port_occupied')
        self.launch_identity = file_identity(self.launch)
        self.script_identity = file_identity(self.script) if self.script else None
        fd = self.prepare_log()
        try:
            process = subprocess.Popen(
                self.command(), cwd=str(self.script.parent if self.script else self.root),
                env=self.child_environment(), stdin=subprocess.DEVNULL,
                stdout=fd, stderr=fd, start_new_session=True)
        finally:
            os.close(fd)
        state = None
        for _ in range(120):
            if process.poll() is not None:
                process.wait()
                self.remove_state()
                raise ValueError('managed_process_start_failed')
            try:
                state = self.capture(process.pid)
                self.write_state(state)
            except (OSError, ValueError):
                state = None
            if state and port_status(state, self.proc_root) == 'owned':
                version = health_version(self.health_url)
                if version is not None and (
                        self.expected_version is None or
                        version == self.expected_version):
                    state = dict(state, status='running',
                                 healthVersion=version)
                    self.write_state(state)
                    threading.Thread(target=process.wait,
                                     daemon=True).start()
                    return {
                        'ok': True, 'name': self.name, 'pid': process.pid,
                        'port': self.port, 'mode': self.mode,
                        'version': version
                    }
            time.sleep(.25)
        if state and identity_status(state, self.proc_root) == 'match':
            self.stop()
            process.wait(timeout=5)
            raise ValueError('managed_process_readiness_timeout')
        threading.Thread(target=process.wait, daemon=True).start()
        raise ValueError('managed_process_identity_unknown')


def controller_from_args(args):
    return Controller(
        args.root, args.name, args.profile, args.launch, args.home,
        args.port, args.mode, args.log, args.health_url,
        endpoint=args.endpoint, script=args.script,
        proc_root=args.proc_root, expected_version=args.expected_version)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('start', 'stop', 'status'))
    parser.add_argument('--root', required=True)
    parser.add_argument('--name', required=True)
    parser.add_argument('--profile', choices=('panel', 'daemon'), required=True)
    parser.add_argument('--launch', required=True)
    parser.add_argument('--script')
    parser.add_argument('--home', required=True)
    parser.add_argument('--port', type=int, required=True)
    parser.add_argument('--mode', choices=('panel', 'direct', 'gateway'), required=True)
    parser.add_argument('--endpoint')
    parser.add_argument('--log', required=True)
    parser.add_argument('--health-url', required=True)
    parser.add_argument('--expected-version')
    parser.add_argument('--proc-root', default='/proc', help=argparse.SUPPRESS)
    args = parser.parse_args()
    controller = controller_from_args(args)
    with controller.locked():
        result = getattr(controller, args.action)()
    print(json.dumps(result, sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        text = str(error)
        code = (text if isinstance(error, ValueError) and
                text.replace('_', '').isalnum()
                else 'process_control_failed')
        print(json.dumps({'ok': False, 'error': code}, sort_keys=True))
        sys.exit(1)
