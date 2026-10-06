"""Local panel authentication; no daemon credentials are exposed to callers."""
import hashlib
import hmac
import os
import json
import re
import secrets
import sqlite3
import threading
import time
from contextlib import contextmanager
from http.cookies import SimpleCookie
from pathlib import Path

COOKIE = 'qwp_session'
IDLE_TTL = 12 * 3600
MAX_TTL = 7 * 86400
SEEN_INTERVAL = 60


class AccessError(Exception):
    def __init__(self, status, code):
        self.status, self.code = status, code
        super().__init__(code)


class AdmissionLease:
    def __init__(self, controller, token):
        self.controller = controller
        self.token = token
        self.released = False

    def release(self):
        if self.released:
            raise RuntimeError('admission_lease_already_released')
        self.controller.release(self.token)
        self.released = True


class AdmissionController:
    MODES = {'normal', 'draining', 'maintenance', 'restarting'}

    def __init__(self, call_limit=2):
        self.condition = threading.Condition(threading.Lock())
        self.call_limit = call_limit
        self.mode = 'normal'
        self.epoch = 0
        self.owner = ''
        self.reason = ''
        self.since = time.time()
        self.expires = None
        self.maintenance_ttl = 300
        self.leases = {}

    def _expire(self, now=None):
        now = now or time.time()
        if self.mode == 'maintenance' and self.expires is not None and self.expires <= now:
            self.mode = 'normal'
            self.epoch += 1
            self.owner = ''
            self.reason = 'maintenance_ttl_expired'
            self.since = now
            self.expires = None
            self.condition.notify_all()

    def status(self):
        with self.condition:
            self._expire()
            mutations = len(self.leases)
            calls = sum(1 for lease in self.leases.values() if lease['call'])
            return {'mode': self.mode, 'epoch': self.epoch, 'reason': self.reason,
                    'since': self.since, 'expires': self.expires,
                    'activeMutations': mutations, 'activeCalls': calls}

    def acquire(self, principal, call=False):
        with self.condition:
            self._expire()
            if self.mode != 'normal':
                raise AccessError(503, 'maintenance_mode')
            calls = sum(1 for lease in self.leases.values() if lease['call'])
            if call and calls >= self.call_limit:
                raise AccessError(429, 'concurrency_limited')
            token = secrets.token_urlsafe(24)
            self.leases[token] = {'principal': principal, 'call': bool(call),
                                  'epoch': self.epoch, 'created': time.time()}
            return AdmissionLease(self, token)

    def release(self, token):
        with self.condition:
            if token not in self.leases:
                raise RuntimeError('invalid_admission_lease')
            del self.leases[token]
            self.condition.notify_all()

    def begin_drain(self, principal, reason, ttl=300, lease=None):
        if not isinstance(reason, str) or not reason.strip() or not 30 <= ttl <= 3600:
            raise ValueError('invalid_maintenance_request')
        with self.condition:
            self._expire()
            if self.mode != 'normal':
                raise AccessError(503, 'maintenance_mode')
            if lease is not None:
                if lease.controller is not self or lease.released or lease.token not in self.leases:
                    raise RuntimeError('invalid_admission_lease')
                del self.leases[lease.token]
                lease.released = True
            now = time.time()
            self.mode = 'draining'
            self.epoch += 1
            self.owner = principal
            self.reason = reason.strip()[:200]
            self.since = now
            self.maintenance_ttl = ttl
            self.expires = None
            self.condition.notify_all()
            return self.epoch

    def wait_clear(self, epoch, timeout=30):
        deadline = time.monotonic() + timeout
        with self.condition:
            while True:
                self._expire()
                if self.epoch != epoch or self.mode != 'draining':
                    raise AccessError(409, 'maintenance_state_changed')
                if not self.leases:
                    return
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise AccessError(503, 'maintenance_drain_timeout')
                self.condition.wait(min(remaining, 0.25))

    def transition(self, epoch, mode):
        if mode not in ('maintenance', 'restarting'):
            raise ValueError('invalid_maintenance_mode')
        with self.condition:
            self._expire()
            if self.epoch != epoch or self.mode != 'draining' or self.leases:
                raise AccessError(409, 'maintenance_state_changed')
            self.mode = mode
            self.since = time.time()
            self.expires = self.since + self.maintenance_ttl if mode == 'maintenance' else None
            self.condition.notify_all()

    def exit(self):
        with self.condition:
            self._expire()
            if self.mode == 'normal':
                return self.epoch
            if self.mode != 'maintenance':
                raise AccessError(409, 'maintenance_exit_rejected')
            self.mode = 'normal'
            self.epoch += 1
            self.owner = ''
            self.reason = ''
            self.since = time.time()
            self.expires = None
            self.condition.notify_all()
            return self.epoch

    def complete(self, epoch):
        with self.condition:
            if self.epoch == epoch and self.mode in ('draining', 'restarting'):
                self.mode = 'normal'
                self.epoch += 1
                self.owner = ''
                self.reason = ''
                self.since = time.time()
                self.expires = None
                self.condition.notify_all()


def private_token(path):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.is_symlink():
        raise RuntimeError('Credential file must not be a symlink')
    try:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        os.chmod(path, 0o600)
    else:
        with os.fdopen(fd, 'w') as f:
            f.write(secrets.token_hex(32))
    value = path.read_text().strip()
    if len(value) < 24 or len(value) > 256:
        raise RuntimeError('Invalid local credential file')
    return value


def digest(value):
    return hashlib.sha256(value.encode()).hexdigest()


class Security:
    def __init__(self, root):
        self.root = Path(root)
        self.root.mkdir(parents=True, exist_ok=True)
        self.admin = private_token(self.root / 'admin-token.txt')
        self.viewer = private_token(self.root / 'viewer-token.txt')
        self.path = self.root / 'access.db'
        self.lock = threading.Lock()
        self.active = 0
        fd = os.open(self.path, os.O_WRONLY | os.O_CREAT | getattr(os, 'O_NOFOLLOW', 0), 0o600)
        os.close(fd)
        os.chmod(self.path, 0o600)
        with self.db() as con:
            con.executescript('''
                CREATE TABLE IF NOT EXISTS sessions (
                    id TEXT PRIMARY KEY, role TEXT NOT NULL, credential TEXT NOT NULL,
                    csrf TEXT NOT NULL, created REAL NOT NULL, seen REAL NOT NULL);
                CREATE TABLE IF NOT EXISTS callers (
                    id TEXT PRIMARY KEY, name TEXT NOT NULL, token_hash TEXT UNIQUE NOT NULL,
                    wakers TEXT NOT NULL, quota INTEGER NOT NULL, used INTEGER NOT NULL DEFAULT 0,
                    expires REAL NOT NULL, revoked INTEGER NOT NULL DEFAULT 0);
                CREATE TABLE IF NOT EXISTS rates (bucket TEXT NOT NULL, ts REAL NOT NULL);
                CREATE INDEX IF NOT EXISTS rate_bucket ON rates(bucket, ts);
                CREATE TABLE IF NOT EXISTS audit (
                    ts TEXT NOT NULL, ip TEXT NOT NULL, principal TEXT NOT NULL,
                    role TEXT NOT NULL, action TEXT NOT NULL, target TEXT NOT NULL,
                    status INTEGER NOT NULL, ok INTEGER NOT NULL);
                CREATE TABLE IF NOT EXISTS deletion_previews (
                    id_hash TEXT PRIMARY KEY, principal TEXT NOT NULL, kind TEXT NOT NULL,
                    target TEXT NOT NULL, revision TEXT NOT NULL, impact_digest TEXT NOT NULL,
                    warnings TEXT NOT NULL, expires REAL NOT NULL, consumed REAL);
                CREATE TABLE IF NOT EXISTS deletion_operations (
                    principal TEXT NOT NULL, idempotency_key TEXT NOT NULL,
                    request_digest TEXT NOT NULL, kind TEXT NOT NULL, target TEXT NOT NULL,
                    status TEXT NOT NULL, result TEXT NOT NULL, created REAL NOT NULL,
                    updated REAL NOT NULL, PRIMARY KEY(principal, idempotency_key));
            ''')

    @contextmanager
    def db(self):
        con = sqlite3.connect(self.path, timeout=10)
        try:
            with con:
                yield con
        finally:
            con.close()

    def role_for(self, value):
        if not isinstance(value, str) or not value or len(value) > 256:
            return None
        hashed = digest(value)
        if hmac.compare_digest(hashed, digest(self.admin)):
            return 'admin'
        if hmac.compare_digest(hashed, digest(self.viewer)):
            return 'viewer'
        return None

    def credential(self, role):
        return digest(self.admin if role == 'admin' else self.viewer)

    def login(self, value):
        role = self.role_for(value)
        if not role:
            raise AccessError(401, 'invalid_credentials')
        session, csrf, now = secrets.token_urlsafe(32), secrets.token_urlsafe(32), time.time()
        with self.db() as con:
            con.execute('DELETE FROM sessions WHERE seen < ? OR created < ?', (now-IDLE_TTL, now-MAX_TTL))
            con.execute('INSERT INTO sessions VALUES(?,?,?,?,?,?)',
                        (digest(session), role, self.credential(role), csrf, now, now))
        return session, {'role': role, 'csrf': csrf}

    def authenticate(self, headers):
        auth = headers.get('Authorization', '')
        if auth:
            if not auth.lower().startswith('bearer '):
                raise AccessError(401, 'invalid_credentials')
            value = auth[7:].strip()
            role = self.role_for(value)
            if not role:
                with self.db() as con:
                    row = con.execute('SELECT id,wakers,expires,revoked FROM callers WHERE token_hash=?', (digest(value),)).fetchone()
                if not row or row[3] or row[2] <= time.time():
                    raise AccessError(401, 'invalid_credentials')
                return {'role': 'caller', 'principal': 'caller:' + row[0], 'caller_id': row[0],
                        'wakers': json.loads(row[1]), 'source': 'bearer', 'csrf': ''}
            return {'role': role, 'principal': 'token:' + self.credential(role)[:12], 'source': 'bearer', 'csrf': ''}
        cookies = SimpleCookie()
        try:
            cookies.load(headers.get('Cookie', ''))
        except Exception:
            raise AccessError(401, 'authentication_required')
        raw = cookies[COOKIE].value if COOKIE in cookies else ''
        if not raw or len(raw) > 256:
            raise AccessError(401, 'authentication_required')
        sid, now = digest(raw), time.time()
        with self.db() as con:
            row = con.execute('SELECT role,credential,csrf,created,seen FROM sessions WHERE id=?', (sid,)).fetchone()
            if not row or now-row[3] > MAX_TTL or now-row[4] > IDLE_TTL or not hmac.compare_digest(row[1], self.credential(row[0])):
                con.execute('DELETE FROM sessions WHERE id=?', (sid,))
                raise AccessError(401, 'session_expired')
            if now - row[4] >= SEEN_INTERVAL:
                con.execute('UPDATE sessions SET seen=? WHERE id=? AND seen<=?', (now, sid, now - SEEN_INTERVAL))
        return {'role': row[0], 'principal': 'session:' + sid[:12], 'source': 'cookie', 'csrf': row[2], 'sid': sid}

    def create_caller(self, name, wakers, quota, days):
        if not isinstance(name, str) or not 1 <= len(name.strip()) <= 80:
            raise ValueError('invalid_caller_name')
        if not isinstance(wakers, list) or not 1 <= len(wakers) <= 100 or any(
                not isinstance(w, str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,100}', w) for w in wakers):
            raise ValueError('invalid_waker_scope')
        if type(quota) is not int or not 1 <= quota <= 100000 or type(days) is not int or not 1 <= days <= 365:
            raise ValueError('invalid_caller_quota')
        cid, token = secrets.token_hex(12), 'qwc_' + secrets.token_urlsafe(32)
        with self.db() as con:
            con.execute('INSERT INTO callers(id,name,token_hash,wakers,quota,expires) VALUES(?,?,?,?,?,?)',
                        (cid, name.strip(), digest(token), json.dumps(sorted(set(wakers))), quota, time.time()+days*86400))
        return {'id': cid, 'token': token}

    def callers(self):
        with self.db() as con:
            rows = con.execute('SELECT id,name,wakers,quota,used,expires,revoked FROM callers ORDER BY rowid DESC').fetchall()
        return [dict(id=r[0], name=r[1], wakers=json.loads(r[2]), quota=r[3], used=r[4], expires=r[5], revoked=bool(r[6])) for r in rows]

    def revoke_caller(self, cid):
        with self.db() as con:
            if not con.execute('UPDATE callers SET revoked=1 WHERE id=?', (cid,)).rowcount:
                raise ValueError('caller_not_found')

    def consume_caller(self, identity, waker):
        # Count admitted attempts, including timeouts: the model may already have incurred cost.
        with self.db() as con:
            con.execute('BEGIN IMMEDIATE')
            row = con.execute('SELECT wakers,quota,used,expires,revoked FROM callers WHERE id=?', (identity['caller_id'],)).fetchone()
            if not row or row[4] or row[3] <= time.time():
                raise AccessError(401, 'invalid_credentials')
            if waker not in json.loads(row[0]):
                raise AccessError(403, 'waker_scope_rejected')
            if row[2] >= row[1]:
                raise AccessError(429, 'caller_quota_exhausted')
            con.execute('UPDATE callers SET used=used+1 WHERE id=?', (identity['caller_id'],))

    def logout(self, identity):
        with self.db() as con:
            con.execute('DELETE FROM sessions WHERE id=?', (identity.get('sid', ''),))

    def limit(self, bucket, limit=20, window=300):
        now = time.time()
        with self.lock, self.db() as con:
            con.execute('BEGIN IMMEDIATE')
            con.execute('DELETE FROM rates WHERE ts < ?', (now-3600,))
            n = con.execute('SELECT COUNT(*) FROM rates WHERE bucket=? AND ts>?', (bucket, now-window)).fetchone()[0]
            if n >= limit:
                return False
            con.execute('INSERT INTO rates VALUES(?,?)', (bucket, now))
        return True

    def acquire_call(self):
        with self.lock:
            if self.active >= 2:
                return False
            self.active += 1
        return True

    def release_call(self):
        with self.lock:
            self.active = max(0, self.active-1)

    def create_deletion_preview(self, principal, preview, ttl=300):
        raw = secrets.token_urlsafe(32)
        now = time.time()
        warnings = preview.get('warningsRequired', [])
        if not isinstance(warnings, list) or any(not isinstance(value, str) for value in warnings):
            raise ValueError('invalid_deletion_preview')
        with self.db() as con:
            con.execute('DELETE FROM deletion_previews WHERE expires < ? OR consumed IS NOT NULL', (now,))
            con.execute('''INSERT INTO deletion_previews
                (id_hash,principal,kind,target,revision,impact_digest,warnings,expires,consumed)
                VALUES(?,?,?,?,?,?,?,?,NULL)''', (
                    digest(raw), principal, preview['kind'], preview['target'],
                    preview.get('revision') or '', preview['impactDigest'],
                    json.dumps(sorted(set(warnings))), now + ttl))
        return raw, now + ttl

    def deletion_preview(self, raw, principal, kind, target):
        if not isinstance(raw, str) or not 20 <= len(raw) <= 256:
            raise AccessError(409, 'invalid_deletion_preview')
        with self.db() as con:
            row = con.execute('''SELECT kind,target,revision,impact_digest,warnings,expires,consumed
                FROM deletion_previews WHERE id_hash=? AND principal=?''',
                (digest(raw), principal)).fetchone()
        if not row or row[5] <= time.time() or row[6] is not None or row[0] != kind or row[1] != target:
            raise AccessError(409, 'invalid_deletion_preview')
        return {'kind': row[0], 'target': row[1], 'revision': row[2],
                'impactDigest': row[3], 'warningsRequired': json.loads(row[4]),
                'expires': row[5]}

    @staticmethod
    def deletion_request_digest(kind, target, impact_digest, warnings):
        value = json.dumps({
            'kind': kind, 'target': target, 'impactDigest': impact_digest,
            'warnings': sorted(set(warnings))
        }, sort_keys=True, separators=(',', ':'))
        return digest(value)

    def unresolved_deletion(self, kind, target):
        with self.db() as con:
            row = con.execute('''SELECT status,updated FROM deletion_operations
                WHERE kind=? AND target=? AND status IN ('pending','unknown')
                ORDER BY created DESC LIMIT 1''', (kind, target)).fetchone()
        return {'status': row[0], 'updated': row[1]} if row else None

    def has_unresolved_deletions(self):
        with self.db() as con:
            return con.execute("SELECT 1 FROM deletion_operations WHERE status IN ('pending','unknown') LIMIT 1").fetchone() is not None

    def deletion_operation(self, principal, idempotency_key):
        if not isinstance(idempotency_key, str) or not re.fullmatch(r'[A-Za-z0-9_.:-]{8,128}', idempotency_key):
            raise ValueError('invalid_idempotency_key')
        with self.db() as con:
            row = con.execute('''SELECT kind,target,status,result,created,updated
                FROM deletion_operations WHERE principal=? AND idempotency_key=?''',
                (principal, idempotency_key)).fetchone()
        if not row:
            raise AccessError(404, 'deletion_operation_not_found')
        return {'kind': row[0], 'target': row[1], 'status': row[2],
                'result': json.loads(row[3]), 'created': row[4], 'updated': row[5]}

    def replay_deletion(self, principal, idempotency_key, kind, target,
                        impact_digest, acknowledged):
        if not isinstance(idempotency_key, str) or not re.fullmatch(r'[A-Za-z0-9_.:-]{8,128}', idempotency_key):
            raise ValueError('invalid_idempotency_key')
        request_digest = self.deletion_request_digest(
            kind, target, impact_digest, acknowledged)
        with self.db() as con:
            operation = con.execute('''SELECT request_digest,status,result FROM deletion_operations
                WHERE principal=? AND idempotency_key=?''',
                (principal, idempotency_key)).fetchone()
        if not operation:
            return None
        if not hmac.compare_digest(operation[0], request_digest):
            raise AccessError(409, 'idempotency_key_conflict')
        return {'replay': True, 'status': operation[1],
                'result': json.loads(operation[2])}

    def begin_deletion(self, raw, principal, kind, target, impact_digest,
                       acknowledged, idempotency_key):
        replay = self.replay_deletion(
            principal, idempotency_key, kind, target, impact_digest, acknowledged)
        if replay:
            return replay
        preview = self.deletion_preview(raw, principal, kind, target)
        if not hmac.compare_digest(preview['impactDigest'], impact_digest):
            raise AccessError(409, 'stale_deletion_preview')
        required = sorted(set(preview['warningsRequired']))
        if sorted(set(acknowledged)) != required:
            raise AccessError(409, 'deletion_warnings_unconfirmed')
        request_digest = self.deletion_request_digest(kind, target, impact_digest, required)
        now = time.time()
        with self.db() as con:
            con.execute('BEGIN IMMEDIATE')
            operation = con.execute('''SELECT request_digest,status,result FROM deletion_operations
                WHERE principal=? AND idempotency_key=?''', (principal, idempotency_key)).fetchone()
            if operation:
                if not hmac.compare_digest(operation[0], request_digest):
                    raise AccessError(409, 'idempotency_key_conflict')
                return {'replay': True, 'status': operation[1],
                        'result': json.loads(operation[2])}
            unresolved = con.execute('''SELECT idempotency_key,status FROM deletion_operations
                WHERE kind=? AND target=? AND status IN ('pending','unknown')
                ORDER BY created DESC LIMIT 1''', (kind, target)).fetchone()
            if unresolved:
                raise AccessError(409, 'deletion_operation_unresolved')
            consumed = con.execute('''UPDATE deletion_previews SET consumed=?
                WHERE id_hash=? AND principal=? AND consumed IS NULL AND expires>?''',
                (now, digest(raw), principal, now)).rowcount
            if not consumed:
                raise AccessError(409, 'invalid_deletion_preview')
            pending = {'ok': False, 'status': 'pending'}
            con.execute('''INSERT INTO deletion_operations
                (principal,idempotency_key,request_digest,kind,target,status,result,created,updated)
                VALUES(?,?,?,?,?,?,?,?,?)''', (
                    principal, idempotency_key, request_digest, kind, target,
                    'pending', json.dumps(pending, sort_keys=True), now, now))
        return {'replay': False, 'status': 'pending', 'result': pending}

    def resolve_deletion(self, kind, target, status, result):
        if status not in ('succeeded', 'failed'):
            raise ValueError('invalid_deletion_status')
        public = result if isinstance(result, dict) else {'ok': status == 'succeeded'}
        with self.db() as con:
            changed = con.execute('''UPDATE deletion_operations SET status=?,result=?,updated=?
                WHERE rowid=(SELECT rowid FROM deletion_operations
                    WHERE kind=? AND target=? AND status IN ('pending','unknown')
                    ORDER BY created DESC LIMIT 1)''',
                (status, json.dumps(public, ensure_ascii=False, sort_keys=True),
                 time.time(), kind, target)).rowcount
        if not changed:
            raise AccessError(404, 'deletion_operation_not_found')
        return public

    def finish_deletion(self, principal, idempotency_key, status, result):
        if status not in ('succeeded', 'failed', 'pending', 'unknown'):
            raise ValueError('invalid_deletion_status')
        public = result if isinstance(result, dict) else {'ok': status == 'succeeded'}
        with self.db() as con:
            changed = con.execute('''UPDATE deletion_operations SET status=?,result=?,updated=?
                WHERE principal=? AND idempotency_key=? AND status='pending' ''',
                (status, json.dumps(public, ensure_ascii=False, sort_keys=True), time.time(),
                 principal, idempotency_key)).rowcount
        if not changed:
            raise ValueError('deletion_operation_not_pending')
        return public

    def audit(self, ip, identity, action, target, status, ok):
        identity = identity or {}
        with self.db() as con:
            con.execute('INSERT INTO audit VALUES(?,?,?,?,?,?,?,?)', (
                time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()), ip[:64],
                identity.get('principal', 'anonymous'), identity.get('role', 'anonymous'),
                action[:64], target[:128], status, int(ok)))
            con.execute('DELETE FROM audit WHERE rowid < (SELECT MAX(rowid)-10000 FROM audit)')

    def recent(self):
        with self.db() as con:
            rows = con.execute('SELECT ts,ip,principal,role,action,target,status,ok FROM audit ORDER BY rowid DESC LIMIT 50').fetchall()
        return [dict(zip(['ts','ip','principal','role','action','target','status','ok'], r)) for r in rows]
