"""本地人员账号；与 daemon 身份和程序 caller key 分开保存。"""
import hashlib
import hmac
import json
import re
import secrets
import sqlite3
import time


class AccessError(Exception):
    def __init__(self, status, code):
        self.status, self.code = status, code
        super().__init__(code)


ROLES = ('admin', 'maintainer', 'observer')
USER_COLUMNS = 'id,username,display_name,role,wakers,enabled,auth_version,created,updated'


def password_hash(value):
    if not callable(getattr(hashlib, 'scrypt', None)):
        raise ValueError('password_kdf_unavailable')
    if not isinstance(value, str) or not 10 <= len(value) <= 1024:
        raise ValueError('password_length_invalid')
    salt = secrets.token_bytes(16)
    result = hashlib.scrypt(value.encode(), salt=salt, n=32768, r=8, p=1, maxmem=64*1024*1024)
    return 'scrypt$32768$8$1$' + salt.hex() + '$' + result.hex()


def password_matches(value, encoded):
    if not isinstance(value, str) or len(value) > 1024:
        return False
    try:
        kind, n, r, p, salt, expected = encoded.split('$')
        if (kind, n, r, p) != ('scrypt', '32768', '8', '1'):
            return False
        actual = hashlib.scrypt(value.encode(), salt=bytes.fromhex(salt), n=32768, r=8, p=1,
                                maxmem=64*1024*1024).hex()
        return hmac.compare_digest(actual, expected)
    except (ValueError, TypeError, AttributeError):
        return False


def user_fields(username, name, role, wakers):
    if not isinstance(username, str) or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,63}', username):
        raise ValueError('invalid_username')
    if not isinstance(name, str) or not 1 <= len(name.strip()) <= 80:
        raise ValueError('invalid_display_name')
    if role not in ROLES:
        raise ValueError('invalid_user_role')
    if (not isinstance(wakers, list) or len(wakers) > 100 or
            any(not isinstance(w, str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,100}', w) for w in wakers)):
        raise ValueError('invalid_waker_scope')
    return username.lower(), name.strip(), role, json.dumps(sorted(set(wakers)) if role != 'admin' else [])


def public_user(row):
    return dict(id=row[0], username=row[1], displayName=row[2], role=row[3],
                wakers=json.loads(row[4]), enabled=bool(row[5]), revision=row[6],
                createdAt=row[7], updatedAt=row[8])


class AccountsMixin:
    def init_accounts(self, con):
        con.executescript('''
            CREATE TABLE IF NOT EXISTS users (
                id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE COLLATE NOCASE,
                display_name TEXT NOT NULL, password_hash TEXT NOT NULL, role TEXT NOT NULL,
                wakers TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1,
                auth_version INTEGER NOT NULL DEFAULT 1, created REAL NOT NULL, updated REAL NOT NULL);
            CREATE TABLE IF NOT EXISTS auth_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
        ''')
        if 'user_id' not in {r[1] for r in con.execute('PRAGMA table_info(sessions)')}:
            con.execute('ALTER TABLE sessions ADD COLUMN user_id TEXT')
        for name in ('resource_waker', 'before_revision', 'after_revision'):
            if name not in {r[1] for r in con.execute('PRAGMA table_info(audit)')}:
                con.execute('ALTER TABLE audit ADD COLUMN ' + name + " TEXT NOT NULL DEFAULT ''")
        con.execute('CREATE INDEX IF NOT EXISTS session_user ON sessions(user_id)')
        con.execute('CREATE INDEX IF NOT EXISTS audit_waker ON audit(resource_waker)')

    def accounts_enabled(self):
        with self.db() as con:
            return con.execute("SELECT value FROM auth_settings WHERE key='mode'").fetchone() == ('accounts',)

    def bootstrap_admin(self, username, name, password):
        fields = user_fields(username, name, 'admin', [])
        encoded = password_hash(password)
        uid, now = secrets.token_hex(16), time.time()
        with self.db() as con:
            con.execute('BEGIN IMMEDIATE')
            if con.execute('SELECT 1 FROM users LIMIT 1').fetchone():
                raise AccessError(409, 'admin_already_initialized')
            con.execute('INSERT INTO users VALUES(?,?,?,?,?,?,1,1,?,?)',
                        (uid, fields[0], fields[1], encoded, fields[2], fields[3], now, now))
            con.execute("INSERT OR REPLACE INTO auth_settings VALUES('mode','accounts')")
            con.execute('DELETE FROM sessions WHERE user_id IS NULL')
        return self.user(uid)

    def user(self, uid):
        with self.db() as con:
            row = con.execute('SELECT '+USER_COLUMNS+' FROM users WHERE id=?', (uid,)).fetchone()
        if not row:
            raise AccessError(404, 'user_not_found')
        return public_user(row)

    def users(self):
        with self.db() as con:
            return [public_user(row) for row in con.execute('SELECT '+USER_COLUMNS+' FROM users ORDER BY username')]

    def create_user(self, username, name, password, role, wakers):
        if not self.accounts_enabled():
            raise AccessError(409, 'accounts_not_initialized')
        fields = user_fields(username, name, role, wakers)
        encoded = password_hash(password)
        uid, now = secrets.token_hex(16), time.time()
        try:
            with self.db() as con:
                con.execute('INSERT INTO users VALUES(?,?,?,?,?,?,1,1,?,?)',
                            (uid, fields[0], fields[1], encoded, fields[2], fields[3], now, now))
        except sqlite3.IntegrityError:
            raise AccessError(409, 'username_exists') from None
        return self.user(uid)

    def update_user(self, uid, name, role, wakers, enabled, revision):
        fields = user_fields('placeholder', name, role, wakers)
        if type(enabled) is not bool or type(revision) is not int:
            raise ValueError('invalid_user_update')
        with self.db() as con:
            con.execute('BEGIN IMMEDIATE')
            row = con.execute('SELECT role,enabled,auth_version FROM users WHERE id=?', (uid,)).fetchone()
            if not row:
                raise AccessError(404, 'user_not_found')
            if row[2] != revision:
                raise AccessError(409, 'version_conflict')
            if row[0] == 'admin' and row[1] and (role != 'admin' or not enabled):
                if con.execute("SELECT COUNT(*) FROM users WHERE role='admin' AND enabled=1").fetchone()[0] <= 1:
                    raise AccessError(409, 'last_admin_required')
            con.execute('UPDATE users SET display_name=?,role=?,wakers=?,enabled=?,auth_version=auth_version+1,updated=? WHERE id=?',
                        (fields[1], fields[2], fields[3], int(enabled), time.time(), uid))
            con.execute('DELETE FROM sessions WHERE user_id=?', (uid,))
        return self.user(uid)

    def reset_password(self, uid, password, revision=None):
        encoded = password_hash(password)
        with self.db() as con:
            con.execute('BEGIN IMMEDIATE')
            row = con.execute('SELECT auth_version FROM users WHERE id=?', (uid,)).fetchone()
            if not row:
                raise AccessError(404, 'user_not_found')
            if revision is not None and (type(revision) is not int or row[0] != revision):
                raise AccessError(409, 'version_conflict')
            con.execute('UPDATE users SET password_hash=?,auth_version=auth_version+1,updated=? WHERE id=?',
                        (encoded, time.time(), uid))
            con.execute('DELETE FROM sessions WHERE user_id=?', (uid,))

    def change_password(self, identity, current, password):
        identity = self.refresh_identity(identity)
        with self.db() as con:
            row = con.execute('SELECT password_hash FROM users WHERE id=?', (identity.get('user_id'),)).fetchone()
        if not row or not password_matches(current, row[0]):
            raise AccessError(403, 'current_password_invalid')
        self.reset_password(identity['user_id'], password, identity['auth_version'])

    def login_user(self, username, password):
        from panel_security import digest, IDLE_TTL, MAX_TTL
        if not self.accounts_enabled():
            raise AccessError(401, 'invalid_credentials')
        with self.db() as con:
            row = con.execute('SELECT id,password_hash,enabled,auth_version FROM users WHERE username=?',
                              (username if isinstance(username, str) else '',)).fetchone()
        # 未知账号仍执行同样的 KDF；响应不区分用户名、密码或停用状态。
        encoded = row[1] if row else 'scrypt$32768$8$1$' + '00'*16 + '$' + '00'*64
        valid = password_matches(password, encoded)
        if not row or not row[2] or not valid:
            raise AccessError(401, 'invalid_credentials')
        raw, csrf, now = secrets.token_urlsafe(32), secrets.token_urlsafe(32), time.time()
        with self.db() as con:
            con.execute('BEGIN IMMEDIATE')
            current = con.execute('SELECT enabled,auth_version,role FROM users WHERE id=?', (row[0],)).fetchone()
            if not current or not current[0] or current[1] != row[3]:
                raise AccessError(401, 'invalid_credentials')
            con.execute('DELETE FROM sessions WHERE seen < ? OR created < ?', (now-IDLE_TTL, now-MAX_TTL))
            con.execute('INSERT INTO sessions(id,role,credential,csrf,created,seen,user_id) VALUES(?,?,?,?,?,?,?)',
                        (digest(raw), current[2], str(row[3]), csrf, now, now, row[0]))
        identity = self.user_identity(row[0], row[3])
        identity.update(csrf=csrf, sid=digest(raw), source='cookie')
        return raw, identity

    def user_identity(self, uid, revision):
        user = self.user(uid)
        if not user['enabled'] or user['revision'] != revision or not self.accounts_enabled():
            raise AccessError(401, 'session_expired')
        return {'role': user['role'], 'principal': 'user:'+uid, 'user_id': uid,
                'auth_version': revision, 'username': user['username'], 'displayName': user['displayName'],
                'wakers': user['wakers']}

    def refresh_identity(self, identity):
        if identity.get('user_id'):
            return dict(identity, **self.user_identity(identity['user_id'], identity['auth_version']))
        return identity
