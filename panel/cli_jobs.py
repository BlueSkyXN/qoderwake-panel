"""受控 CLI 操作、持续输出和幂等任务记录；不持久化参数、输入或输出正文。"""
import codecs
import contextlib
import hashlib
import hmac
import json
import os
from pathlib import Path
import pty
import re
import selectors
import shutil
import signal
import sqlite3
import subprocess
import termios
import threading
import time
import uuid

from cli_catalog import Catalog, build_argv, parse_help, redact, SECRET
from panel_security import AccessError, private_token

ACTIVE = {'queued', 'running'}
MAX_OUTPUT = 2 * 1024 * 1024
MAX_JOBS = 100
CONTEXT_KEYS = ('QODERWAKE_CONVERSATION_ID', 'QODERWAKE_CONVERSATION_KIND',
                'QODERWAKE_RUN_ID', 'QODERWAKE_RUN_TOKEN', 'QODERWAKE_PARTICIPANT_ID',
                'QODER_AGENT_ID', 'QODER_SESSION_ID', 'QODERWAKE_SESSION_ID',
                'QODERWAKE_CLOUD_CONVERSATION')


class Operations:
    def __init__(self, root, home, binary, catalog=None, audit=None, guard=None, resolve=None):
        self.root, self.home = Path(root), Path(home)
        self.binary = shutil.which(binary) or binary
        self.catalog = catalog or Catalog()
        self.audit = audit or (lambda *args: None)
        self.guard = guard or (lambda *args: contextlib.nullcontext())
        self.resolve = resolve or (lambda identity, value: value)
        self.root.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.lock = threading.RLock()
        self.jobs, self.probes = {}, {}
        self.key = private_token(self.root/'digest-key').encode()
        self.database = self.root/'operations.db'
        with self.db() as con:
            con.execute('''CREATE TABLE IF NOT EXISTS jobs (
                id TEXT PRIMARY KEY, principal TEXT NOT NULL, request_key TEXT NOT NULL,
                digest TEXT NOT NULL, operation TEXT NOT NULL, waker TEXT NOT NULL,
                status TEXT NOT NULL, created REAL NOT NULL, updated REAL NOT NULL,
                exit_code INTEGER, error TEXT NOT NULL DEFAULT '',
                UNIQUE(principal, request_key))''')
            # 已发出的写请求不重放；重启后保留任务身份供操作者核对。
            con.execute("UPDATE jobs SET status='unknown', error='panel_restarted', updated=? WHERE status IN ('running','queued')", (time.time(),))
        os.chmod(self.database, 0o600)

    @contextlib.contextmanager
    def db(self):
        con = sqlite3.connect(self.database, timeout=10)
        con.row_factory = sqlite3.Row
        try:
            with con:
                yield con
        finally:
            con.close()

    def environment(self):
        env = dict(os.environ, QODERWAKE_HOME=str(self.home), NO_COLOR='1', TERM='dumb', COLUMNS='180')
        # 不能继承启动 Panel 的 agent 会话并把操作路由进其他 Conversation。
        for name in list(env):
            if name in CONTEXT_KEYS or name.startswith(('QODER_SESSION_', 'QODERWAKE_CONVERSATION_', 'QODERWAKE_GROUP_',
                                'QODERWAKE_RUN_', 'QODERWAKE_AGENT_')):
                env.pop(name)
        env.pop('BUN_BE_BUN', None)
        return env

    def detail(self, identity, operation):
        spec = self.catalog.get(operation)
        self.catalog.authorize(identity, spec)
        if spec.get('panelOnly'):
            return dict(spec, available=True)
        try:
            fingerprint = (os.stat(self.binary).st_mtime_ns, os.stat(self.binary).st_size)
        except OSError:
            return dict(spec, available=False, error='cli_binary_unavailable')
        with self.lock:
            previous = self.probes.get(operation)
            if previous and previous[0] == fingerprint and time.monotonic() - previous[1] < 300:
                return previous[2]
        try:
            process = subprocess.run([self.binary, *spec['path'], '--help'], env=self.environment(),
                                     capture_output=True, text=True, timeout=20)
            if process.returncode or len(process.stdout) > 131072:
                raise ValueError('cli_help_unavailable')
            current = parse_help(spec['path'], process.stdout)
            if current['revision'] != spec['revision']:
                # 新版本参数未经登记，显示差异并停止执行，不能把旧 schema 当作新契约。
                result = dict(spec, available=False, error='cli_contract_changed', currentHelp=process.stdout)
            else:
                result = dict(spec, available=True)
        except (ValueError, OSError, subprocess.SubprocessError):
            result = dict(spec, available=False, error='cli_help_unavailable')
        with self.lock:
            self.probes[operation] = (fingerprint, time.monotonic(), result)
        return result

    def _digest(self, body):
        return hmac.new(self.key, json.dumps(body, sort_keys=True, ensure_ascii=False).encode(), hashlib.sha256).hexdigest()

    def submit(self, identity, body, request_key):
        if not isinstance(request_key, str) or not re.fullmatch(r'[A-Za-z0-9_-]{16,100}', request_key):
            raise ValueError('cli_request_key_required')
        spec = self.catalog.get(body.get('operation'))
        params = body.get('parameters', {})
        build_argv(spec, params)
        self.catalog.authorize(identity, spec, params)
        if spec.get('panelOnly'):
            raise AccessError(409, 'cli_use_panel_action')
        if body.get('revision') != spec['revision']:
            raise AccessError(409, 'cli_contract_changed')
        if spec['effect'] != 'read' and body.get('confirm') != spec['id']:
            raise AccessError(409, 'confirmation_required')
        interactive = body.get('interactive', False)
        timeout = body.get('timeout', 300)
        if type(interactive) is not bool or type(timeout) is not int or not 10 <= timeout <= 86400:
            raise ValueError('invalid_cli_execution')
        if interactive and identity['role'] != 'admin':
            raise AccessError(403, 'permission_denied')
        context, stdin = body.get('context', {}), body.get('stdin', '')
        if not isinstance(context, dict) or set(context)-set(CONTEXT_KEYS):
            raise ValueError('invalid_cli_context')
        if (not isinstance(stdin, str) or len(stdin.encode())>60000 or '\0' in stdin or
                (interactive and stdin)):
            raise ValueError('invalid_cli_input')
        if (context or stdin) and identity['role'] != 'admin':
            raise AccessError(403, 'permission_denied')
        for value in context.values():
            if not isinstance(value,str) or '\0' in value or len(value)>4096:
                raise ValueError('invalid_cli_context')
        digest = self._digest(body)
        with self.lock, self.db() as con:
            previous = con.execute('SELECT * FROM jobs WHERE principal=? AND request_key=?',
                                   (identity['principal'], request_key)).fetchone()
            if previous:
                if previous['digest'] != digest:
                    raise AccessError(409, 'idempotency_key_conflict')
                return self._public(previous)
            if sum(j['status'] in ACTIVE for j in self.jobs.values()) >= 4:
                raise AccessError(429, 'concurrency_limited')
            current = self.detail(identity, spec['id'])
            if not current['available']:
                raise AccessError(409, current['error'])
            now, jid = time.time(), uuid.uuid4().hex
            waker = params.get('options', {}).get('--waker-id', '')
            if not isinstance(waker, str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,100}', waker):
                waker = ''
            con.execute('INSERT INTO jobs(id,principal,request_key,digest,operation,waker,status,created,updated) VALUES(?,?,?,?,?,?,?,?,?)',
                        (jid, identity['principal'], request_key, digest, spec['id'], waker, 'queued', now, now))
            job = {'id':jid, 'principal':identity['principal'], 'operation':spec['id'], 'waker':waker,
                   'status':'queued', 'created':now, 'updated':now, 'exit_code':None, 'error':'',
                   'output':'', 'outputBytes':0, 'offset':0, 'truncated':False, 'cancel':False, 'process':None, 'fd':None,
                   'interactive':interactive, 'inputLock':threading.Lock(), 'secrets':[]}
            self.jobs[jid] = job
            completed = [j for j in self.jobs.values() if j['status'] not in ACTIVE]
            for old in sorted(completed, key=lambda j:j['created'])[:-MAX_JOBS]:
                self.jobs.pop(old['id'], None)
        threading.Thread(target=self._run, args=(job, dict(identity), spec, body), daemon=True,
                         name='native-'+jid[:8]).start()
        return self._public(job)

    def _public(self, row):
        return {k: row[k] for k in ('id','operation','waker','status','created','updated','exit_code','error')}

    def _owned(self, identity, row):
        if not row or (row['principal'] != identity['principal'] and identity['role'] != 'admin'):
            raise AccessError(404, 'cli_job_not_found')
        spec = self.catalog.get(row['operation'])
        self.catalog.authorize(identity, spec)
        if identity['role'] != 'admin' and row['waker'] not in identity.get('wakers', []):
            raise AccessError(403, 'waker_scope_rejected')

    def list(self, identity):
        with self.db() as con:
            rows = con.execute('SELECT * FROM jobs WHERE principal=? ORDER BY created DESC LIMIT 50',
                               (identity['principal'],)).fetchall()
        result = []
        for row in rows:
            try:
                self._owned(identity, row)
            except AccessError:
                continue
            result.append(self._public(row))
        return result

    def get(self, identity, jid, offset=0):
        try:
            offset = int(offset)
        except (TypeError, ValueError):
            raise ValueError('invalid_cli_cursor') from None
        if offset < 0:
            raise ValueError('invalid_cli_cursor')
        with self.lock, self.db() as con:
            row = con.execute('SELECT * FROM jobs WHERE id=?', (jid,)).fetchone()
            self._owned(identity, row)
            result = self._public(row)
            job = self.jobs.get(jid)
            if not job:
                return dict(result, output='', nextOffset=offset, outputAvailable=False, interactive=False)
            start = max(job['offset'], offset)
            output = job['output'][start-job['offset']:start-job['offset']+65536]
            return dict(result, output=output, nextOffset=start+len(output),
                        outputAvailable=True, truncated=job['offset']>0,
                        interactive=job['interactive'], canInput=job['fd'] is not None and job['status']=='running')

    def control(self, identity, jid, action, value=''):
        with self.lock, self.db() as con:
            row = con.execute('SELECT * FROM jobs WHERE id=?', (jid,)).fetchone()
            self._owned(identity, row)
            job = self.jobs.get(jid)
            if not job or job['status'] not in ACTIVE:
                raise AccessError(409, 'cli_job_finished')
            if action == 'cancel':
                job['cancel'] = True
                return {'ok':True, 'id':jid}
            if action != 'input' or not job['interactive'] or identity['role'] != 'admin':
                raise AccessError(403, 'permission_denied')
            if not isinstance(value, str) or len(value.encode()) > 4000 or '\0' in value:
                raise ValueError('invalid_cli_input')
            # 交互值可能是 token；屏蔽其回显，也不记录进审计。
            if value.strip():
                job['secrets'].append(value.strip())
            with job['inputLock']:
                if job['fd'] is None:
                    raise AccessError(409, 'cli_input_not_ready')
                os.write(job['fd'], value.encode())
            return {'ok':True, 'id':jid}

    def _state(self, job, status, error='', exit_code=None):
        with self.lock, self.db() as con:
            job.update(status=status, updated=time.time(), error=error, exit_code=exit_code)
            con.execute('UPDATE jobs SET status=?,updated=?,error=?,exit_code=? WHERE id=?',
                        (status, job['updated'], error, exit_code, job['id']))

    def _append(self, job, value):
        clean = redact(value, job['secrets'])
        clean = re.sub(r'\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07]*(?:\x07|\x1b\\)', '', clean)
        clean = ''.join(c for c in clean if c in '\n\r\t' or ord(c) >= 32)
        with self.lock:
            job['output'] += clean
            job['outputBytes'] += len(clean.encode())
            if job['outputBytes'] > MAX_OUTPUT:
                previous_length = len(job['output'])
                job['output'] = job['output'].encode()[-MAX_OUTPUT:].decode('utf-8','ignore')
                job['offset'] += previous_length-len(job['output'])
                job['outputBytes'] = len(job['output'].encode())

    @staticmethod
    def _partial_output(value, secrets):
        # PTY 提示可以不带换行。保留可能跨块的秘密前缀，下一块到达后再遮蔽。
        retained = 0
        for secret in secrets:
            if not isinstance(secret,str) or len(secret)<2:continue
            for size in range(min(len(value),len(secret)-1),retained,-1):
                if value.endswith(secret[:size]):
                    retained=size
                    break
        return (value[:-retained],value[-retained:]) if retained else (value,'')

    @staticmethod
    def _kill(process):
        if process and process.poll() is None:
            try:
                os.killpg(process.pid, signal.SIGTERM)
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=3)
            except ProcessLookupError:
                pass

    def _run(self, job, identity, spec, body):
        process, master, slave = None, None, None
        dispatched = False
        status, error, exit_code = 'failed', '', None
        try:
            with self.guard(identity, spec, body), contextlib.ExitStack() as cleanup:
                cleanup.callback(lambda:self._kill(process))
                if job['cancel']:
                    status = 'cancelled'
                    return
                current = self.detail(identity,spec['id'])
                if not current['available']:
                    raise AccessError(409,current['error'])
                argv = build_argv(spec, self.resolve(identity, body.get('parameters', {})))
                params = body.get('parameters', {})
                job['secrets'].extend(v for k,v in body.get('context',{}).items() if SECRET.search(k))
                if body.get('stdin'):
                    job['secrets'].append(body['stdin'])
                for name, value in params.get('options', {}).items():
                    if SECRET.search(name) or name in ('--json','--env','--headers','--header'):
                        job['secrets'].extend(value if isinstance(value, list) else [value])
                if spec['id'] in ('config.set','settings.set','settings.global.set'):
                    job['secrets'].extend(params.get('arguments', {}).values())
                streams = {'stdin':subprocess.PIPE if body.get('stdin') else subprocess.DEVNULL,
                           'stdout':subprocess.PIPE, 'stderr':subprocess.STDOUT}
                if job['interactive']:
                    master, slave = pty.openpty()
                    attrs = termios.tcgetattr(slave)
                    attrs[3] &= ~(termios.ECHO | termios.ECHONL)
                    termios.tcsetattr(slave, termios.TCSANOW, attrs)
                    streams = {'stdin':slave, 'stdout':slave, 'stderr':slave}
                process = subprocess.Popen([self.binary, *argv], env=dict(self.environment(), **body.get('context',{})),
                                           cwd=self.home, start_new_session=True, **streams)
                dispatched = True
                if slave is not None:
                    os.close(slave); slave = None
                with self.lock:
                    job['process'], job['fd'] = process, master
                self._state(job, 'running')
                stream_fd = master if master is not None else process.stdout.fileno()
                os.set_blocking(stream_fd, False)
                decoder = codecs.getincrementaldecoder('utf-8')('replace')
                pending, deadline = '', time.monotonic()+body.get('timeout',300)
                with selectors.DefaultSelector() as selector:
                    selector.register(stream_fd, selectors.EVENT_READ, 'output')
                    input_bytes = body.get('stdin','').encode()
                    if input_bytes:
                        os.set_blocking(process.stdin.fileno(),False)
                        selector.register(process.stdin.fileno(),selectors.EVENT_WRITE,'input')
                    while selector.get_map():
                        if job['cancel'] or time.monotonic() >= deadline:
                            error = 'cli_cancelled' if job['cancel'] else 'cli_timeout'
                            self._kill(process)
                            break
                        for key, _ in selector.select(0.15):
                            if key.data == 'input':
                                try:
                                    written = os.write(key.fd,input_bytes[:8192])
                                    input_bytes = input_bytes[written:]
                                except BrokenPipeError:
                                    input_bytes = b''
                                if not input_bytes:
                                    selector.unregister(key.fd)
                                    process.stdin.close()
                                continue
                            try:
                                data = os.read(key.fd, 32768)
                            except BlockingIOError:
                                continue
                            except OSError:
                                data = b''
                            if not data:
                                selector.unregister(key.fd)
                                continue
                            pending += decoder.decode(data)
                            if '\n' in pending:
                                lines, pending = pending.rsplit('\n', 1)
                                self._append(job, lines+'\n')
                            if len(pending) > 131072:
                                self._append(job, '[单行输出超过上限，已省略]\n')
                                pending = ''
                        # PTY 提示通常无换行；呈现提示后等待显式输入。
                        if job['interactive'] and pending and not selector.select(0):
                            ready,pending=self._partial_output(pending,job['secrets'])
                            self._append(job, ready)
                pending += decoder.decode(b'',final=True)
                if pending:
                    self._append(job, pending)
                exit_code = process.wait(timeout=5)
                status = ('unknown' if spec['effect']=='write' else 'cancelled') if error else (
                    'succeeded' if exit_code == 0 else 'failed')
        except AccessError as exc:
            error = exc.code
        except (OSError, ValueError, subprocess.SubprocessError):
            error = 'cli_execution_failed'
            if dispatched and spec['effect'] == 'write':
                status = 'unknown'
        except Exception:
            error = 'cli_execution_failed'
            if dispatched and spec['effect'] == 'write':
                status = 'unknown'
        finally:
            self._kill(process)
            with job['inputLock']:
                job['fd'] = None
                if master is not None:
                    os.close(master)
            if slave is not None:
                os.close(slave)
            if process and process.stdout:
                process.stdout.close()
            if process and process.stdin:
                process.stdin.close()
            self._state(job, status, error, exit_code)
            job['secrets'] = []
            self.audit(identity, spec['id'], job['id'], job['waker'], status)

    def close(self):
        with self.lock:
            jobs = [j for j in self.jobs.values() if j['status'] in ACTIVE]
            for job in jobs:
                job['cancel'] = True
        for job in jobs:
            self._kill(job['process'])
