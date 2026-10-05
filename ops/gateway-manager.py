#!/usr/bin/env python3
"""Preflight immutable gateway generations, switch with identity gates, and recover journals."""
import argparse
from contextlib import contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path
import pwd
import secrets
import select
import shutil
import signal
import socket
import subprocess
import sys
import threading
import time
import urllib.request

HERE = Path(__file__).resolve().parent
PANEL = HERE.parent / 'panel' if (HERE.parent / 'panel/gateway_policy.py').is_file() else HERE
sys.path.insert(0, str(PANEL))
from gateway_policy import policy_hash, validate_cfg
from gateway_runtime import (
    JOURNAL_VERSION, STATE_VERSION, capture_process, gateway_port_status,
    generation_spec, identity_status, load_journal, load_state,
    proc_generation_paths,
    referenced_generations, validate_generation, validate_journal,
    validate_port, validate_state, validate_upstream, verify_generation_files
)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def _open_directory(path):
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


def path_entry_exists(path, unavailable='gateway_state_unavailable'):
    try:
        os.lstat(path)
        return True
    except FileNotFoundError:
        return False
    except OSError:
        raise ValueError(unavailable)


def atomic(path, data, mode=0o600, gid=None):
    path = Path(path)
    directory = _open_directory(path.parent)
    tmp = '.' + path.name + '-' + secrets.token_hex(6)
    fd = -1
    try:
        try:
            target = os.stat(path.name, dir_fd=directory,
                             follow_symlinks=False)
            if __import__('stat').S_ISLNK(target.st_mode):
                raise ValueError('symlink_rejected')
        except FileNotFoundError:
            pass
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL |
                     getattr(os, 'O_NOFOLLOW', 0), mode,
                     dir_fd=directory)
        with os.fdopen(fd, 'wb') as stream:
            fd = -1
            stream.write(data)
            if gid is not None and os.geteuid() == 0:
                os.fchown(stream.fileno(), 0, gid)
            os.fchmod(stream.fileno(), mode)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(tmp, path.name, src_dir_fd=directory,
                   dst_dir_fd=directory)
        os.fsync(directory)
    finally:
        if fd >= 0:
            os.close(fd)
        try:
            os.unlink(tmp, dir_fd=directory)
        except FileNotFoundError:
            pass
        os.close(directory)


def unlink_atomic(path):
    path = Path(path)
    directory = _open_directory(path.parent)
    try:
        try:
            os.unlink(path.name, dir_fd=directory)
        except FileNotFoundError:
            return
        os.fsync(directory)
    finally:
        os.close(directory)


def read_regular(path, limit=4 * 1024 * 1024):
    path = Path(path)
    directory = _open_directory(path.parent)
    try:
        return read_regular_at(directory, path.name, limit)
    finally:
        os.close(directory)


def read_regular_at(directory, name, limit=4 * 1024 * 1024):
    try:
        fd = os.open(name, os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0),
                     dir_fd=directory)
    except FileNotFoundError:
        raise
    except OSError:
        raise ValueError('gateway_source_unavailable')
    try:
        info = os.fstat(fd)
        if not __import__('stat').S_ISREG(info.st_mode) or info.st_size > limit:
            raise ValueError('gateway_source_unavailable')
        with os.fdopen(fd, 'rb') as stream:
            fd = -1
            data = stream.read(limit + 1)
    finally:
        if fd >= 0:
            os.close(fd)
    if len(data) > limit:
        raise ValueError('gateway_source_unavailable')
    return data


class Manager:
    def __init__(self, root, script, config, port, account, proc_root='/proc'):
        raw_root = Path(os.path.abspath(os.fspath(root)))
        raw_config = Path(os.path.abspath(os.fspath(config)))
        if raw_config.parent != raw_root / 'config':
            raise ValueError('invalid_gateway_config_path')
        self.root = raw_root.resolve(strict=False)
        self.script = Path(script)
        self.config = self.root / 'config' / raw_config.name
        self.port = validate_port(int(port))
        self.account = account
        self.proc_root = Path(proc_root)
        self.store = self.root / 'gateway-runtime'
        self.state_file = self.store / 'current.json'
        self.journal_file = self.store / 'operation.json'

    @contextmanager
    def locked(self):
        if self.store.is_symlink():
            raise ValueError('symlink_rejected')
        self.store.mkdir(parents=True, exist_ok=True, mode=0o750)
        if os.geteuid() == 0:
            os.chown(self.store, 0, self.account.pw_gid)
        os.chmod(self.store, 0o750)
        fd = os.open(self.store / 'manager.lock',
                     os.O_CREAT | os.O_RDWR | getattr(os, 'O_NOFOLLOW', 0),
                     0o600)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX)
            self.recover_gc_quarantines()
            yield
        finally:
            os.close(fd)

    def state(self):
        return load_state(
            self.state_file, self.store, self.root, self.port,
            missing_ok=True)

    def journal(self):
        return load_journal(
            self.journal_file, self.store, self.root, self.port,
            missing_ok=True)

    def busy(self):
        with socket.socket() as sock:
            sock.settimeout(.3)
            return sock.connect_ex(('127.0.0.1', self.port)) == 0

    def process_status(self, state):
        try:
            validate_state(state, self.store, self.root, self.port)
        except (ValueError, TypeError):
            return 'unknown'
        return identity_status(state, self.proc_root)

    def process_matches(self, state):
        return self.process_status(state) == 'match'

    def health_response(self):
        opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}), NoRedirect())
        with opener.open(
                'http://127.0.0.1:%d/__qwp_gateway_health' % self.port,
                timeout=1) as response:
            raw = response.read(65537)
        if len(raw) > 65536:
            raise ValueError('gateway_health_invalid')
        value = json.loads(raw)
        if not isinstance(value, dict):
            raise ValueError('gateway_health_invalid')
        return value

    def health(self, state):
        if (self.process_status(state) != 'match' or
                gateway_port_status(state, self.proc_root) != 'owned'):
            return False
        generation, process = state['generation'], state['process']
        try:
            value = self.health_response()
            fields_match = (
                set(value) == {
                    'schemaVersion', 'service', 'pid', 'nonce',
                    'sourceHash', 'policyHash', 'runtimeHash', 'configHash', 'mode',
                    'port', 'root', 'upstream', 'configPath'
                } and value['schemaVersion'] == STATE_VERSION and
                value['service'] == 'qw-control-gateway' and
                value['pid'] == process['pid'] and
                value['nonce'] == process['nonce'] and
                value['sourceHash'] == generation['sourceHash'] and
                value['policyHash'] == generation['policyHash'] and
                value['runtimeHash'] == generation['runtimeHash'] and
                value['configHash'] == generation['configHash'] and
                value['mode'] == generation['mode'] and
                value['port'] == generation['port'] and
                value['root'] == generation['root'] and
                value['upstream'] == generation['upstream'] and
                value['configPath'] == process['configPath'])
            return (fields_match and self.process_status(state) == 'match' and
                    gateway_port_status(state, self.proc_root) == 'owned')
        except Exception:
            return False

    def upstream(self):
        return validate_upstream(
            os.environ.get('QW_GW_UPSTREAM') or 'openapi.qoder.com.cn')

    def snapshot(self, candidate):
        source = read_regular(self.script)
        policy_path = Path(__import__('gateway_policy').__file__)
        runtime_path = Path(__import__('gateway_runtime').__file__)
        policy = read_regular(policy_path)
        runtime = read_regular(runtime_path)
        cfg = validate_cfg(candidate)
        generation = self.store / ('generation-' + secrets.token_hex(12))
        generation.mkdir(mode=0o750)
        if os.geteuid() == 0:
            os.chown(generation, 0, self.account.pw_gid)
        try:
            for name, data in (
                    ('uplink-gw.py', source),
                    ('gateway_policy.py', policy),
                    ('gateway_runtime.py', runtime),
                    ('config.json', json.dumps(
                        cfg, sort_keys=True, separators=(',', ':')).encode())):
                atomic(generation / name, data, 0o640, self.account.pw_gid)
            return generation_spec(
                generation, hashlib.sha256(source).hexdigest(),
                hashlib.sha256(policy).hexdigest(),
                hashlib.sha256(runtime).hexdigest(), policy_hash(cfg),
                cfg['mode'], self.upstream(), self.port, self.root)
        except Exception:
            self.remove_generation_path(generation)
            raise

    def verify_generation(self, generation):
        generation = validate_generation(
            generation, self.store, self.root, self.port)
        return verify_generation_files(generation)

    def launch_environment(self, generation, nonce):
        return {
            'PYTHONDONTWRITEBYTECODE': '1',
            'QW_ROOT': generation['root'],
            'QW_GW_PORT': str(generation['port']),
            'QW_GW_CONFIG': str(Path(generation['path']) / 'config.json'),
            'QW_GW_CONFIG_HASH': generation['configHash'],
            'QW_GW_NONCE': nonce,
            'QW_GW_UPSTREAM': generation['upstream']
        }

    def launch_options(self, generation, nonce):
        options = {
            'env': self.launch_environment(generation, nonce),
            'cwd': generation['path'],
            'stdin': subprocess.DEVNULL
        }
        if os.geteuid() == 0:
            options.update(user=self.account.pw_uid,
                           group=self.account.pw_gid, extra_groups=[])
        elif os.geteuid() != self.account.pw_uid:
            raise ValueError('gateway_user_unavailable')
        return options

    def prepare_log(self):
        directory = self.root / 'logs'
        if directory.is_symlink():
            raise ValueError('symlink_rejected')
        directory.mkdir(parents=True, exist_ok=True)
        for name in ('uplink-gw.jsonl', 'uplink-gw-manager.log'):
            path = directory / name
            fd = os.open(path, os.O_CREAT | os.O_APPEND | os.O_WRONLY |
                         getattr(os, 'O_NOFOLLOW', 0), 0o600)
            try:
                if os.geteuid() == 0:
                    os.fchown(fd, self.account.pw_uid,
                              self.account.pw_gid)
                os.fchmod(fd, 0o600)
            finally:
                os.close(fd)

    def preflight_candidate(self, candidate):
        generation = self.snapshot(candidate)
        try:
            self.preflight(generation)
            return {
                'ok': True,
                'mode': generation['mode'],
                'configHash': generation['configHash'],
                'activeUnchanged': True
            }
        finally:
            self.remove_generation(generation)

    def preflight(self, generation):
        self.verify_generation(generation)
        nonce = secrets.token_hex(16)
        result = subprocess.run(
            [os.path.realpath(sys.executable), '-B',
             str(Path(generation['path']) / 'uplink-gw.py'), '--check'],
            capture_output=True, timeout=15,
            **self.launch_options(generation, nonce))
        if result.returncode:
            raise ValueError(
                'gateway_preflight_failed_check_user_permissions')
        try:
            value = json.loads(result.stdout)
        except ValueError:
            raise ValueError('gateway_preflight_invalid_response')
        expected_keys = {
            'ok', 'configHash', 'sourceHash', 'policyHash', 'runtimeHash', 'port',
            'root', 'upstream'
        }
        if (not isinstance(value, dict) or set(value) != expected_keys or
                value.get('ok') is not True or
                value.get('configHash') != generation['configHash'] or
                value.get('sourceHash') != generation['sourceHash'] or
                value.get('policyHash') != generation['policyHash'] or
                value.get('runtimeHash') != generation['runtimeHash'] or
                value.get('port') != generation['port'] or
                value.get('root') != generation['root'] or
                value.get('upstream') != generation['upstream']):
            raise ValueError('gateway_preflight_hash_mismatch')

    def require_stoppable_port(self, state):
        if gateway_port_status(state, self.proc_root) not in ('owned', 'unbound'):
            raise ValueError('gateway_listener_identity_unknown')

    def stop(self, state):
        status = self.process_status(state)
        if status == 'exited':
            if self.busy():
                raise ValueError('gateway_listener_identity_unknown')
            return
        if status != 'match':
            raise ValueError('gateway_process_identity_unknown')
        self.require_stoppable_port(state)
        if (not hasattr(os, 'pidfd_open') or
                not hasattr(signal, 'pidfd_send_signal')):
            raise ValueError('gateway_pidfd_required')
        try:
            pidfd = os.pidfd_open(state['process']['pid'], 0)
        except ProcessLookupError:
            if self.process_status(state) == 'exited' and not self.busy():
                return
            raise ValueError('gateway_process_identity_unknown')
        except OSError:
            raise ValueError('gateway_pidfd_required')
        try:
            if self.process_status(state) != 'match':
                raise ValueError('gateway_process_identity_changed')
            self.require_stoppable_port(state)
            signal.pidfd_send_signal(pidfd, signal.SIGTERM)
            ready, _, _ = select.select([pidfd], [], [], 5)
            if ready:
                if self.busy():
                    raise ValueError('gateway_listener_identity_unknown')
                return
        finally:
            os.close(pidfd)
        raise ValueError('gateway_stop_timeout')

    def start(self, generation, previous=None):
        generation = validate_generation(
            generation, self.store, self.root, self.port)
        self.verify_generation(generation)
        self.prepare_log()
        nonce = secrets.token_hex(16)
        fd = os.open(self.root / 'logs/uplink-gw-manager.log',
                     os.O_CREAT | os.O_APPEND | os.O_WRONLY |
                     getattr(os, 'O_NOFOLLOW', 0), 0o600)
        try:
            process = subprocess.Popen(
                [os.path.realpath(sys.executable), '-B',
                 str(Path(generation['path']) / 'uplink-gw.py')],
                stdout=fd, stderr=fd, start_new_session=True,
                **self.launch_options(generation, nonce))
        finally:
            os.close(fd)
        state = None
        for _ in range(60):
            if process.poll() is not None:
                process.wait()
                raise ValueError('gateway_start_failed')
            try:
                observed = capture_process(
                    process.pid, generation, self.account.pw_uid,
                    self.proc_root)
                state = validate_state({
                    'schemaVersion': STATE_VERSION,
                    'generation': generation,
                    'process': observed,
                    'previous': previous
                }, self.store, self.root, self.port)
            except (OSError, ValueError):
                state = None
            if state and self.health(state):
                threading.Thread(target=process.wait, daemon=True).start()
                return state
            time.sleep(.1)
        if state and self.process_status(state) == 'match':
            self.stop(state)
            process.wait(timeout=5)
            raise ValueError('gateway_health_timeout')
        threading.Thread(target=process.wait, daemon=True).start()
        raise ValueError('gateway_start_identity_unknown')

    def write_state(self, state):
        state = validate_state(state, self.store, self.root, self.port)
        atomic(self.state_file, json.dumps(
            state, sort_keys=True, separators=(',', ':')).encode())

    def publish(self, state):
        state = validate_state(state, self.store, self.root, self.port)
        self.verify_generation(state['generation'])
        config_parent = self.root / 'config'
        try:
            config_parent.mkdir(mode=0o750)
        except FileExistsError:
            pass
        try:
            directory = _open_directory(config_parent)
        except OSError:
            raise ValueError('gateway_config_directory_unavailable')
        try:
            try:
                previous = read_regular_at(directory, self.config.name)
                metadata = os.stat(
                    self.config.name, dir_fd=directory,
                    follow_symlinks=False)
            except FileNotFoundError:
                previous = metadata = None
            except ValueError:
                raise
            except OSError:
                raise ValueError('gateway_config_unavailable')
        finally:
            os.close(directory)
        config_bytes = read_regular(
            Path(state['generation']['path']) / 'config.json')
        try:
            config_digest = policy_hash(json.loads(config_bytes))
        except (ValueError, UnicodeDecodeError):
            raise ValueError('gateway_generation_changed')
        if config_digest != state['generation']['configHash']:
            raise ValueError('gateway_generation_changed')
        try:
            atomic(self.config, config_bytes, 0o640, self.account.pw_gid)
        except Exception:
            raise ValueError('gateway_publish_commit_unknown')
        try:
            self.write_state(state)
        except Exception as error:
            try:
                observed = self.state()
            except ValueError:
                raise ValueError('gateway_publish_commit_unknown')
            if observed == state:
                raise ValueError('gateway_publish_commit_unknown')
            try:
                if previous is None:
                    unlink_atomic(self.config)
                else:
                    atomic(self.config, previous,
                           metadata.st_mode & 0o777, metadata.st_gid)
            except Exception:
                raise ValueError('gateway_publish_commit_unknown')
            raise error

    def write_journal(self, action, phase, candidate, previous=None,
                      started=None, operation_id=None, created_at=None):
        value = validate_journal({
            'schemaVersion': JOURNAL_VERSION,
            'operationId': operation_id or secrets.token_hex(16),
            'action': action,
            'phase': phase,
            'candidate': candidate,
            'previous': previous,
            'started': started,
            'createdAt': created_at or int(time.time())
        }, self.store, self.root, self.port)
        atomic(self.journal_file, json.dumps(
            value, sort_keys=True, separators=(',', ':')).encode())
        return value

    def clear_journal(self):
        unlink_atomic(self.journal_file)

    def finish_journal(self, journal):
        try:
            self.clear_journal()
        except Exception:
            try:
                if not path_entry_exists(
                        self.journal_file,
                        'gateway_journal_visibility_unknown'):
                    self.write_journal(
                        journal['action'], journal['phase'],
                        journal['candidate'], journal.get('previous'),
                        journal.get('started'), journal['operationId'],
                        journal['createdAt'])
            finally:
                raise ValueError('gateway_commit_cleanup_failed')

    def publish_and_clear(self, state, journal):
        self.publish(state)
        self.finish_journal(journal)

    def stop_or_require_exited(self, state):
        self.stop(state)

    def remove_generation_path(self, path):
        path = Path(path)
        if (path.parent.resolve(strict=False) !=
                self.store.resolve(strict=False) or
                not __import__('re').fullmatch(r'generation-[0-9a-f]{24}', path.name) or
                path.is_symlink()):
            return False
        try:
            shutil.rmtree(path)
        except FileNotFoundError:
            return True
        directory = os.open(self.store, os.O_RDONLY |
                            getattr(os, 'O_DIRECTORY', 0))
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
        return True

    def remove_generation(self, generation):
        if isinstance(generation, dict) and isinstance(generation.get('path'), str):
            return self.remove_generation_path(generation['path'])
        return False

    def recover_gc_quarantines(self):
        pattern = __import__('re').compile(
            r'\.gc-(generation-[0-9a-f]{24})-[0-9a-f]{12}')
        for quarantine in self.store.glob('.gc-generation-*'):
            match = pattern.fullmatch(quarantine.name)
            if not match or quarantine.is_symlink() or not quarantine.is_dir():
                raise ValueError('gateway_gc_manual_recovery_required')
            original = self.store / match.group(1)
            if path_entry_exists(
                    original, 'gateway_gc_manual_recovery_required'):
                raise ValueError('gateway_gc_manual_recovery_required')
            self.restore_quarantine(quarantine, original)

    def quarantine_generation(self, path):
        path = Path(path)
        if (path.parent.resolve(strict=False) !=
                self.store.resolve(strict=False) or
                not __import__('re').fullmatch(
                    r'generation-[0-9a-f]{24}', path.name) or
                path.is_symlink()):
            return None
        quarantine = self.store / (
            '.gc-' + path.name + '-' + secrets.token_hex(6))
        directory = _open_directory(self.store)
        try:
            try:
                os.replace(path.name, quarantine.name,
                           src_dir_fd=directory, dst_dir_fd=directory)
            except FileNotFoundError:
                return None
            os.fsync(directory)
        finally:
            os.close(directory)
        return quarantine

    def restore_quarantine(self, quarantine, original):
        quarantine, original = Path(quarantine), Path(original)
        directory = _open_directory(self.store)
        try:
            os.replace(quarantine.name, original.name,
                       src_dir_fd=directory, dst_dir_fd=directory)
            os.fsync(directory)
        finally:
            os.close(directory)

    def remove_quarantine(self, quarantine):
        quarantine = Path(quarantine)
        if (quarantine.parent.resolve(strict=False) !=
                self.store.resolve(strict=False) or
                not __import__('re').fullmatch(
                    r'\.gc-generation-[0-9a-f]{24}-[0-9a-f]{12}',
                    quarantine.name) or quarantine.is_symlink()):
            raise ValueError('invalid_gateway_gc_quarantine')
        shutil.rmtree(quarantine)
        directory = _open_directory(self.store)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)

    def discover_candidate_state(self, generation, previous=None):
        config_path = str(Path(generation['path']) / 'config.json')
        try:
            entries = list(self.proc_root.iterdir())
        except OSError:
            raise ValueError('gateway_journal_process_visibility_unknown')
        for entry in entries:
            if not entry.name.isdigit():
                continue
            try:
                environment = (entry / 'environ').read_bytes().split(b'\0')
            except (FileNotFoundError, ProcessLookupError):
                continue
            except OSError:
                raise ValueError(
                    'gateway_journal_process_visibility_unknown')
            if ('QW_GW_CONFIG=' + config_path).encode() not in environment:
                continue
            try:
                process = capture_process(
                    int(entry.name), generation, self.account.pw_uid,
                    self.proc_root)
                return validate_state({
                    'schemaVersion': STATE_VERSION,
                    'generation': generation,
                    'process': process,
                    'previous': previous
                }, self.store, self.root, self.port)
            except (OSError, ValueError):
                raise ValueError(
                    'gateway_journal_candidate_identity_unknown')
        return None

    def recover(self):
        try:
            journal = self.journal()
            current = self.state()
        except ValueError:
            raise ValueError('gateway_journal_manual_recovery_required')
        if journal is None:
            return None
        candidate = journal['candidate']
        previous = journal.get('previous')
        started = journal.get('started')
        if current and self.health(current) and current['generation'] == candidate:
            self.publish_and_clear(current, journal)
            return 'candidate_committed'
        if started and self.health(started):
            self.publish_and_clear(started, journal)
            return 'candidate_committed'
        discovered = None
        candidate_stopped = False
        if journal['phase'] == 'prepared':
            discovered = self.discover_candidate_state(
                candidate,
                previous['generation'] if previous else None)
            if discovered and self.health(discovered):
                self.publish_and_clear(discovered, journal)
                return 'candidate_committed'
        candidate_state = started or discovered
        if candidate_state:
            self.stop_or_require_exited(candidate_state)
            candidate_stopped = True
        if previous and current and self.health(current) and \
                current['generation'] == previous['generation']:
            self.finish_journal(journal)
            if journal['action'] == 'apply':
                self.remove_generation(candidate)
            return 'previous_active'
        if previous and self.health(previous):
            self.publish(previous)
            self.finish_journal(journal)
            if journal['action'] == 'apply':
                self.remove_generation(candidate)
            return 'previous_active'
        if self.busy():
            raise ValueError('gateway_journal_listener_identity_unknown')
        if previous:
            previous_status = self.process_status(previous)
            if previous_status == 'match':
                if candidate_stopped:
                    raise ValueError('gateway_listener_identity_unknown')
                self.stop(previous)
            elif previous_status != 'exited':
                raise ValueError('gateway_process_identity_unknown')
            restored = self.start(
                previous['generation'], previous.get('previous'))
            self.publish(restored)
            self.finish_journal(journal)
            if journal['action'] == 'apply':
                self.remove_generation(candidate)
            return 'previous_restored'
        if (journal['phase'] == 'prepared' and
                (discovered is None or candidate_stopped)):
            self.finish_journal(journal)
            self.remove_generation(candidate)
            return 'aborted_before_start'
        raise ValueError('gateway_journal_manual_recovery_required')

    def gc_snapshot(self):
        try:
            state = self.state()
            journal = self.journal()
        except ValueError:
            return None
        process_paths, complete = proc_generation_paths(self.proc_root)
        if not complete:
            return None
        return referenced_generations(state, journal) | process_paths

    def gc(self):
        if not self.proc_root.is_dir():
            return []
        keep = self.gc_snapshot()
        if keep is None:
            return []
        removed = []
        for path in self.store.glob('generation-*'):
            original = str(path.resolve(strict=False))
            if path.is_symlink() or original in keep:
                continue
            quarantine = self.quarantine_generation(path)
            if quarantine is None:
                continue
            refreshed = self.gc_snapshot()
            if refreshed is None or original in refreshed:
                self.restore_quarantine(quarantine, path)
                if refreshed is None:
                    return removed
                continue
            self.remove_quarantine(quarantine)
            removed.append(original)
        return removed

    def apply(self, candidate):
        self.recover()
        generation = self.snapshot(candidate)
        try:
            self.preflight(generation)
            previous = self.state()
            if self.busy() and (not previous or not self.health(previous)):
                raise ValueError(
                    'unmanaged_gateway_running_migration_required')
            if previous:
                self.preflight(previous['generation'])
            journal = self.write_journal(
                'apply', 'prepared', generation, previous=previous)
            if previous:
                self.stop(previous)
            current = None
            published = False
            try:
                current = self.start(
                    generation,
                    previous['generation'] if previous else None)
                journal = self.write_journal(
                    'apply', 'new-started', generation, previous, current,
                    journal['operationId'], journal['createdAt'])
                journal = self.write_journal(
                    'apply', 'committing', generation, previous, current,
                    journal['operationId'], journal['createdAt'])
                self.publish(current)
                published = True
                self.finish_journal(journal)
            except Exception as error:
                if str(error) == 'gateway_publish_commit_unknown':
                    raise
                if published or str(error) == 'gateway_commit_cleanup_failed':
                    raise ValueError('gateway_commit_cleanup_failed')
                if current:
                    self.stop_or_require_exited(current)
                elif str(error) not in (
                        'gateway_start_failed', 'gateway_health_timeout'):
                    raise
                if previous:
                    try:
                        restored = self.start(
                            previous['generation'], previous.get('previous'))
                        self.publish(restored)
                        self.finish_journal(journal)
                    except Exception:
                        raise ValueError(
                            'gateway_apply_failed_rollback_failed')
                    self.remove_generation(generation)
                    raise ValueError(
                        'gateway_apply_failed_previous_restored')
                self.finish_journal(journal)
                self.remove_generation(generation)
                raise
            self.gc()
            return {
                'ok': True, 'mode': current['generation']['mode'],
                'configHash': current['generation']['configHash'],
                'pid': current['process']['pid']
            }
        except Exception as error:
            if str(error) in (
                    'gateway_commit_cleanup_failed',
                    'gateway_publish_commit_unknown'):
                raise
            try:
                journal_present = path_entry_exists(
                    self.journal_file,
                    'gateway_journal_visibility_unknown')
            except ValueError:
                journal_present = True
            if not journal_present:
                self.remove_generation(generation)
            raise

    def rollback(self):
        self.recover()
        current = self.state()
        previous = current.get('previous') if current else None
        if not previous:
            raise ValueError('gateway_rollback_unavailable')
        self.preflight(previous)
        if not self.health(current):
            raise ValueError('gateway_process_identity_unknown')
        journal = self.write_journal(
            'rollback', 'prepared', previous, previous=current)
        self.stop(current)
        restored = None
        published = False
        try:
            restored = self.start(previous, current['generation'])
            journal = self.write_journal(
                'rollback', 'new-started', previous, current, restored,
                journal['operationId'], journal['createdAt'])
            journal = self.write_journal(
                'rollback', 'committing', previous, current, restored,
                journal['operationId'], journal['createdAt'])
            self.publish(restored)
            published = True
            self.finish_journal(journal)
        except Exception as error:
            if str(error) == 'gateway_publish_commit_unknown':
                raise
            if published or str(error) == 'gateway_commit_cleanup_failed':
                raise ValueError('gateway_commit_cleanup_failed')
            if restored:
                self.stop_or_require_exited(restored)
            elif str(error) not in (
                    'gateway_start_failed', 'gateway_health_timeout'):
                raise
            try:
                recovered = self.start(
                    current['generation'], current.get('previous'))
                self.publish(recovered)
                self.finish_journal(journal)
            except Exception:
                raise ValueError(
                    'gateway_rollback_failed_recovery_failed')
            raise ValueError('gateway_rollback_failed_current_restored')
        self.gc()
        return {
            'ok': True, 'mode': restored['generation']['mode'],
            'configHash': restored['generation']['configHash']
        }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=(
        'preflight', 'start', 'apply', 'rollback', 'status', 'stop'))
    args = parser.parse_args()
    root = Path(os.environ.get('QW_ROOT') or
                Path.home() / 'qoderwake-panel').resolve()
    account = pwd.getpwnam(os.environ.get('QW_GW_USER', 'qwgw'))
    manager = Manager(
        root, HERE / 'uplink-gw.py',
        os.environ.get('QW_GW_CONFIG') or root / 'config/uplink-gw.json',
        os.environ.get('QW_GW_PORT', '19840'), account)
    if args.action == 'status':
        state = manager.state()
        healthy = bool(state and manager.health(state))
        try:
            recovery_required = path_entry_exists(
                manager.journal_file,
                'gateway_journal_visibility_unknown')
        except ValueError:
            recovery_required = True
        print(json.dumps({
            'ok': True, 'managed': bool(state),
            'listening': manager.busy(), 'healthy': healthy,
            'activeMode': state['generation']['mode'] if healthy else None,
            'activeConfigHash': (
                state['generation']['configHash'] if healthy else None),
            'recoveryRequired': recovery_required
        }))
        return
    with manager.locked():
        if args.action != 'preflight':
            manager.recover()
        if args.action == 'stop':
            manager.stop(manager.state())
            result = {'ok': True}
        elif args.action == 'rollback':
            result = manager.rollback()
        else:
            candidate_path = Path(
                os.environ.get('QW_GW_CANDIDATE') or manager.config)
            if candidate_path.is_symlink():
                raise ValueError('symlink_rejected')
            candidate = json.loads(read_regular(candidate_path))
            if args.action == 'preflight':
                result = manager.preflight_candidate(candidate)
            else:
                result = manager.apply(candidate)
        if args.action != 'preflight':
            manager.gc()
        print(json.dumps(result))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        text = str(error)
        code = (text if isinstance(error, ValueError) and
                text.replace('_', '').isalnum()
                else 'gateway_manager_failed')
        print(json.dumps({'ok': False, 'error': code}))
        sys.exit(1)
