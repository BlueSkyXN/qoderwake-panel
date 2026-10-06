"""Strict managed-gateway state, generation, journal, and Linux process identity."""
import hashlib
import json
import os
from pathlib import Path
import re
import stat

from gateway_policy import policy_hash

STATE_VERSION = 1
JOURNAL_VERSION = 1
APPROVED_UPSTREAMS = {'openapi.qoder.com.cn', 'openapi.qoder.sh'}
HASH_RE = re.compile(r'[0-9a-f]{64}')
GENERATION_RE = re.compile(r'generation-[0-9a-f]{24}')
NONCE_RE = re.compile(r'[0-9a-f]{32}')
BOOT_ID_RE = re.compile(r'[0-9a-f-]{16,64}')
STATE_KEYS = {'schemaVersion', 'generation', 'process', 'previous'}
GENERATION_KEYS = {
    'path', 'sourceHash', 'policyHash', 'runtimeHash', 'configHash',
    'mode', 'upstream', 'port', 'root'
}
PROCESS_KEYS = {
    'pid', 'startTicks', 'bootId', 'uid', 'executableDevice',
    'executableInode', 'cmdline', 'configPath', 'configHash',
    'port', 'root', 'upstream', 'nonce'
}
JOURNAL_KEYS = {
    'schemaVersion', 'operationId', 'action', 'phase', 'candidate',
    'previous', 'started', 'createdAt'
}


def _plain_int(value, minimum=0, maximum=None):
    return (type(value) is int and value >= minimum and
            (maximum is None or value <= maximum))


def validate_port(value):
    if not _plain_int(value, 1, 65535):
        raise ValueError('invalid_gateway_port')
    return value


def validate_upstream(value):
    if not isinstance(value, str) or value not in APPROVED_UPSTREAMS:
        raise ValueError('invalid_gateway_upstream')
    return value


def _resolved(path):
    return Path(path).resolve(strict=False)


def _generation_path(value, store):
    if not isinstance(value, str) or '\x00' in value:
        raise ValueError('invalid_gateway_generation')
    path = Path(value)
    if not path.is_absolute() or not GENERATION_RE.fullmatch(path.name):
        raise ValueError('invalid_gateway_generation')
    if path.parent.resolve(strict=False) != Path(store).resolve(strict=False):
        raise ValueError('invalid_gateway_generation')
    if path.resolve(strict=False) != path or path.is_symlink():
        raise ValueError('invalid_gateway_generation')
    return path


def validate_generation(value, store, root, port, require_directory=True):
    if not isinstance(value, dict) or set(value) != GENERATION_KEYS:
        raise ValueError('invalid_gateway_generation_state')
    path = _generation_path(value.get('path'), store)
    if require_directory and not path.is_dir():
        raise ValueError('invalid_gateway_generation')
    if any(not isinstance(value.get(key), str) or not HASH_RE.fullmatch(value[key])
           for key in ('sourceHash', 'policyHash', 'runtimeHash', 'configHash')):
        raise ValueError('invalid_gateway_generation_hash')
    if value.get('mode') not in ('observe', 'enforce', 'strict'):
        raise ValueError('invalid_gateway_generation_mode')
    validate_upstream(value.get('upstream'))
    if value.get('port') != validate_port(port):
        raise ValueError('gateway_port_mismatch')
    expected_root = str(_resolved(root))
    if value.get('root') != expected_root:
        raise ValueError('gateway_root_mismatch')
    return dict(value)


def generation_spec(path, source_hash, policy_digest, runtime_hash,
                    config_hash, mode, upstream, port, root):
    value = {
        'path': str(Path(path).resolve(strict=False)),
        'sourceHash': source_hash,
        'policyHash': policy_digest,
        'runtimeHash': runtime_hash,
        'configHash': config_hash,
        'mode': mode,
        'upstream': validate_upstream(upstream),
        'port': validate_port(port),
        'root': str(_resolved(root))
    }
    return validate_generation(value, Path(path).parent, root, port)


def _validate_process(value, generation):
    if not isinstance(value, dict) or set(value) != PROCESS_KEYS:
        raise ValueError('invalid_gateway_process_state')
    for key in ('pid', 'startTicks', 'executableInode'):
        if not _plain_int(value.get(key), 1):
            raise ValueError('invalid_gateway_process_identity')
    for key in ('uid', 'executableDevice'):
        if not _plain_int(value.get(key), 0):
            raise ValueError('invalid_gateway_process_identity')
    if not isinstance(value.get('bootId'), str) or not BOOT_ID_RE.fullmatch(value['bootId']):
        raise ValueError('invalid_gateway_process_identity')
    if not isinstance(value.get('nonce'), str) or not NONCE_RE.fullmatch(value['nonce']):
        raise ValueError('invalid_gateway_process_identity')
    cmdline = value.get('cmdline')
    script = str(Path(generation['path']) / 'uplink-gw.py')
    if (not isinstance(cmdline, list) or len(cmdline) != 3 or
            any(not isinstance(part, str) or not part for part in cmdline) or
            not Path(cmdline[0]).is_absolute() or
            cmdline[1:] != ['-B', script]):
        raise ValueError('invalid_gateway_process_command')
    expected = {
        'configPath': str(Path(generation['path']) / 'config.json'),
        'configHash': generation['configHash'],
        'port': generation['port'],
        'root': generation['root'],
        'upstream': generation['upstream']
    }
    if any(value.get(key) != expected_value for key, expected_value in expected.items()):
        raise ValueError('invalid_gateway_process_environment')
    return dict(value)


def validate_state(value, store, root, port, require_directory=True):
    if not isinstance(value, dict) or set(value) != STATE_KEYS or value.get('schemaVersion') != STATE_VERSION:
        raise ValueError('invalid_gateway_state')
    generation = validate_generation(value.get('generation'), store, root, port, require_directory)
    process = _validate_process(value.get('process'), generation)
    previous = value.get('previous')
    if previous is not None:
        previous = validate_generation(previous, store, root, port, require_directory)
        if previous['path'] == generation['path']:
            raise ValueError('invalid_gateway_previous_generation')
    return {'schemaVersion': STATE_VERSION, 'generation': generation,
            'process': process, 'previous': previous}


def _open_directory(path):
    path = Path(os.path.abspath(os.fspath(path)))
    flags = os.O_RDONLY | getattr(os, 'O_DIRECTORY', 0) | getattr(os, 'O_NOFOLLOW', 0)
    traverse = getattr(os, 'O_PATH', os.O_RDONLY) | getattr(os, 'O_DIRECTORY', 0) | getattr(os, 'O_NOFOLLOW', 0)
    fd = os.open(path.anchor or '/', traverse if len(path.parts) > 1 else flags)
    try:
        for index, part in enumerate(path.parts[1:], 1):
            child = os.open(part, flags if index == len(path.parts) - 1 else traverse, dir_fd=fd)
            os.close(fd)
            fd = child
        return fd
    except Exception:
        os.close(fd)
        raise


def read_json_file(path, limit=1024 * 1024, missing_ok=False):
    path = Path(os.path.abspath(os.fspath(path)))
    directory = -1
    fd = -1
    try:
        directory = _open_directory(path.parent)
        try:
            fd = os.open(path.name, os.O_RDONLY |
                         getattr(os, 'O_NOFOLLOW', 0), dir_fd=directory)
        except FileNotFoundError:
            if missing_ok:
                return None
            raise ValueError('gateway_state_unavailable')
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
            raise ValueError('invalid_gateway_state_file')
        with os.fdopen(fd, 'rb') as stream:
            fd = -1
            raw = stream.read(limit + 1)
    except FileNotFoundError:
        if missing_ok:
            return None
        raise ValueError('gateway_state_unavailable')
    except ValueError:
        raise
    except OSError:
        raise ValueError('gateway_state_unavailable')
    finally:
        if fd >= 0:
            os.close(fd)
        if directory >= 0:
            os.close(directory)
    if len(raw) > limit:
        raise ValueError('invalid_gateway_state_file')
    try:
        value = json.loads(raw)
    except (ValueError, UnicodeDecodeError):
        raise ValueError('invalid_gateway_state_file')
    if not isinstance(value, dict):
        raise ValueError('invalid_gateway_state_file')
    return value


def load_state(path, store, root, port, missing_ok=False):
    value = read_json_file(path, missing_ok=missing_ok)
    return None if value is None else validate_state(value, store, root, port)


def verify_generation_files(generation):
    directory = Path(generation['path'])
    checks = (
        ('uplink-gw.py', 'sourceHash'),
        ('gateway_policy.py', 'policyHash'),
        ('gateway_runtime.py', 'runtimeHash')
    )
    for name, key in checks:
        path = directory / name
        if path.is_symlink() or not path.is_file():
            raise ValueError('gateway_generation_changed')
        if hashlib.sha256(path.read_bytes()).hexdigest() != generation[key]:
            raise ValueError('gateway_generation_changed')
    config = directory / 'config.json'
    if config.is_symlink() or not config.is_file():
        raise ValueError('gateway_generation_changed')
    try:
        digest = policy_hash(json.loads(config.read_text()))
    except (OSError, ValueError, UnicodeDecodeError):
        raise ValueError('gateway_generation_changed')
    if digest != generation['configHash']:
        raise ValueError('gateway_generation_changed')
    return True


def _proc_stat_start_ticks(raw):
    close = raw.rfind(')')
    if close < 0:
        raise ValueError('invalid_proc_stat')
    fields = raw[close + 1:].strip().split()
    if len(fields) <= 19:
        raise ValueError('invalid_proc_stat')
    value = int(fields[19])
    if value <= 0:
        raise ValueError('invalid_proc_stat')
    return value


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
        except (UnicodeDecodeError, ValueError):
            continue
    return result


def observe_process(pid, proc_root='/proc'):
    if not _plain_int(pid, 1):
        raise ValueError('invalid_gateway_pid')
    proc_root = Path(proc_root)
    process = proc_root / str(pid)
    raw_cmdline = (process / 'cmdline').read_bytes()
    cmdline = [os.fsdecode(part) for part in raw_cmdline.split(b'\0') if part]
    environment = _proc_environment((process / 'environ').read_bytes())
    executable = (process / 'exe').stat()
    boot_id = (proc_root / 'sys/kernel/random/boot_id').read_text().strip().lower()
    if not BOOT_ID_RE.fullmatch(boot_id):
        raise ValueError('invalid_proc_boot_id')
    config_hash = environment.get('QW_GW_CONFIG_HASH', '')
    port_text = environment.get('QW_GW_PORT', '')
    if not port_text.isdigit():
        raise ValueError('invalid_gateway_process_environment')
    return {
        'pid': pid,
        'startTicks': _proc_stat_start_ticks((process / 'stat').read_text()),
        'bootId': boot_id,
        'uid': _proc_uid((process / 'status').read_text()),
        'executableDevice': executable.st_dev,
        'executableInode': executable.st_ino,
        'cmdline': cmdline,
        'configPath': environment.get('QW_GW_CONFIG', ''),
        'configHash': config_hash,
        'port': int(port_text),
        'root': environment.get('QW_ROOT', ''),
        'upstream': environment.get('QW_GW_UPSTREAM', ''),
        'nonce': environment.get('QW_GW_NONCE', '')
    }


def capture_process(pid, generation, uid, proc_root='/proc'):
    observed = observe_process(pid, proc_root)
    if observed['uid'] != uid:
        raise ValueError('gateway_process_identity_mismatch')
    _validate_process(observed, generation)
    return observed


def identity_status(state, proc_root='/proc'):
    try:
        expected = _validate_process(state['process'], state['generation'])
    except (OSError, ValueError, KeyError, TypeError):
        return 'unknown'
    process = Path(proc_root) / str(expected['pid'])
    try:
        observed = observe_process(expected['pid'], proc_root)
    except (FileNotFoundError, ProcessLookupError):
        try:
            process.stat()
        except FileNotFoundError:
            try:
                boot_id = (Path(proc_root) / 'sys/kernel/random/boot_id').read_text().strip().lower()
            except OSError:
                return 'unknown'
            if not BOOT_ID_RE.fullmatch(boot_id):
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
    return 'match' if observed == expected else 'mismatch'


def listening_socket_inodes(port, proc_root='/proc'):
    listeners = set()
    proc_root = Path(proc_root)
    allowed = {
        ('tcp', '0100007F'),
        ('tcp6', '0000000000000000FFFF00000100007F'),
        ('tcp6', '00000000000000000000000001000000')
    }
    for name in ('tcp', 'tcp6'):
        try:
            lines = (proc_root / 'net' / name).read_text().splitlines()[1:]
        except OSError:
            raise ValueError('gateway_port_visibility_required')
        for line in lines:
            fields = line.split()
            if len(fields) < 10:
                continue
            try:
                address, raw_port = fields[1].rsplit(':', 1)
                local_port = int(raw_port, 16)
            except (ValueError, IndexError):
                continue
            if local_port == validate_port(port) and fields[3] == '0A':
                listeners.add((fields[9], (name, address.upper()) in allowed))
    return listeners


def process_socket_inodes(pid, proc_root='/proc'):
    result = set()
    try:
        entries = list((Path(proc_root) / str(pid) / 'fd').iterdir())
    except OSError:
        raise ValueError('gateway_port_visibility_required')
    for entry in entries:
        try:
            target = os.readlink(entry)
        except (FileNotFoundError, ProcessLookupError):
            continue
        except OSError:
            raise ValueError('gateway_port_visibility_required')
        match = re.fullmatch(r'socket:\[(\d+)\]', target)
        if match:
            result.add(match.group(1))
    return result


def gateway_port_status(state, proc_root='/proc'):
    try:
        listeners = listening_socket_inodes(
            state['generation']['port'], proc_root)
        owned = process_socket_inodes(state['process']['pid'], proc_root)
    except (KeyError, TypeError, ValueError):
        return 'unknown'
    if not listeners:
        return 'unbound'
    if any(not loopback for _, loopback in listeners):
        return 'other'
    return 'owned' if {inode for inode, _ in listeners} <= owned else 'other'


def validate_journal(value, store, root, port):
    if (not isinstance(value, dict) or set(value) != JOURNAL_KEYS or
            value.get('schemaVersion') != JOURNAL_VERSION):
        raise ValueError('invalid_gateway_journal')
    if not isinstance(value.get('operationId'), str) or not NONCE_RE.fullmatch(value['operationId']):
        raise ValueError('invalid_gateway_journal')
    if value.get('action') not in ('apply', 'rollback'):
        raise ValueError('invalid_gateway_journal')
    if value.get('phase') not in ('prepared', 'new-started', 'committing'):
        raise ValueError('invalid_gateway_journal')
    if not _plain_int(value.get('createdAt'), 1):
        raise ValueError('invalid_gateway_journal')
    candidate = validate_generation(value.get('candidate'), store, root, port)
    previous = value.get('previous')
    if previous is not None:
        previous = validate_state(previous, store, root, port)
    started = value.get('started')
    if started is not None:
        started = validate_state(started, store, root, port)
        if started['generation'] != candidate:
            raise ValueError('invalid_gateway_journal')
    if value['phase'] == 'prepared' and started is not None:
        raise ValueError('invalid_gateway_journal')
    if value['phase'] != 'prepared' and started is None:
        raise ValueError('invalid_gateway_journal')
    expected_previous = previous['generation'] if previous else None
    if started is not None and started.get('previous') != expected_previous:
        raise ValueError('invalid_gateway_journal')
    if value['action'] == 'rollback':
        if previous is None or previous.get('previous') != candidate:
            raise ValueError('invalid_gateway_journal')
    elif previous and previous['generation'] == candidate:
        raise ValueError('invalid_gateway_journal')
    return dict(value, candidate=candidate, previous=previous, started=started)


def load_journal(path, store, root, port, missing_ok=False):
    value = read_json_file(path, missing_ok=missing_ok)
    return None if value is None else validate_journal(value, store, root, port)


def referenced_generations(state=None, journal=None):
    result = set()
    if state:
        result.add(state['generation']['path'])
        if state.get('previous'):
            result.add(state['previous']['path'])
    if journal:
        result.add(journal['candidate']['path'])
        if journal.get('previous'):
            result.add(journal['previous']['generation']['path'])
            if journal['previous'].get('previous'):
                result.add(journal['previous']['previous']['path'])
        if journal.get('started'):
            result.add(journal['started']['generation']['path'])
    return result


def proc_generation_paths(proc_root='/proc'):
    result = set()
    complete = True
    proc_root = Path(proc_root)
    try:
        entries = list(proc_root.iterdir())
    except OSError:
        return result, False
    for entry in entries:
        if not entry.name.isdigit():
            continue
        try:
            env = _proc_environment((entry / 'environ').read_bytes())
            config = Path(env.get('QW_GW_CONFIG', ''))
            if config.name == 'config.json' and GENERATION_RE.fullmatch(config.parent.name):
                result.add(str(config.parent.resolve(strict=False)))
        except (FileNotFoundError, ProcessLookupError):
            continue
        except OSError:
            complete = False
    return result, complete
