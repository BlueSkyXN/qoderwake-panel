"""Revision-checked Provider settings transactions and one-use probe plans."""
import copy
import fcntl
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import secrets
import stat
import tempfile
import threading
import time
from urllib.parse import urlsplit

EMPTY_SETTINGS = b'{}'
MAX_SETTINGS_BYTES = 4 * 1024 * 1024
REVISION_RE = re.compile(r'[a-f0-9]{64}')
_PATH_LOCKS = {}
_PATH_LOCKS_GUARD = threading.Lock()


def _path_lock(path):
    key = str(Path(path).resolve(strict=False))
    with _PATH_LOCKS_GUARD:
        return _PATH_LOCKS.setdefault(key, threading.Lock())


class SettingsConflict(Exception):
    pass


class ProviderProbePlans:
    def __init__(self):
        self.lock = threading.Lock()
        self.plans = {}

    @staticmethod
    def _digest(*values):
        value = '\0'.join(values).encode()
        return hashlib.sha256(value).hexdigest()

    def create_saved(self, principal, name, revision, url):
        return self._create(principal, 'saved', name, revision, url, '')

    def create_adhoc(self, principal, url, api_key):
        return self._create(principal, 'adhoc', '', '', url, self._digest(api_key))

    def _create(self, principal, mode, name, revision, url, secret_digest):
        parsed = urlsplit(url)
        port = parsed.port or (443 if parsed.scheme == 'https' else 80)
        path = (parsed.path.rstrip('/') or '') + '/models'
        target = '%s://%s%s%s' % (
            parsed.scheme, parsed.hostname,
            '' if port == (443 if parsed.scheme == 'https' else 80) else ':' + str(port), path)
        plan_id = secrets.token_urlsafe(32)
        plan = {'id': plan_id, 'principal': principal, 'mode': mode, 'name': name,
                'revision': revision, 'url': url, 'secretDigest': secret_digest,
                'target': target, 'expires': time.time() + 300}
        with self.lock:
            now = time.time()
            self.plans = {key:value for key,value in self.plans.items() if value['expires'] > now}
            self.plans[plan_id] = plan
        return {key: plan[key] for key in ('id', 'mode', 'name', 'target', 'expires')}

    def consume(self, plan_id, principal, mode, name='', revision='', url='', api_key=''):
        if not isinstance(plan_id, str):
            raise ValueError('invalid_provider_probe_plan')
        with self.lock:
            plan = self.plans.get(plan_id)
            if (not plan or plan['expires'] <= time.time() or
                    not hmac.compare_digest(plan['principal'], principal)):
                raise ValueError('invalid_provider_probe_plan')
            del self.plans[plan_id]
        valid = plan['mode'] == mode and hmac.compare_digest(plan['url'], url)
        if mode == 'saved':
            valid = valid and hmac.compare_digest(plan['name'], name) and hmac.compare_digest(plan['revision'], revision)
        elif mode == 'adhoc':
            valid = valid and hmac.compare_digest(plan['secretDigest'], self._digest(api_key))
        else:
            valid = False
        if not valid:
            raise ValueError('invalid_provider_probe_plan')
        return plan


def revision(raw):
    return hashlib.sha256(raw).hexdigest()


def require_revision(value):
    if not isinstance(value, str) or REVISION_RE.fullmatch(value) is None:
        raise ValueError('provider_revision_required')
    return value


def provider_models(name, value):
    if not isinstance(value, dict):
        return []
    rows = value.get('models')
    if not isinstance(rows, list):
        return []
    result = []
    for row in rows:
        if isinstance(row, dict) and isinstance(row.get('model'), str) and row['model'].strip():
            result.append(name + '/' + row['model'].strip())
    return result


def provider_editability(value):
    if not isinstance(value, dict):
        return False, 'provider_schema_unsupported'
    if value.get('type') not in (None, 'openai-compatible'):
        return False, 'provider_protocol_unsupported'
    if value.get('authType') not in (None, 'bearer'):
        return False, 'provider_auth_unsupported'
    models = value.get('models')
    if not isinstance(models, list) or len(models) != 1:
        return False, 'provider_model_count_unsupported'
    row = models[0]
    if not isinstance(row, dict) or not isinstance(row.get('model'), str) or not row['model'].strip():
        return False, 'provider_schema_unsupported'
    return True, None


def _atomic_bytes(path, raw, mode=0o600, before_replace=None):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.is_symlink():
        raise ValueError('symlink_not_allowed')
    fd, temporary = tempfile.mkstemp(prefix='.' + path.name + '-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(raw)
            os.fchmod(stream.fileno(), mode)
            stream.flush()
            os.fsync(stream.fileno())
        if before_replace is not None:
            before_replace()
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


class ProviderSettings:
    def __init__(self, path, recovery):
        self.path = Path(path)
        self.recovery = Path(recovery)
        self.thread_lock = _path_lock(self.path)
        self.lock_path = self.path.with_name('.' + self.path.name + '.panel.lock')

    def read(self):
        try:
            fd = os.open(self.path, os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0))
        except FileNotFoundError:
            raw = EMPTY_SETTINGS
        except OSError:
            raise ValueError('provider_settings_unavailable')
        else:
            try:
                info = os.fstat(fd)
                if not stat.S_ISREG(info.st_mode):
                    raise ValueError('provider_settings_unavailable')
                if info.st_size > MAX_SETTINGS_BYTES:
                    raise ValueError('provider_settings_too_large')
                with os.fdopen(fd, 'rb') as stream:
                    fd = -1
                    raw = stream.read(MAX_SETTINGS_BYTES + 1)
                if len(raw) > MAX_SETTINGS_BYTES:
                    raise ValueError('provider_settings_too_large')
            finally:
                if fd >= 0:
                    os.close(fd)
        try:
            data = json.loads(raw)
        except (ValueError, UnicodeDecodeError):
            raise ValueError('provider_settings_invalid')
        if not isinstance(data, dict):
            raise ValueError('provider_settings_invalid')
        providers = data.get('providers', {})
        if providers is None:
            providers = data['providers'] = {}
        if not isinstance(providers, dict):
            raise ValueError('provider_settings_invalid')
        return {'raw': raw, 'data': data, 'revision': revision(raw)}

    def summaries(self):
        snapshot = self.read()
        rows = []
        for name, value in snapshot['data'].get('providers', {}).items():
            if not isinstance(name, str):
                continue
            editable, reason = provider_editability(value)
            item = value if isinstance(value, dict) else {}
            models = item.get('models') if isinstance(item.get('models'), list) else []
            rows.append({'name': name, 'baseUrl': item.get('baseUrl'), 'type': item.get('type'),
                         'authType': item.get('authType'),
                         'models': [row.get('model') for row in models if isinstance(row, dict)],
                         'modelKeys': provider_models(name, item),
                         'displayNames': [row.get('displayName') for row in models if isinstance(row, dict)],
                         'keyConfigured': bool(item.get('apiKey')), 'panelManaged': editable,
                         'readOnlyReason': reason})
        return rows, snapshot['revision']

    def legacy_backups(self):
        rows = []
        if self.path.parent.is_dir():
            for candidate in self.path.parent.glob('settings.panel-bak-*.json'):
                try:
                    if not candidate.is_symlink() and candidate.is_file():
                        rows.append(candidate.stat())
                except OSError:
                    continue
        return {'count': len(rows), 'bytes': sum(row.st_size for row in rows),
                'oldest': min((row.st_mtime for row in rows), default=None)}

    def _locked(self):
        self.path.parent.mkdir(parents=True, exist_ok=True)
        if self.lock_path.is_symlink():
            raise ValueError('symlink_not_allowed')
        fd = os.open(self.lock_path, os.O_CREAT | os.O_RDWR | getattr(os, 'O_NOFOLLOW', 0), 0o600)
        fcntl.flock(fd, fcntl.LOCK_EX)
        return fd

    def update(self, expected_revision, transform):
        expected_revision = require_revision(expected_revision)
        with self.thread_lock:
            lock_fd = self._locked()
            try:
                before = self.read()
                if not hmac.compare_digest(before['revision'], expected_revision):
                    raise SettingsConflict('provider_settings_changed')
                updated = transform(copy.deepcopy(before['data']))
                if not isinstance(updated, dict):
                    raise ValueError('provider_settings_invalid')
                encoded = json.dumps(updated, ensure_ascii=False, indent=2).encode()
                if len(encoded) > MAX_SETTINGS_BYTES:
                    raise ValueError('provider_settings_too_large')
                def check_revision():
                    current = self.read()
                    if not hmac.compare_digest(current['revision'], expected_revision):
                        raise SettingsConflict('provider_settings_changed')
                check_revision()
                if self.path.exists():
                    _atomic_bytes(self.recovery, before['raw'])
                # Non-cooperating writers must still be stopped; rename is not a filesystem CAS.
                _atomic_bytes(self.path, encoded, before_replace=check_revision)
                return {'revision': revision(encoded), 'data': updated}
            finally:
                os.close(lock_fd)
