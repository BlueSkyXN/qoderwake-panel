#!/usr/bin/env python3
"""QoderWake Panel：沿用官方视觉语言的独立增强面板。

总览、聊天、模型、机器人、用量、安全、网络、插件与 API。
面板通过 daemon API 与 CLI 工作；受支持补丁必须经过哈希校验、预检和显式确认。

全部部署相关路径经环境变量参数化（公开仓不含任何私有部署信息）：
  QW_ROOT        工作根目录（token / 备份 / 用量库 / 日志都在其下）   默认 ~/.qoderwake-panel
  QW_HOME        daemon HOME（含 qodercli/settings.json）            默认 $QW_ROOT/test-home
  QW_DAEMON_BIN  daemon / CLI 可执行文件                             默认 PATH 里的 qoderwake-cn
  QW_DAEMON_URL  daemon API 地址                                      默认 http://127.0.0.1:19830
  QW_WHITELIST   出网白名单 JSON（{"IP": "标签"}）                    默认就近 config/whitelist[.example].json
  QW_PORT        面板端口                                             默认 19831
启动：panel/restart-panel.sh（读同目录 panel.env，该文件 gitignore）。
"""
import datetime as dt
import hashlib
import hmac
import ipaddress
import socket
import stat
import tempfile
import ssl
from concurrent.futures import ThreadPoolExecutor
from urllib.parse import urlsplit, quote, urlencode
from panel_security import Security, AccessError, AdmissionController, COOKIE
from panel_patches import PatchManager
from provider_config import ProviderSettings, ProviderProbePlans, SettingsConflict, provider_editability, require_revision
from deletion_preflight import DeletionPreflight
from daemon_transport import DaemonSession, DaemonTransportError, MAX_BOOTSTRAP_BYTES
from gateway_policy import validate_cfg, policy_hash
from log_io import tail_records
import usage_store
from gateway_runtime import (
    STATE_VERSION, gateway_port_status, identity_status, load_state,
    verify_generation_files
)
import json
import os
import re
import sqlite3
import subprocess
import tarfile
import threading
import time
import urllib.request
import urllib.error
import uuid
import http.cookiejar
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ROOT = Path(os.environ.get('QW_ROOT') or Path.home() / 'qoderwake-panel')
HOME = Path(os.environ.get('QW_HOME') or ROOT / 'test-home')
DAEMON_BIN = os.environ.get('QW_DAEMON_BIN') or 'qoderwake-cn'
DAEMON = os.environ.get('QW_DAEMON_URL') or 'http://127.0.0.1:19830'
PORT = int(os.environ.get('QW_PORT') or 19831)
BIND = os.environ.get('QW_BIND') or '127.0.0.1'
VERSION = '0.12.3'
ASSETS = Path(__file__).resolve().parent / 'static'
SECURITY = None
PATCHES = None
PROVIDER_SETTINGS = None
PROVIDER_PROBES = ProviderProbePlans()
DAEMON_SESSION = None
ADMISSION = AdmissionController()
PUBLIC_ORIGIN = os.environ.get('QW_PUBLIC_ORIGIN', '').rstrip('/')
PLUGIN_WRITES_ENABLED = os.environ.get('QW_EXPERIMENTAL_PLUGIN_WRITES') == '1'
SETTINGS = HOME / 'qodercli/settings.json'
RUNS = HOME / 'qodercli/logs/runs'
BACKUPS = ROOT / 'backups'
TOKEN_FILE = ROOT / 'admin-token.txt'
DB = ROOT / 'usage.db'
CFG = Path(__file__).resolve().parent / 'config'
LOCK = threading.Lock()
RESOURCE_LOCK = threading.RLock()


def load_whitelist():
    for c in [os.environ.get('QW_WHITELIST'), ROOT / 'config/whitelist.json',
              CFG / 'whitelist.json', CFG / 'whitelist.example.json']:
        if c and Path(c).exists():
            try:
                return {k: v for k, v in json.load(open(c)).items() if re.match(r'^\d+\.\d+\.\d+\.\d+$', k)}
            except Exception:
                pass
    return {}


WL = load_whitelist()


def security():
    global SECURITY
    with LOCK:
        if SECURITY is None:
            SECURITY = Security(ROOT)
    return SECURITY


def safe_id(value):
    if not isinstance(value, str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,100}', value):
        raise ValueError('invalid_identifier')
    return value


def opaque_id(value):
    if (not isinstance(value, str) or not 1 <= len(value) <= 256 or value in ('.', '..') or
            any(c in value for c in '/\\?#%') or any(ord(c) < 32 or ord(c) == 127 for c in value)):
        raise ValueError('invalid_identifier')
    return value


def text(value, limit=4000, empty=False):
    if not isinstance(value, str) or len(value) > limit or (not empty and not value.strip()):
        raise ValueError('invalid_text')
    return value.strip()


def atomic_json(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.is_symlink():
        raise ValueError('symlink_not_allowed')
    fd, tmp = tempfile.mkstemp(prefix='.panel-', dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as f:
            json.dump(data, f, ensure_ascii=False, indent=2)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)


def console_op():
    global DAEMON_SESSION
    token = Path(os.environ.get('QW_DAEMON_FRONTEND_TOKEN_FILE') or HOME / '.auth/token')
    with LOCK:
        if (DAEMON_SESSION is None or DAEMON_SESSION.base_url != DAEMON or
                DAEMON_SESSION.token_path != token):
            DAEMON_SESSION = DaemonSession(DAEMON, token)
    DAEMON_SESSION.ensure()
    return DAEMON_SESSION, DAEMON_SESSION.headers()


def api_state():
    st = {'daemon': 'stopped', 'models': [], 'wakers': [], 'whoami': None}
    try:
        d = daemon_json('GET', '/api/models', timeout=8)
        if not d.get('success', True):
            raise RuntimeError('daemon_auth_or_api_error')
        st['daemon'] = 'running'
        st['models'] = [{'id': m.get('id'), 'name': m.get('name') or m.get('displayName'),
                         'provider': m.get('provider')} for m in d.get('data', {}).get('models', [])]
        d = daemon_json('GET', '/api/agents', timeout=8)
        items = d.get('data', d) if isinstance(d, dict) else d
        items = items if isinstance(items, list) else items.get('list', [])
        if not isinstance(items, list) or any(not isinstance(a, dict) for a in items):
            raise ValueError('invalid_waker_list')
        with ThreadPoolExecutor(max_workers=4) as pool:
            st['wakers'] = list(pool.map(waker_summary, items))
    except Exception:
        st['error'] = 'daemon_unavailable'
    try:
        value = object_data(daemon_json('GET', '/api/frontend-auth/me', timeout=8))
        profile = value.get('profile')
        if value.get('authenticated') is True and isinstance(profile, dict):
            name = profile.get('name')
            if isinstance(name, str):
                st['whoami'] = {'name': name[:200]}
    except Exception:
        pass
    return st


def waker_summary(item):
    pref = '(未知)'
    try:
        value = deletion_preference(item.get('agentId', ''))
        model = value.get('noProject')
        if model is None or model == '':
            pref = '(默认 auto)'
        elif isinstance(model, str):
            pref = model
        elif isinstance(model, dict):
            names = [model[key] for key in ('modelId', 'model') if key in model]
            if names and all(isinstance(name, str) and name.strip() for name in names) and len(set(names)) == 1:
                pref = names[0]
    except Exception:
        pass
    return {'id': item.get('agentId'), 'name': item.get('name'), 'preference': pref}


def provider_store():
    global PROVIDER_SETTINGS
    with LOCK:
        if PROVIDER_SETTINGS is None or PROVIDER_SETTINGS.path != SETTINGS:
            PROVIDER_SETTINGS = ProviderSettings(SETTINGS, ROOT / 'provider-settings.previous.json')
    return PROVIDER_SETTINGS


def providers_state():
    return provider_store().summaries()[0]


USAGE_LOCK = threading.Lock()


def init_db():
    usage_store.initialize(DB)


def usage_data():
    with USAGE_LOCK:
        return usage_store.usage_data(DB, RUNS)


def egress_data():
    if not __import__('shutil').which('ss'):
        return []
    state = daemon_process_state()
    pid = str(state['pid']) if state else None
    out = []
    if pid:
        ss = subprocess.run(['ss', '-tnp'], capture_output=True, text=True).stdout
        for line in ss.splitlines():
            if 'pid=%s,' % pid in line:
                dst = line.split()[4]
                ip = dst.rsplit(':', 1)[0]
                if ip.startswith('127.'):
                    continue
                out.append({'dest': dst, 'label': WL.get(ip, 'UNKNOWN-需人工判定'), 'ok': ip in WL})
    return out


def backups_list():
    if not BACKUPS.exists():
        return []
    out = []
    for f in sorted(BACKUPS.iterdir(), key=lambda p: -p.stat().st_mtime)[:30]:
        if f.is_file():
            out.append({'name': f.name, 'size': f.stat().st_size,
                        'mtime': time.strftime('%m-%d %H:%M', time.localtime(f.stat().st_mtime))})
    return out


def do_backup():
    BACKUPS.mkdir(parents=True, exist_ok=True, mode=0o700)
    name = 'qw-backup-' + str(time.time_ns()) + '.tar.gz'
    fd = os.open(BACKUPS / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'wb') as f, tarfile.open(fileobj=f, mode='w:gz') as tar:
        for path, arc in [(SETTINGS, 'settings.json'), (HOME / '.auth', 'auth'), (HOME / 'config/settings.json', 'daemon-settings.json')]:
            if path.exists():
                tar.add(path, arcname=arc)
    return name


def del_backup(name):
    if not isinstance(name, str) or not re.fullmatch(r'qw-backup-[A-Za-z0-9-]+\.tar\.gz', name):
        raise ValueError('invalid_backup_name')
    f = BACKUPS / name
    if f.is_symlink() or not f.is_file():
        raise ValueError('backup_not_found')
    f.unlink()
    return True, '已删除'


def valid_provider_url(value):
    value = text(value, 2048)
    u = urlsplit(value)
    if u.scheme not in ('https', 'http') or not u.hostname or u.username or u.password or u.query or u.fragment:
        raise ValueError('invalid_provider_url')
    if u.scheme != 'https' and u.hostname not in ('localhost', '127.0.0.1', '::1'):
        raise ValueError('provider_requires_https')
    try:
        addr = ipaddress.ip_address(u.hostname)
    except ValueError:
        addr = None
    if addr and (addr.is_link_local or addr.is_unspecified or addr.is_multicast):
        raise ValueError('provider_destination_rejected')
    return value.rstrip('/')


def write_provider(name, base_url, api_key, model, display, base_revision):
    name, model = text(name, 120), text(model, 200)
    if not re.fullmatch(r'[A-Za-z0-9_.-]+', name):
        raise ValueError('invalid_provider_name')
    base_url = valid_provider_url(base_url)
    api_key, display = text(api_key, 2048, True), text(display, 200, True)
    def transform(data):
        providers = data.setdefault('providers', {})
        if not isinstance(providers, dict):
            raise ValueError('provider_settings_invalid')
        old = providers.get(name, {})
        if name in providers and not provider_editability(old)[0]:
            raise ValueError('provider_requires_official_editor')
        key = api_key or old.get('apiKey', '')
        if not key:
            raise ValueError('api_key_required')
        item = dict((old.get('models') or [{}])[0])
        item.update({'model': model, 'displayName': display or model})
        updated = dict(old)
        updated.update({'baseUrl': base_url, 'apiKey': key, 'type': 'openai-compatible', 'authType': 'bearer',
                        'model': model, 'models': [item]})
        providers[name] = updated
        return data
    return provider_store().update(base_revision, transform)


def delete_provider(name, base_revision):
    name = text(name, 120)
    def transform(data):
        providers = data.get('providers', {})
        if not isinstance(providers, dict) or name not in providers:
            raise ValueError('provider_not_found')
        if not provider_editability(providers[name])[0]:
            raise ValueError('provider_requires_official_editor')
        del providers[name]
        return data
    return provider_store().update(base_revision, transform)


def provider_probe_plan(body, principal):
    mode = body.get('mode')
    if mode == 'saved':
        name = text(body.get('name'), 120)
        snapshot = provider_store().read()
        revision = require_revision(body.get('baseRevision'))
        if not hmac.compare_digest(snapshot['revision'], revision):
            raise DaemonConflict('provider_settings_changed')
        provider = (snapshot['data'].get('providers') or {}).get(name)
        if not provider:
            raise ValueError('provider_not_found')
        if not provider_editability(provider)[0]:
            raise ValueError('provider_requires_official_editor')
        url = valid_provider_url(provider.get('baseUrl'))
        return PROVIDER_PROBES.create_saved(principal, name, revision, url)
    if mode == 'adhoc':
        url = valid_provider_url(body.get('baseUrl'))
        key = text(body.get('apiKey', ''), 2048)
        return PROVIDER_PROBES.create_adhoc(principal, url, key)
    raise ValueError('invalid_provider_probe_mode')


def execute_provider_probe(body, principal):
    mode = body.get('mode')
    if mode == 'saved':
        name = text(body.get('name'), 120)
        revision = require_revision(body.get('baseRevision'))
        snapshot = provider_store().read()
        if not hmac.compare_digest(snapshot['revision'], revision):
            raise DaemonConflict('provider_settings_changed')
        provider = (snapshot['data'].get('providers') or {}).get(name)
        if not provider or not provider_editability(provider)[0]:
            raise ValueError('provider_requires_official_editor')
        url, key = valid_provider_url(provider.get('baseUrl')), provider.get('apiKey', '')
        PROVIDER_PROBES.consume(body.get('plan'), principal, mode, name=name, revision=revision, url=url)
    elif mode == 'adhoc':
        url, key = valid_provider_url(body.get('baseUrl')), text(body.get('apiKey', ''), 2048)
        PROVIDER_PROBES.consume(body.get('plan'), principal, mode, url=url, api_key=key)
    else:
        raise ValueError('invalid_provider_probe_mode')
    return test_provider(url, key)


def cli(*args, timeout=30):
    env = dict(os.environ, QODERWAKE_HOME=str(HOME))
    return subprocess.run([DAEMON_BIN] + list(args), capture_output=True, text=True, env=env, timeout=timeout)


def set_preference(waker, model):
    r = cli('models', 'preference', 'set', '--waker-id', waker, '--model', model, timeout=25)
    return r.returncode == 0, (r.stdout + r.stderr)[-160:]


PROCESS_CONTROL = None


def process_control():
    global PROCESS_CONTROL
    import importlib.util
    with LOCK:
        if PROCESS_CONTROL is None:
            here = Path(__file__).resolve().parent
            candidates = (here.parent / 'ops/process-control.py', here / 'process-control.py')
            path = next((item for item in candidates if item.is_file() and not item.is_symlink()), None)
            if path is None:
                raise ValueError('process_controller_missing')
            spec = importlib.util.spec_from_file_location('panel_process_control', path)
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
            PROCESS_CONTROL = module
    return PROCESS_CONTROL


def daemon_process_state():
    import shutil
    try:
        control = process_control()
        value = control.read_state(ROOT / 'process-state/daemon-cn.json')
        daemon = urlsplit(DAEMON)
        binary = shutil.which(DAEMON_BIN) or DAEMON_BIN
        if (value is None or value['name'] != 'daemon-cn' or
                value['profile'] != 'daemon' or value['status'] != 'running' or
                value['home'] != str(HOME.resolve(strict=False)) or
                value['launch']['path'] != str(Path(binary).resolve(strict=True)) or
                daemon.hostname not in ('127.0.0.1', 'localhost', '::1') or
                value['port'] != (daemon.port or (443 if daemon.scheme == 'https' else 80)) or
                control.identity_status(value) != 'match' or
                control.port_status(value) != 'owned'):
            return None
        return value
    except (OSError, ValueError, KeyError, TypeError):
        return None


def restart_daemon():
    qwa = None
    for c in [Path(__file__).resolve().parent / 'qw-ctl.sh',
              Path(__file__).resolve().parent.parent / 'ops' / 'qw-ctl.sh',
              ROOT / 'staging' / 'qw-ctl.sh']:
        if c.exists() and not c.is_symlink():
            qwa = c
            break
    if not qwa:
        return False, '未找到 qw-ctl.sh（应与面板同目录或 ops/ 下）'
    env = dict(runtime_env(), QW_ROOT=str(ROOT), QW_CN_HOME=str(HOME))
    if Path(DAEMON_BIN).is_absolute():
        env['QW_CN_BIN'] = DAEMON_BIN
    state = daemon_process_state()
    if not state:
        return False, 'daemon 受管状态缺失或无效；请先用受管启动器建立精确身份记录'
    env['QW_DAEMON_MODE'] = state['mode']
    env['QW_CN_PORT'] = str(state['port'])
    if state['mode'] == 'gateway':
        env['QW_GW_URL'] = state['endpoint']
    status_run = subprocess.run(
        ['bash', str(qwa), 'cn', 'status'], capture_output=True,
        text=True, env=env, timeout=30)
    try:
        observed = json.loads(status_run.stdout)
    except ValueError:
        observed = {}
    if (status_run.returncode or observed.get('managed') is not True or
            observed.get('running') is not True or observed.get('healthy') is not True or
            observed.get('pid') != state['pid'] or observed.get('mode') != state['mode']):
        return False, 'daemon state、进程、端口与健康响应未同时通过核验，已拒绝重启'
    mode = state['mode']
    env['QW_DAEMON_MODE'] = mode
    if mode == 'gateway':
        env['QW_GW_URL'] = state['endpoint']
    args = ['bash', str(qwa), 'cn', 'start']
    if mode == 'gateway':
        starter = qwa.with_name('start-cn-daemon-gw.sh')
        if not starter.is_file() or starter.is_symlink():
            return False, '未找到可信网关模式启动器，已拒绝降级为直连启动'
        args = ['bash', str(starter)]
    r = subprocess.run(args, capture_output=True, text=True, env=env, timeout=140)
    if r.returncode:
        return False, (r.stdout+r.stderr).strip()[-160:]
    updated = daemon_process_state()
    if not updated or updated['mode'] != mode:
        return False, '启动器返回成功，但新的 daemon 精确身份或模式未通过核验'
    try:
        result = json.loads(r.stdout)
    except ValueError:
        return False, '启动器返回成功，但没有可验证的结构化结果'
    if (result.get('ok') is not True or result.get('pid') != updated['pid'] or
            result.get('mode') != mode or result.get('version') != updated['healthVersion']):
        return False, 'daemon 启动结果与持久状态不一致'
    return True, 'daemon 已按受管模式重启并通过进程、端口与健康核验'


class DaemonConflict(Exception):
    pass


class DaemonRequestError(AccessError):
    def __init__(self, status, code, detail=None):
        super().__init__(status, code)
        self.detail = detail or {}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def test_provider(base_url, api_key):
    try:
        from provider_transport import probe
        return probe(valid_provider_url(base_url), api_key)
    except urllib.error.HTTPError as e:
        if e.code == 403:
            return False, ('HTTP 403：官方拒绝自定义通道。排查：①控制页 BYOK 开关（自定义 Provider 通道）'
                           '是否开启、是否已重启生效；②Provider 键是否与模型目录条目同名冲突（CLI 有 catalog '
                           '磁盘缓存，改后需清理）；③settings 是否残留 endpoint/vpc 字段')
        return False, 'HTTP %d（key 或端点问题）' % e.code
    except Exception:
        return False, '端点检查失败，请核对地址、凭据与网络；未转发到重定向目的地'


def gw_ask(waker_id, message, timeout=110):
    sid = chat_new(safe_id(waker_id), 'API 会话')
    if not chat_send(sid, text(message, 16000)):
        return False, '发送失败'
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        result = chat_messages(sid)
        if result['status'] not in ('open', 'idle'):
            replies = [m['text'] for m in result['messages'] if m['role'] == 'assistant']
            return result['status'] == 'success' and bool(replies), '\n'.join(replies) or '回合结束，无文本回复'
        time.sleep(2)
    return False, '等待超时；请求已提交，请在聊天记录查看，勿盲目重复发送'


def daemon_error_detail(value):
    if not isinstance(value, dict):
        return {}
    data = value.get('data') if isinstance(value.get('data'), dict) else {}
    detail = {}
    for key in ('code', 'operationId', 'pendingId', 'status', 'retryable', 'currentRevision'):
        candidate = value.get(key, data.get(key))
        if isinstance(candidate, (str, int, bool)) and len(str(candidate)) <= 256:
            detail[key] = candidate
    return detail


def daemon_error_payload(stream):
    try:
        raw = stream.read(MAX_BOOTSTRAP_BYTES + 1)
    except Exception:
        return {}
    if len(raw) > MAX_BOOTSTRAP_BYTES:
        return {}
    try:
        return daemon_error_detail(json.loads(raw))
    except (ValueError, UnicodeDecodeError):
        return {}


def daemon_request_error(status, detail):
    code = detail.get('code') if isinstance(detail.get('code'), str) else ''
    if status == 409:
        raise DaemonConflict(code or 'daemon_conflict')
    if status == 404:
        raise DaemonRequestError(404, code or 'daemon_resource_not_found', detail)
    if status == 401:
        raise DaemonRequestError(403, code or 'daemon_owner_login_required', detail)
    if status == 403:
        raise DaemonRequestError(403, code or 'daemon_permission_denied', detail)
    if status == 429:
        raise DaemonRequestError(429, code or 'daemon_rate_limited', detail)
    if status == 202 or (
            status in (400, 422) and
            detail.get('status') in ('pending', 'running', 'accepted')):
        raise DaemonRequestError(202, code or 'daemon_operation_pending', detail)
    if status in (400, 422):
        raise DaemonRequestError(400, code or 'daemon_request_rejected', detail)
    raise DaemonRequestError(502, code or 'daemon_request_failed', detail)


def daemon_json(method, path, body=None, timeout=20):
    try:
        op, hdr = console_op()
    except DaemonTransportError as error:
        raise DaemonRequestError(error.status, error.code, error.payload)
    data = json.dumps(body).encode() if body is not None else None
    rq = urllib.request.Request(DAEMON + path, headers=hdr, data=data, method=method)
    try:
        response = op.open(rq, timeout=timeout, retry_auth=method in ('GET', 'HEAD'))
    except urllib.error.HTTPError as error:
        status = error.code
        try:
            detail = daemon_error_payload(error)
        finally:
            error.close()
        daemon_request_error(status, detail)
    except DaemonTransportError as error:
        raise DaemonRequestError(error.status, error.code, error.payload)
    status = getattr(response, 'status', getattr(response, 'code', 200))
    with response:
        raw = response.read(MAX_BOOTSTRAP_BYTES + 1)
    if len(raw) > MAX_BOOTSTRAP_BYTES:
        raise DaemonRequestError(502, 'daemon_response_too_large')
    try:
        result = json.loads(raw)
    except (ValueError, UnicodeDecodeError):
        raise DaemonRequestError(502, 'daemon_response_invalid')
    if not isinstance(result, dict):
        raise DaemonRequestError(502, 'daemon_response_invalid')
    detail = daemon_error_detail(result)
    if status == 202:
        daemon_request_error(status, detail)
    if result.get('success') is False:
        daemon_request_error(400, detail)
    return result


def object_data(value):
    value = value.get('data', value) if isinstance(value, dict) else value
    return value if isinstance(value, dict) else {}


def system_status():
    health = object_data(daemon_json('GET', '/api/health'))
    status = object_data(daemon_json('GET', '/api/v1/system/status'))
    update = status.get('update') if isinstance(status.get('update'), dict) else {}
    activity = status.get('activity') if isinstance(status.get('activity'), dict) else {}
    return {'version': status.get('version') or health.get('version'),
            'update': {k: update.get(k) for k in ('runningVersion','installedVersion','restartRequired','showUpdateDiagnostics')},
            'activity': {k: activity.get(k) for k in ('runningSessions','runningConversationSessions','runningConversationTasks','runningTriggerSessions','runningTriggerTasks','queuedTriggerTasks','hasRunningWork')},
            'timestamp': status.get('timestamp')}


ACTIVITY_COUNTERS = (
    'runningSessions', 'runningConversationSessions', 'runningConversationTasks',
    'runningTriggerSessions', 'runningTriggerTasks', 'queuedTriggerTasks'
)


def daemon_activity_clear():
    try:
        activity = system_status().get('activity')
    except Exception:
        raise AccessError(503, 'daemon_activity_unknown')
    if not isinstance(activity, dict) or not isinstance(activity.get('hasRunningWork'), bool):
        raise AccessError(503, 'daemon_activity_unknown')
    values = [activity.get(key) for key in ACTIVITY_COUNTERS]
    if any(type(value) is not int or value < 0 for value in values):
        raise AccessError(503, 'daemon_activity_unknown')
    return activity['hasRunningWork'] is False and all(value == 0 for value in values)


def wait_daemon_idle(timeout=30, interval=0.25):
    deadline = time.monotonic() + timeout
    consecutive = 0
    while time.monotonic() < deadline:
        if daemon_activity_clear():
            consecutive += 1
            if consecutive >= 2:
                return
        else:
            consecutive = 0
        time.sleep(interval)
    raise AccessError(503, 'daemon_activity_not_idle')


def enter_maintenance(principal, reason, ttl, lease, timeout=30):
    epoch = ADMISSION.begin_drain(principal, reason, ttl, lease=lease)
    try:
        ADMISSION.wait_clear(epoch, timeout)
        ADMISSION.transition(epoch, 'maintenance')
        return ADMISSION.status()
    except Exception:
        ADMISSION.complete(epoch)
        raise


def maintenance_transaction(principal, reason, action, lease, timeout=30):
    epoch = ADMISSION.begin_drain(principal, reason, lease=lease)
    try:
        ADMISSION.wait_clear(epoch, timeout)
        wait_daemon_idle(timeout)
        ADMISSION.transition(epoch, 'restarting')
        return action()
    finally:
        ADMISSION.complete(epoch)


def console_sessions(waker, offset=0, limit=30, origin=''):
    wid = safe_id(waker)
    offset, limit = int(offset), int(limit)
    if not 0 <= offset <= 100000 or not 1 <= limit <= 100:
        raise ValueError('invalid_pagination')
    params = {'offset': offset, 'limit': limit, 'pinned_first': 'true', 'task_source': 'all'}
    if origin:
        origin = text(origin, 100)
        if not re.fullmatch(r'[A-Za-z0-9_-]+', origin):
            raise ValueError('invalid_session_origin')
        params['origins'] = origin
    data = object_data(daemon_json('GET', '/api/agents/%s/console-sessions/query?%s' % (wid, urlencode(params))))
    rows = []
    for row in data.get('items', []):
        if not isinstance(row, dict) or row.get('local_agent_id') not in (None, '', wid):
            continue
        rows.append({k: row.get(k) for k in ('session_id','title','origin','session_status','unread','connection_status','created_at','updated_at')})
    return {'items': rows, 'has_more': bool(data.get('has_more'))}


def session_artifacts(sid):
    data = object_data(daemon_json('GET', '/api/sessions/%s/artifacts' % quote(opaque_id(sid), safe='')))
    out = {'target': data.get('target'), 'artifacts': []}
    for item in data.get('artifacts', []):
        if isinstance(item, dict):
            out['artifacts'].append({k:item.get(k) for k in ('id','type','title','description','relativePath','mimeType','filesChanged')})
    return out


def artifact_file(sid, artifact):
    sid, artifact = opaque_id(sid), opaque_id(artifact)
    if not any(row.get('id') == artifact for row in session_artifacts(sid)['artifacts']):
        raise DaemonRequestError(404, 'artifact_not_found')
    op, hdr = console_op()
    path = '/api/sessions/%s/artifacts/%s/file' % (quote(sid, safe=''), quote(artifact, safe=''))
    request = urllib.request.Request(DAEMON + path, headers=hdr)
    response = op.open(request, timeout=30)
    size = response.headers.get('Content-Length')
    if size and int(size) > 32 * 1024 * 1024:
        response.close()
        raise ValueError('artifact_too_large')
    data = response.read(32 * 1024 * 1024 + 1)
    response.close()
    if len(data) > 32 * 1024 * 1024:
        raise ValueError('artifact_too_large')
    ctype = response.headers.get_content_type()
    if ctype in ('text/html', 'application/xhtml+xml', 'image/svg+xml'):
        ctype = 'application/octet-stream'
    return data, ctype or 'application/octet-stream'


def chat_sessions(waker):
    d = daemon_json('GET', '/api/sessions?limit=30&agentId=%s' % safe_id(waker))
    out = []
    for s in d.get('data') or []:
        if s.get('agentId') and s.get('agentId') != waker:
            continue
        out.append({'id': s.get('sessionId'), 'title': s.get('title') or '(未命名)',
                    'status': s.get('status'), 'startedAt': (s.get('startedAt') or '')[:16].replace('T', ' ')})
    return sorted(out, key=lambda s: s['startedAt'], reverse=True)[:100]


def chat_new(waker, title):
    d = daemon_json('POST', '/api/agents/%s/sessions' % safe_id(waker),
                    {'title': title or '面板会话', 'user_message_id': str(uuid.uuid4()), 'events': []})
    return (d.get('data') or {}).get('sessionId')


def chat_send(sid, message):
    sid, message = safe_id(sid), text(message, 16000)
    body = {'user_message_id': str(uuid.uuid4()), 'events': [
        {'event_type': 'user', 'payload': {'type': 'user', 'uuid': str(uuid.uuid4()),
         'message': {'role': 'user', 'content': [{'type': 'text', 'text': message}]}}}]}
    d = daemon_json('POST', '/api/sessions/%s/events' % sid, body)
    return bool(d.get('success'))


def fold_events(raw):
    msgs, status, index = [], 'idle', {}
    for e in raw:
        p = e.get('payload') or {}
        t = p.get('type')
        if t == 'user':
            status = 'open'
        if t in ('user', 'assistant'):
            message = p.get('message') or {}
            content = message.get('content', [])
            txt = content if isinstance(content, str) else '\n'.join(c.get('text', '') for c in content if c.get('type') == 'text')
            if not txt.strip():
                continue
            mid = message.get('id') or p.get('uuid') or e.get('event_id') or e.get('id')
            key = (t, mid) if mid else (t, len(msgs))
            item = {'role': t, 'text': txt}
            if key in index:
                msgs[index[key]] = item
            else:
                index[key] = len(msgs)
                msgs.append(item)
        elif t == 'result':
            status = p.get('subtype') or 'done'
    return {'messages': msgs, 'status': status}


def chat_messages(sid):
    d = daemon_json('GET', '/api/sessions/%s/events' % safe_id(sid))
    data = d.get('data', {})
    raw = data.get('data', data.get('events', [])) if isinstance(data, dict) else data if isinstance(data, list) else []
    return fold_events(raw)


def waker_detail(wid):
    d = daemon_json('GET', '/api/agents/%s' % safe_id(wid)).get('data')
    if not isinstance(d, dict) or (d.get('agentId') or d.get('id')) != wid:
        raise ValueError('invalid_waker_detail')
    return {k: d.get(k) for k in ('agentId', 'id', 'name', 'description', 'sessionTimeout', 'skills',
                                  'systemIdentity', 'revision', 'version', 'updatedAt', 'updated_at')}


def deletion_wakers():
    data = daemon_json('GET', '/api/agents').get('data')
    rows = data if isinstance(data, list) else data.get('list', data.get('items')) if isinstance(data, dict) else None
    if not isinstance(rows, list) or len(rows) > 10000:
        raise ValueError('invalid_waker_list')
    result = []
    for row in rows:
        if not isinstance(row, dict):
            raise ValueError('invalid_waker_list')
        wid = row.get('agentId') or row.get('id')
        if not isinstance(wid, str):
            raise ValueError('invalid_waker_list')
        result.append({'agentId': safe_id(wid), 'name': row.get('name')})
    return result


def deletion_triggers():
    rows = []
    for page in range(1, 501):
        data = object_data(daemon_json('GET', '/api/triggers?' + urlencode({'page': page, 'pageSize': 100})))
        items = data if isinstance(data, list) else data.get('items') if isinstance(data, dict) else None
        if not isinstance(items, list) or len(items) > 100:
            raise ValueError('invalid_trigger_page')
        rows.extend(items)
        if len(rows) > 10000:
            raise ValueError('deletion_scan_limit')
        pagination = data.get('pagination') if isinstance(data, dict) else None
        if isinstance(data, list):
            if len(items) == 100:
                raise ValueError('trigger_pagination_unavailable')
            break
        if not isinstance(pagination, dict):
            raise ValueError('trigger_pagination_unavailable')
        total = pagination.get('total')
        if type(total) is not int or total < 0 or total > 10000:
            raise ValueError('invalid_trigger_page')
        if len(rows) >= total:
            if len(rows) != total:
                raise ValueError('invalid_trigger_page')
            break
        if not items:
            raise ValueError('invalid_trigger_page')
    else:
        raise ValueError('deletion_scan_limit')
    if any(not isinstance(row, dict) for row in rows):
        raise ValueError('invalid_trigger_page')
    return rows


def deletion_channels():
    value = daemon_json('GET', '/api/channels').get('data')
    rows = value if isinstance(value, list) else value.get('items', value.get('channels')) if isinstance(value, dict) else None
    if not isinstance(rows, list) or len(rows) > 10000 or any(not isinstance(row, dict) for row in rows):
        raise ValueError('invalid_channel_list')
    return rows


def deletion_pairings():
    rows = []
    for offset in range(0, 10000, 100):
        data = object_data(daemon_json('GET', '/api/channels/pairing/pending?' + urlencode({
            'limit': 100, 'offset': offset, 'includeTotal': 'true'})))
        items = data.get('items') if isinstance(data, dict) else None
        total = data.get('total') if isinstance(data, dict) else None
        if not isinstance(items, list) or type(total) is not int or total < 0 or total > 10000:
            raise ValueError('invalid_pairing_page')
        rows.extend(items)
        if len(rows) >= total:
            if len(rows) != total:
                raise ValueError('invalid_pairing_page')
            break
        if not items:
            raise ValueError('invalid_pairing_page')
    else:
        if rows:
            raise ValueError('deletion_scan_limit')
    if any(not isinstance(row, dict) for row in rows):
        raise ValueError('invalid_pairing_page')
    return rows


def deletion_groups():
    data = daemon_json('GET', '/api/conversation-groups')
    value = data.get('data', data) if isinstance(data, dict) else data
    rows = value.get('groups') if isinstance(value, dict) else None
    if not isinstance(rows, list) or len(rows) > 10000:
        raise ValueError('invalid_group_list')
    details = []
    for row in rows:
        if not isinstance(row, dict):
            raise ValueError('invalid_group_list')
        gid = row.get('id') or row.get('groupId') or row.get('group_id')
        gid = opaque_id(gid)
        detail = daemon_json('GET', '/api/conversation-surface-groups/' + quote(gid, safe=''))
        detail = detail.get('data', detail) if isinstance(detail, dict) else detail
        if not isinstance(detail, dict):
            raise ValueError('invalid_group_detail')
        group = detail.get('group', detail)
        if not isinstance(group, dict):
            raise ValueError('invalid_group_detail')
        identifiers = [group[key] for key in ('id', 'groupId', 'group_id') if key in group]
        if not identifiers or any(value != gid for value in identifiers):
            raise ValueError('invalid_group_detail')
        details.append(detail)
    return details


def deletion_workflows(waker=None):
    path = '/api/agents/%s/workflows' % safe_id(waker) if waker else '/api/workflows'
    data = daemon_json('GET', path)
    value = data.get('data', data) if isinstance(data, dict) else data
    rows = value if isinstance(value, list) else value.get('workflows') if isinstance(value, dict) else None
    if not isinstance(rows, list) or len(rows) > 10000 or any(not isinstance(row, dict) for row in rows):
        raise ValueError('invalid_workflow_list')
    return rows


def deletion_sessions(waker):
    rows = []
    for offset in range(0, 10000, 100):
        data = object_data(daemon_json('GET', '/api/agents/%s/console-sessions/query?%s' % (
            safe_id(waker), urlencode({'offset': offset, 'limit': 100,
                                      'pinned_first': 'true', 'task_source': 'all'}))))
        items = data.get('items') if isinstance(data, dict) else None
        more = data.get('has_more', data.get('hasMore')) if isinstance(data, dict) else None
        if not isinstance(items, list) or not isinstance(more, bool) or len(items) > 100:
            raise ValueError('invalid_session_page')
        for row in items:
            if not isinstance(row, dict) or row.get('local_agent_id') not in (None, '', waker):
                raise ValueError('invalid_session_page')
            rows.append(row)
        if not more:
            return rows
        if not items:
            raise ValueError('invalid_session_page')
    raise ValueError('deletion_scan_limit')


def deletion_preference(waker):
    value = object_data(daemon_json('GET', '/api/model-preferences/' + safe_id(waker), timeout=8))
    if not isinstance(value, dict) or not {'noProject', 'byProject'}.intersection(value):
        raise ValueError('model_preference_unavailable')
    if 'byProject' in value and not isinstance(value['byProject'], dict):
        raise ValueError('model_preference_unavailable')
    return value


class DeletionSources:
    def wakers(self): return deletion_wakers()
    def triggers(self): return deletion_triggers()
    def channels(self): return deletion_channels()
    def pairings(self): return deletion_pairings()
    def groups(self): return deletion_groups()
    def workflows(self, waker=None): return deletion_workflows(waker)
    def sessions(self, waker): return deletion_sessions(waker)
    def preference(self, waker): return deletion_preference(waker)
    def callers(self): return security().callers()
    def waker_detail(self, waker): return waker_detail(waker)
    def activity(self): return system_status().get('activity')

    def provider_models(self, name):
        snapshot = provider_store().read()
        provider = (snapshot['data'].get('providers') or {}).get(name)
        if provider is None:
            raise ValueError('provider_not_found')
        if not provider_editability(provider)[0]:
            raise ValueError('provider_requires_official_editor')
        return [name + '/' + row['model'].strip() for row in provider['models']]


def deletion_scan(kind, target, revision=''):
    return DeletionPreflight(DeletionSources()).scan(kind, target, revision)


def deletion_preview(body, principal):
    kind = body.get('kind')
    target = body.get('target')
    if kind == 'waker':
        target = safe_id(target)
        revision = None
    elif kind == 'provider':
        target = text(target, 120)
        revision = require_revision(body.get('baseRevision'))
        current = provider_store().read()['revision']
        if not hmac.compare_digest(current, revision):
            raise SettingsConflict('provider_settings_changed')
    else:
        raise ValueError('invalid_deletion_kind')
    if security().unresolved_deletion(kind, target):
        raise AccessError(409, 'deletion_operation_unresolved')
    preview = deletion_scan(kind, target, revision or '')
    if preview['deletable']:
        raw, expires = security().create_deletion_preview(principal, preview)
        preview.update({'id': raw, 'expires': expires})
    else:
        preview.update({'id': None, 'expires': None})
    return preview


def deletion_operation_status(body, principal):
    operation = security().deletion_operation(principal, body.get('idempotencyKey'))
    return {'ok': True, **operation}


def reconcile_deletion(body):
    kind = body.get('kind')
    target = body.get('target')
    if kind == 'waker':
        target = safe_id(target)
        try:
            waker_detail(target)
        except DaemonRequestError as error:
            if error.status != 404:
                raise
            exists = False
        else:
            exists = True
    elif kind == 'provider':
        target = text(target, 120)
        providers = provider_store().read()['data'].get('providers') or {}
        if not isinstance(providers, dict):
            raise ValueError('provider_settings_invalid')
        exists = target in providers
    else:
        raise ValueError('invalid_deletion_kind')
    if exists:
        unresolved = security().unresolved_deletion(kind, target)
        if not unresolved:
            raise AccessError(404, 'deletion_operation_not_found')
        return {'status': unresolved['status'],
                'error': 'deletion_target_still_exists_unresolved',
                'reconciled': False}
    result = {'ok': True, 'status': 'succeeded', 'reconciled': True}
    if security().unresolved_deletion(kind, target):
        security().resolve_deletion(kind, target, 'succeeded', result)
    return {'status': 'succeeded', 'reconciled': True}


def release_deletion(body):
    kind = body.get('kind')
    target = body.get('target')
    if kind == 'waker':
        target = safe_id(target)
    elif kind == 'provider':
        target = text(target, 120)
    else:
        raise ValueError('invalid_deletion_kind')
    if body.get('decision') != 'failed' or body.get('targetConfirmation') != target:
        raise ValueError('deletion_release_confirmation_required')
    stored = {'ok': False, 'status': 'failed',
              'error': 'deletion_released_by_admin', 'released': True}
    security().resolve_deletion(kind, target, 'failed', stored)
    return {'status': 'failed', 'released': True}


def execute_deletion(body, principal, idempotency_key):
    kind = body.get('kind')
    target = body.get('target')
    if kind == 'waker':
        target = safe_id(target)
        revision = None
    elif kind == 'provider':
        target = text(target, 120)
        revision = require_revision(body.get('baseRevision'))
    else:
        raise ValueError('invalid_deletion_kind')
    acknowledged = body.get('acknowledgedWarnings', [])
    if not isinstance(acknowledged, list) or any(not isinstance(item, str) for item in acknowledged):
        raise ValueError('invalid_deletion_warnings')
    replay = security().replay_deletion(
        principal, idempotency_key, kind, target,
        body.get('impactDigest', ''), acknowledged)
    if replay:
        return dict(replay['result'], replay=True)
    with RESOURCE_LOCK:
        if kind == 'provider':
            current = provider_store().read()['revision']
            if not hmac.compare_digest(current, revision):
                raise AccessError(409, 'stale_deletion_preview')
        stored = security().deletion_preview(body.get('preview'), principal, kind, target)
        if kind == 'provider' and revision != stored['revision']:
            raise AccessError(409, 'stale_deletion_preview')
        fresh = deletion_scan(kind, target, revision or '')
        if kind == 'waker' and fresh['revision'] != stored['revision']:
            raise AccessError(409, 'stale_deletion_preview')
        supplied_digest = body.get('impactDigest')
        if (not isinstance(supplied_digest, str) or
                not fresh['deletable'] or
                not hmac.compare_digest(fresh['impactDigest'], stored['impactDigest']) or
                not hmac.compare_digest(fresh['impactDigest'], supplied_digest)):
            raise AccessError(409, 'stale_deletion_preview')
        operation = security().begin_deletion(
            body.get('preview'), principal, kind, target, fresh['impactDigest'],
            acknowledged, idempotency_key)
        if operation['replay']:
            return dict(operation['result'], replay=True)
        try:
            if kind == 'provider':
                changed = delete_provider(target, revision)
                result = {'ok': True, 'status': 'succeeded',
                          'providerRevision': changed['revision']}
            else:
                daemon_json('DELETE', '/api/agents/' + target)
                result = {'ok': True, 'status': 'succeeded'}
        except (DaemonConflict, SettingsConflict) as error:
            result = {'ok': False, 'status': 'failed', 'error': 'version_conflict'}
            security().finish_deletion(principal, idempotency_key, 'failed', result)
            raise
        except DaemonRequestError as error:
            if error.status == 202:
                result = {'ok': False, 'status': 'pending',
                          'error': 'deletion_operation_pending'}
                security().finish_deletion(principal, idempotency_key, 'pending', result)
                return result
            if 400 <= error.status < 500:
                result = {'ok': False, 'status': 'failed', 'error': error.code}
                security().finish_deletion(principal, idempotency_key, 'failed', result)
                raise
            result = {'ok': False, 'status': 'unknown', 'error': 'deletion_result_unknown'}
            security().finish_deletion(principal, idempotency_key, 'unknown', result)
            return result
        except Exception:
            result = {'ok': False, 'status': 'unknown', 'error': 'deletion_result_unknown'}
            security().finish_deletion(principal, idempotency_key, 'unknown', result)
            return result
        security().finish_deletion(principal, idempotency_key, 'succeeded', result)
        return result


def skills_list(wid):
    detail = waker_detail(safe_id(wid))
    return [{k: row.get(k) for k in ('skillId','name','description','enabled','source','mutableByAgent','pinned','currentVersionId')}
            for row in detail.get('skills') or [] if isinstance(row, dict)]


def skill_content(wid, skill):
    wid, skill = safe_id(wid), safe_id(skill)
    metadata = next((row for row in skills_list(wid) if row['skillId'] == skill), None)
    if metadata is None:
        raise DaemonRequestError(404, 'skill_not_found')
    try:
        data = object_data(daemon_json('GET', '/api/agents/%s/skills/%s/content' % (wid, skill)))
    except DaemonRequestError as error:
        if error.status != 404:
            raise
        return {'skill': metadata, 'content': '', 'editable': False, 'contentAvailable': False,
                'readOnlyReason': 'skill_content_unavailable'}
    row = dict(metadata)
    row.update(data.get('skill') if isinstance(data.get('skill'), dict) else {})
    editable = row.get('mutableByAgent') is True and row.get('pinned') is not True and bool(row.get('currentVersionId'))
    return {'skill': {k: row.get(k) for k in ('skillId','name','description','enabled','source','mutableByAgent','pinned','currentVersionId','updatedAt')},
            'content': data.get('content', '') if isinstance(data.get('content'), str) else '',
            'contentAvailable': isinstance(data.get('content'), str), 'editable': editable,
            'readOnlyReason': None if editable else 'skill_read_only'}


def require_editable_skill(wid, skill, base=None):
    current = skill_content(wid, skill)
    if not current['editable'] or not current['contentAvailable']:
        raise AccessError(403, 'skill_read_only')
    if base is not None and base != current['skill']['currentVersionId']:
        raise DaemonConflict('skill_version_changed')
    return current


def skill_versions(wid, skill):
    data = daemon_json('GET', '/api/agents/%s/skills/%s/versions' % (safe_id(wid), safe_id(skill))).get('data') or {}
    rows = data if isinstance(data, list) else data.get('items', data.get('versions', []))
    return [{k:r.get(k) for k in ('versionId','createdAt','updatedAt','reason','source')} for r in rows if isinstance(r, dict)]


def skill_diff(wid, skill, version):
    data = object_data(daemon_json('GET', '/api/agents/%s/skills/%s/versions/%s/diff' %
                                   (safe_id(wid), safe_id(skill), quote(opaque_id(version), safe=''))))
    # Diff schemas vary by daemon build; recursively retain JSON data but cap serialized size.
    encoded = json.dumps(data, ensure_ascii=False)
    if len(encoded) > 256 * 1024:
        raise ValueError('skill_diff_too_large')
    return data


def automations(page=1):
    page = int(page)
    if not 1 <= page <= 100000:
        raise ValueError('invalid_page')
    data = object_data(daemon_json('GET', '/api/triggers?' + urlencode({'page':page,'pageSize':20})))
    rows=[]
    for r in data.get('items',[]):
        if isinstance(r,dict):
            rows.append({k:r.get(k) for k in ('id','triggerId','triggerName','taskDescription','model','enabled','maxRuns','endDate','location','executionTarget')})
    return {'items':rows,'pagination':data.get('pagination',{})}


def automation_runs(trigger, page=1):
    page=int(page)
    if not 1 <= page <= 100000:raise ValueError('invalid_page')
    data=object_data(daemon_json('GET','/api/triggers/%s/runs?%s' % (safe_id(trigger),urlencode({'page':page,'pageSize':20}))))
    rows=data.get('items',[])
    return {'items':[{k:r.get(k) for k in ('runId','triggerId','startedAt','finishedAt','status','sessionId','runSequence','location')} for r in rows if isinstance(r,dict)],'pagination':data.get('pagination',{})}


def pending_pairings(limit=50, offset=0):
    limit,offset=int(limit),int(offset)
    if not 1 <= limit <= 100 or not 0 <= offset <= 100000:raise ValueError('invalid_pagination')
    data=object_data(daemon_json('GET','/api/channels/pairing/pending?'+urlencode({'limit':limit,'offset':offset,'includeTotal':'true'})))
    keys=('pendingId','pendingRevision','remoteExpiresAt','channelId','robotId','bindingKey','conversationType','senderId','senderStaffId','conversationId','conversationName','subjectName','displayName','createdAt','updatedAt','corpId','corpName')
    return {'items':[{k:r.get(k) for k in keys} for r in data.get('items',[]) if isinstance(r,dict)],'total':data.get('total',0)}


def pairing_approval(body):
    pending_id = opaque_id(body.get('pendingId'))
    revision = body.get('pendingRevision')
    if revision is None or isinstance(revision, bool) or not isinstance(revision, (str, int)):
        raise ValueError('pairing_revision_required')
    offset = int(body.get('pendingOffset', 0))
    rows = pending_pairings(100, offset)['items']
    current = next((row for row in rows if row.get('pendingId') == pending_id), None)
    if current is None or current.get('pendingRevision') != revision:
        raise DaemonConflict('pairing_request_changed')
    required = ('channelId','senderId','conversationId','bindingKey','conversationType')
    if any(body.get(key) != current.get(key) for key in required):
        raise DaemonConflict('pairing_request_changed')
    expires = current.get('remoteExpiresAt')
    try:
        expiry = dt.datetime.fromisoformat(str(expires).replace('Z', '+00:00'))
        if expiry.tzinfo is None or expiry.timestamp() <= time.time():
            raise ValueError()
    except (ValueError, TypeError, OverflowError):
        raise DaemonConflict('pairing_request_expired_or_unverified')
    wid = safe_id(body.get('wakerId'))
    waker_detail(wid)
    payload = {key:text(current.get(key),500) for key in required}
    payload.update({'senderStaffId':text(current.get('senderStaffId') or '',500,True),
                    'conversationName':text(current.get('conversationName') or '',500,True),
                    'senderName':text(current.get('displayName') or current.get('subjectName') or '',200,True),
                    'targets':[{'targetKind':'waker','targetId':wid,'enabled':True}],
                    'model':'auto','workspace':None,'intermediateReplyMode':'off',
                    'allowQoderwakeCommands':False,'allowCustomModel':False})
    return payload


def plugin_id(value):
    value = text(value, 256)
    if any(ord(c) < 32 for c in value) or '/' in value or '\\' in value or value in ('.', '..'):
        raise ValueError('invalid_plugin_id')
    return quote(value, safe='')


def plugin_catalog(keyword='', page='1'):
    page = int(page)
    if not 1 <= page <= 100000:
        raise ValueError('invalid_page')
    params = urlencode({'keyword': text(keyword, 120, True), 'page': page, 'pageSize': 20, 'language': 'zh-CN'})
    return daemon_json('GET', '/api/plugin-market?' + params).get('data') or {}


CHANNEL_TYPES = {'feishu'}
CHANNEL_POLICIES = {'paired', 'open'}


def channel_value(value, limit=200, required=True):
    return text(value, limit, not required)


def channel_public(row):
    if isinstance(row.get('channel'), dict):
        row = row['channel']
    source = row.get('config', row) if isinstance(row.get('config'), dict) else row
    result = {k: source.get(k) for k in ('id', 'channelId', 'type', 'name', 'botName', 'status', 'enabled',
                                          'appId', 'agentId', 'bindingTarget', 'accessPolicy', 'artifactDeliveryEnabled')}
    for key in ('id', 'channelId', 'status', 'agentId'):
        if result.get(key) is None:
            result[key] = row.get(key)
    return result


def channels_state():
    value = daemon_json('GET', '/api/channels').get('data') or []
    rows = value if isinstance(value, list) else value.get('items', value.get('channels', []))
    # Channel config contains platform secrets; return only a display allowlist.
    return [channel_public(row) for row in rows if isinstance(row, dict)]


def channel_config(body, existing=None):
    existing = existing or {}
    kind = body.get('type')
    if kind not in CHANNEL_TYPES:
        raise ValueError('unsupported_channel_type')
    policy = body.get('accessPolicy', 'paired')
    if policy not in CHANNEL_POLICIES:
        raise ValueError('invalid_channel_policy')
    enabled = body.get('enabled', existing.get('enabled', False))
    delivery = body.get('artifactDeliveryEnabled', False)
    if not isinstance(enabled, bool) or not isinstance(delivery, bool):
        raise ValueError('invalid_channel_config')
    app_id = channel_value(body.get('appId', ''), 300, False) or existing.get('appId', '')
    if not app_id:
        raise ValueError('channel_app_id_required')
    incoming_secret = channel_value(body.get('appSecret', ''), 2000, False)
    if incoming_secret:
        secret = incoming_secret
    elif existing:
        secret = '***'
    else:
        raise ValueError('channel_secret_required')
    old_target = existing.get('bindingTarget')
    if isinstance(old_target, dict):
        old_target = old_target.get('targetId')
    target = body.get('bindingTarget') or body.get('agentId') or old_target
    if policy == 'open':
        target = safe_id(target)
    elif target:
        target = safe_id(target)
    config = dict(existing)
    config.update({'type': kind, 'enabled': enabled, 'appId': app_id, 'appSecret': secret,
                   'botName': channel_value(body.get('botName', existing.get('botName','')), 100, False),
                   'accessPolicy': policy, 'artifactDeliveryEnabled': delivery})
    for key, default in (('workspacePath',''),('mode','conversation'),('workflowId',''),('workflowName','')):
        if key not in config:
            config[key] = default
    if policy == 'open':
        config['bindingTarget'] = {'targetKind': 'waker', 'targetId': target,
                                   'displayName': None, 'enabled': True}
        model = body.get('model', existing.get('model', 'auto'))
        config['model'] = text(model or 'auto', 300)
        workspace = body.get('workspace', existing.get('workspace'))
        if workspace is not None and not isinstance(workspace, dict):
            raise ValueError('invalid_channel_workspace')
        config['workspace'] = workspace
    else:
        config['bindingTarget'] = None
        config['model'] = None
        config['workspace'] = None
    return config


def channel_detail(cid):
    row = daemon_json('GET', '/api/channels/' + safe_id(cid)).get('data') or {}
    if not isinstance(row, dict):
        raise ValueError('invalid_channel_response')
    return channel_public(row)


def write_channel(cid, body):
    current = {}
    if cid:
        raw = daemon_json('GET', '/api/channels/' + safe_id(cid)).get('data') or {}
        if isinstance(raw, dict):
            channel = raw.get('channel', raw)
            current = channel.get('config', channel) if isinstance(channel, dict) else {}
    else:
        alphabet = '0123456789abcdefghijklmnopqrstuvwxyz'
        code = ''.join(alphabet[b % len(alphabet)] for b in os.urandom(6))
        cid = 'global-' + code + '-' + body.get('type', '')
    daemon_json('PUT', '/api/channels/' + safe_id(cid) + '/config', channel_config(body, current))
    return cid


UPLINK_SWITCHES = {
    'sessionProjectionUplink': 'QODERWAKE_SESSION_PROJECTION_UPLINK',
    'remoteExecutionUplink': 'QODERWAKE_REMOTE_EXECUTION_UPLINK'
}

SDK_BYOK_SWITCH = 'QODER_SDK_CUSTOM_BASE_URL_BYOK'
SDK_BYOK_DEFAULT = True


def runtime_policy():
    path = ROOT / 'runtime-policy.json'
    default = {'hotDeploy': False, 'embeddingDisabled': True}
    if not path.exists() and not path.is_symlink():
        return default
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NONBLOCK | getattr(os, 'O_NOFOLLOW', 0))
        with os.fdopen(fd, 'rb') as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size > 4096:
                raise ValueError('invalid_runtime_policy')
            raw = stream.read(4097)
        if len(raw) > 4096:
            raise ValueError('invalid_runtime_policy')
        value = json.loads(raw)
    except (OSError, ValueError):
        raise ValueError('invalid_runtime_policy')
    if (not isinstance(value, dict) or set(value) - (set(default) | set(UPLINK_SWITCHES) | {'sdkByok'}) or
            any(not isinstance(value.get(key), bool) for key in default) or
            any(key in value and value[key] is not None and not isinstance(value[key], bool)
                for key in UPLINK_SWITCHES) or
            ('sdkByok' in value and not isinstance(value['sdkByok'], bool))):
        raise ValueError('invalid_runtime_policy')
    return value


def switch_value(value):
    if value is None:
        return None
    if isinstance(value, bytes):
        value = value.decode('ascii', errors='replace')
    normalized = value.strip().lower()
    if normalized in ('0', 'false', 'off'):
        return False
    if normalized in ('1', 'true', 'on'):
        return True
    raise ValueError('invalid_runtime_switch')


def runtime_env():
    policy = runtime_policy()
    env = dict(os.environ)
    env['QODERWAKE_HOT_DEPLOY'] = '1' if policy['hotDeploy'] else '0'
    env['QODER_MEMORY_DISABLE_EMBEDDING'] = '1' if policy['embeddingDisabled'] else '0'
    env[SDK_BYOK_SWITCH] = '1' if policy.get('sdkByok', SDK_BYOK_DEFAULT) else '0'
    for key, variable in UPLINK_SWITCHES.items():
        if policy.get(key) is not None:
            env[variable] = '1' if policy[key] else '0'
    return env


def read_daemon_environ(pid):
    proc = Path('/proc') / str(pid) / 'environ'
    return dict(v.split(b'=', 1) for v in proc.read_bytes().split(b'\0') if b'=' in v)


def runtime_state():
    policy = runtime_policy()
    effective = dict(policy)
    effective['sdkByok'] = policy.get('sdkByok', SDK_BYOK_DEFAULT)
    environment = runtime_env()
    for key, variable in UPLINK_SWITCHES.items():
        effective[key] = switch_value(environment.get(variable))
    observed, uplinks = [], {}
    state = daemon_process_state()
    if state:
        try:
            env = read_daemon_environ(state['pid'])
            row = {
                'hotDeploy': switch_value(env.get(b'QODERWAKE_HOT_DEPLOY')),
                'embeddingDisabled': switch_value(env.get(b'QODER_MEMORY_DISABLE_EMBEDDING')),
                'sdkByok': switch_value(env.get(SDK_BYOK_SWITCH.encode()))
            }
            uplinks = {key: switch_value(env.get(variable.encode())) for key, variable in UPLINK_SWITCHES.items()}
            if daemon_process_state() == state:
                observed.append(row)
            else:
                uplinks = {}
        except (OSError, ValueError):
            uplinks = {}
    known = bool(observed) and all(observed[0][key] is not None for key in ('hotDeploy', 'embeddingDisabled'))
    pending = any(observed[0][key] != policy[key] for key in ('hotDeploy', 'embeddingDisabled')) if known else None
    if known:
        pending = pending or any(effective[key] is not None and uplinks.get(key) != effective[key] for key in UPLINK_SWITCHES)
    sdk_pending = bool(observed) and (observed[0]['sdkByok'] is True) != effective['sdkByok']
    return {'desired': policy, 'observed': observed, 'observedUplink': uplinks,
            'effectiveOnPanelRestart': effective, 'pendingRestart': pending, 'sdkByokPending': sdk_pending,
            'observation': 'verified' if known else 'unknown',
            'restartAllowed': bool(state and observed),
            'restartBlocker': None if state and observed else 'daemon_identity_unverified',
            'note': '未知不等于待重启。未受管或身份不完整时禁止自动重启，请先完成独立迁管。上行开关仅控制对应官方功能，关闭可能影响会话同步和远程执行反馈，不代表零上行；继承保留启动环境，未配置时使用官方默认。已关闭的上行项重新启用需独立停止再启动。BYOK 通道关闭后所有自有 Provider 不可用；进程未携带开关时按官方默认（关闭）判定。'}


def plugins_state():
    feat, inst = {}, []
    try:
        feat = daemon_json('GET', '/api/plugin-market/featured?limit=12').get('data') or {}
    except Exception:
        pass
    try:
        inst = daemon_json('GET', '/api/plugin-market/installed').get('data') or {}
    except Exception:
        pass
    return {'featured': feat, 'installed': inst}


GWC = Path(os.environ.get('QW_GW_CONFIG') or ROOT / 'config/uplink-gw.json')
GWLOG = ROOT / 'logs' / 'uplink-gw.jsonl'
GW_PORT = int(os.environ.get('QW_GW_PORT') or 19840)


def gw_cfg():
    if not GWC.exists() or GWC.is_symlink():
        return {'mode': 'unconfigured', 'token_policy': 'balanced', 'sink_post_prefixes': [], 'rules': [], 'audit_paths': []}
    return validate_cfg(json.loads(GWC.read_text()))


def gateway_active():
    store = ROOT / 'gateway-runtime'
    state_file = store / 'current.json'
    try:
        record = load_state(
            state_file, store, ROOT, GW_PORT, missing_ok=True)
    except Exception:
        return {'managed': True, 'healthy': False,
                'note': 'state 不可完整读取，禁止面板自动重启'}
    if record is None:
        return {'managed': False, 'healthy': False, 'note': '旧网关尚未迁入事务管理，禁止面板自动重启'}
    try:
        verify_generation_files(record['generation'])
        if (identity_status(record) != 'match' or
                gateway_port_status(record) != 'owned'):
            raise ValueError('gateway_process_identity_unknown')
        opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}), NoRedirect())
        with opener.open(
                'http://127.0.0.1:%d/__qwp_gateway_health' % GW_PORT,
                timeout=2) as response:
            raw = response.read(65537)
        if len(raw) > 65536:
            raise ValueError('gateway_health_invalid')
        active = json.loads(raw)
        generation, process = record['generation'], record['process']
        healthy = (
            isinstance(active, dict) and set(active) == {
                'schemaVersion', 'service', 'pid', 'nonce', 'sourceHash',
                'policyHash', 'runtimeHash', 'configHash', 'mode', 'port', 'root',
                'upstream', 'configPath'
            } and active.get('schemaVersion') == STATE_VERSION and
            active.get('service') == 'qw-control-gateway' and
            active.get('pid') == process['pid'] and
            active.get('nonce') == process['nonce'] and
            active.get('sourceHash') == generation['sourceHash'] and
            active.get('policyHash') == generation['policyHash'] and
            active.get('runtimeHash') == generation['runtimeHash'] and
            active.get('configHash') == generation['configHash'] and
            active.get('mode') == generation['mode'] and
            active.get('port') == generation['port'] and
            active.get('root') == generation['root'] and
            active.get('upstream') == generation['upstream'] and
            active.get('configPath') == process['configPath'] and
            identity_status(record) == 'match' and
            gateway_port_status(record) == 'owned')
        return {
            'managed': True, 'healthy': healthy,
            'mode': generation['mode'] if healthy else None,
            'configHash': generation['configHash'] if healthy else None,
            'rollbackAvailable': bool(record.get('previous'))
        }
    except Exception:
        return {'managed': True, 'healthy': False,
                'note': 'state、代际文件、进程身份或本地健康响应无法同时核对'}


def net_state():
    active = gateway_active()
    alive = bool(active.get('healthy'))
    up, gets, total, up_bytes = {}, {}, 0, 0
    window = {'bytesRead': 0, 'fileBytes': 0, 'sources': 0, 'truncated': False,
              'maxBytes': 1024 * 1024, 'maxLines': 5000, 'available': True}
    try:
        managed_log = GWLOG.parent / 'gateway' / GWLOG.name
        if managed_log.exists() or managed_log.is_symlink():
            paths = [managed_log] + [managed_log.with_name(managed_log.name + '.' + str(i)) for i in range(1, 4)]
        else:
            paths = [GWLOG]
        result = tail_records(paths)
        lines = result.pop('lines')
        window.update(result)
        for line in lines:
            try:
                r = json.loads(line)
                if (not isinstance(r, dict) or not isinstance(r.get('path'), str) or
                        not isinstance(r.get('method'), str) or
                        type(r.get('q', 0)) is not int or r.get('q', 0) < 0 or
                        type(r.get('status')) is not int):
                    continue
                total += 1
                p5 = '/'.join(r['path'].split('/')[:5])
                if r['method'] in ('GET', 'HEAD'):
                    gets[p5] = gets.get(p5, 0) + 1
                else:
                    a = up.setdefault(p5, [0, 0, 0])
                    a[0] += 1
                    a[1] += r.get('q', 0)
                    a[2] = r['status']
                    up_bytes += r.get('q', 0)
            except (ValueError, TypeError):
                continue
    except (OSError, ValueError):
        window['available'] = False
    residual = {}
    daemon_state = daemon_process_state()
    pid = str(daemon_state['pid']) if daemon_state else None
    if pid:
        ss = subprocess.run(['ss', '-tnp'], capture_output=True, text=True).stdout
        for line in ss.splitlines():
            if 'pid=%s,' % pid in line:
                dst = line.split()[4]
                ip = dst.rsplit(':', 1)[0]
                if ip.startswith('127.'):
                    continue
                residual[ip] = residual.get(ip, 0) + 1
    cfg = gw_cfg()
    active['matchesSaved'] = bool(active.get('healthy') and cfg['mode'] != 'unconfigured' and
                                  active.get('configHash') == policy_hash(cfg))
    return {'gw_alive': alive, 'gw_port': GW_PORT, 'cfg': cfg, 'active': active,
            'fw': fw_state(),
            'total': total, 'up_bytes': up_bytes, 'logWindow': window,
            'uplink': [{'path': k, 'n': v[0], 'bytes': v[1], 'last_status': v[2]}
                       for k, v in sorted(up.items(), key=lambda x: -x[1][1])],
            'poll': sorted(gets.items(), key=lambda x: -x[1])[:8],
            'residual': sorted(residual.items(), key=lambda x: -x[1])}


def gw_restart(candidate=None, action='apply'):
    launcher = None
    for c in [Path(__file__).resolve().parent / 'uplink-gw-launch.sh',
              Path(__file__).resolve().parent.parent / 'ops' / 'uplink-gw-launch.sh',
              ROOT / 'staging' / 'uplink-gw-launch.sh']:
        if c.exists():
            launcher = c
            break
    if not launcher:
        return False, '未找到 uplink-gw-launch.sh'
    env = dict(os.environ, QW_ROOT=str(ROOT), QW_GW_CONFIG=str(GWC))
    if candidate:
        env['QW_GW_CANDIDATE'] = str(candidate)
    if action not in ('apply', 'preflight', 'rollback'):
        raise ValueError('invalid_gateway_action')
    r = subprocess.run(['bash', str(launcher), action], capture_output=True, text=True,
                       env=env, timeout=70)
    try:
        result = json.loads(r.stdout)
    except ValueError:
        return False, '网关管理器未返回有效结果，未确认变更成功'
    return r.returncode == 0 and result.get('ok') is True, result.get('error') or ('网关配置已核验' if action == 'preflight' else '网关已切换并通过本地健康核验')


def validate_net_config(body):
    cfg = gw_cfg()
    if cfg['mode'] == 'unconfigured':
        cfg = {'mode':'observe','token_policy':'balanced','sink_post_prefixes':[],
               'token_allow_prefixes':[],'rules':[],'audit_paths':[]}
    if body.get('mode') in ('observe', 'enforce'):
        cfg['mode'] = body['mode']
    if body.get('token_policy') == 'strict':
        raise ValueError('strict_not_validated')
    if body.get('token_policy') == 'balanced':
        cfg['token_policy'] = body['token_policy']
    if isinstance(body.get('sink_post_prefixes'), list):
        paths = body['sink_post_prefixes']
        existing = cfg.get('sink_post_prefixes') or []
        allowed = set(existing) | {'/algo/api/v1/tracking'}
        if any(p not in allowed for p in paths):
            raise ValueError('unvalidated_uplink_rule')
        cfg['sink_post_prefixes'] = paths
    if body.get('mode') == 'strict':
        raise ValueError('strict_requires_offline_policy_validation')
    return validate_cfg(cfg)


def apply_net_config(cfg):
    ROOT.mkdir(parents=True, exist_ok=True)
    fd, path = tempfile.mkstemp(prefix='.gateway-candidate-', suffix='.json', dir=ROOT)
    candidate = Path(path)
    try:
        with os.fdopen(fd, 'w') as stream:
            json.dump(cfg, stream)
        return gw_restart(candidate)
    finally:
        candidate.unlink(missing_ok=True)


def net_config(body):
    return apply_net_config(validate_net_config(body))


def fw_script():
    for c in [Path(__file__).resolve().parent / 'uplink-firewall.sh',
              Path(__file__).resolve().parent.parent / 'ops' / 'uplink-firewall.sh',
              ROOT / 'staging' / 'uplink-firewall.sh']:
        if c.exists():
            return c
    return None


def fw_state():
    s = fw_script()
    if not s:
        return {'available': False, 'active_rules': 0}
    try:
        r = subprocess.run(['bash', str(s), 'status'], capture_output=True, text=True,
                           env=dict(os.environ, QW_ROOT=str(ROOT)), timeout=15)
        m = re.search(r'active_rules=(\d+)', r.stdout)
        return {'available': True, 'active_rules': int(m.group(1)) if m else 0,
                'detail': r.stdout.strip()[-200:]}
    except Exception as e:
        return {'available': True, 'active_rules': -1, 'detail': str(e)[:120]}


def fw_action(action):
    s = fw_script()
    if not s or action not in ('add', 'remove'):
        return False, 'uplink-firewall.sh 不可用或动作非法'
    r = subprocess.run(['bash', str(s), action], capture_output=True, text=True,
                       env=dict(os.environ, QW_ROOT=str(ROOT)), timeout=25)
    return r.returncode == 0, (r.stdout or r.stderr).strip()[-160:]


STATE_CACHE = {'at': 0, 'data': None, 'epoch': 0}
STATE_LOCK = threading.Lock()
STATE_REFRESH_LOCK = threading.Lock()


def invalidate_state():
    with STATE_LOCK:
        STATE_CACHE['epoch'] += 1
        STATE_CACHE['at'] = 0


def cached_state():
    with STATE_LOCK:
        if STATE_CACHE['data'] is not None and time.monotonic() - STATE_CACHE['at'] <= 15:
            return dict(STATE_CACHE['data'])
    with STATE_REFRESH_LOCK:
        for _ in range(2):
            with STATE_LOCK:
                if STATE_CACHE['data'] is not None and time.monotonic() - STATE_CACHE['at'] <= 15:
                    return dict(STATE_CACHE['data'])
                epoch = STATE_CACHE['epoch']
            data = api_state()
            with STATE_LOCK:
                if epoch == STATE_CACHE['epoch']:
                    STATE_CACHE.update(data=data, at=time.monotonic())
                    return dict(data)
    raise AccessError(503, 'state_changed_during_refresh')


def panel_state(role):
    d = cached_state()
    d['version'] = VERSION
    if role == 'admin':
        d['providers'], d['providerRevision'] = provider_store().summaries()
        d['legacyProviderBackups'] = provider_store().legacy_backups()
        d['pluginWritesEnabled'] = PLUGIN_WRITES_ENABLED
        d['daemonManaged'] = daemon_process_state() is not None
    else:
        d['providers'], d['providerRevision'], d['legacyProviderBackups'] = [], None, None
        d['pluginWritesEnabled'] = False
        d['whoami'] = None
    try:
        cfg = json.loads((HOME / 'config/config.json').read_text())
        d['telemetry'] = cfg.get('telemetry') if isinstance(cfg.get('telemetry'), bool) else None
    except (OSError, ValueError):
        d['telemetry'] = None
    runtime = HOME / 'runtime-generations'
    d['guard'] = not bool(runtime.stat().st_mode & 0o222) if runtime.is_dir() else None
    d['guardNote'] = '只检查目录写权限；root、手动升级与其他更新路径仍可绕过，未证明完全锁版。'
    d['whitelist'] = [{'ip': k, 'label': v} for k, v in WL.items()] if role == 'admin' else []
    return d


def patches():
    global PATCHES
    import shutil
    with LOCK:
        if PATCHES is None:
            registry = ROOT / 'patch-registry.json'
            if not registry.exists():
                registry = CFG / 'patch-registry.json'
            PATCHES = PatchManager(
                ROOT, shutil.which(DAEMON_BIN) or DAEMON_BIN,
                os.environ.get('QW_PATCH_REGISTRY') or registry,
                GW_PORT)
    return PATCHES


def patch_state():
    return patches().status()


DEPENDENCY_WRITE_ROUTES = {
    '/api/callers/create', '/api/preference', '/api/provider',
    '/api/waker', '/api/waker/update', '/api/chat/new', '/api/chat/send', '/api/gw',
    '/api/channels/config', '/api/channels/action', '/api/pairing/approve',
    '/api/automation/run', '/api/automation/toggle',
    '/api/plugins/install', '/api/plugins/remove', '/api/plugins/toggle',
    '/api/skill/update', '/api/skill/rollback'
}
RESOURCE_ROUTES = DEPENDENCY_WRITE_ROUTES | {
    '/api/waker/delete', '/api/provider/delete',
    '/api/deletion/reconcile', '/api/deletion/release'
}
VIEWER_ROUTES = {'/api/state', '/api/usage', '/api/plugins'}
CONFIRM_ROUTES = {'/api/provider/delete', '/api/test', '/api/test/saved', '/api/waker/delete', '/api/deletion/release', '/api/maintenance/enter', '/api/backup/delete', '/api/apply', '/api/telemetry', '/api/net/config', '/api/net/firewall', '/api/callers/create', '/api/callers/revoke', '/api/patch/execute', '/api/plugins/install', '/api/plugins/remove', '/api/plugins/toggle', '/api/channels/action', '/api/channels/config', '/api/channels/delete', '/api/runtime/policy', '/api/runtime/apply', '/api/session/delete', '/api/skill/update', '/api/skill/rollback', '/api/automation/toggle', '/api/automation/run', '/api/automation/delete', '/api/pairing/approve'}
SPEND_ROUTES = {'/api/gw', '/api/chat/send', '/api/chat/new', '/api/automation/run'}
MAINTENANCE_CONTROL_ROUTES = {'/api/maintenance/enter', '/api/maintenance/exit'}
POST_ROUTES = CONFIRM_ROUTES | SPEND_ROUTES | MAINTENANCE_CONTROL_ROUTES | {'/api/session/read', '/api/patch/plan', '/api/provider', '/api/provider/probe-plan', '/api/deletion/preview', '/api/deletion/status', '/api/deletion/reconcile', '/api/deletion/release', '/api/preference', '/api/backup', '/api/waker', '/api/waker/update'}


class H(BaseHTTPRequestHandler):
    server_version = 'QoderWakePanel/' + VERSION

    def setup(self):
        super().setup()
        self.connection.settimeout(15)
        self.identity = None
        self.audit_action = None
        self.audit_target = ''

    def out(self, code, body, ctype='application/json; charset=utf-8', headers=None):
        if not isinstance(body, (str, bytes)):
            body = json.dumps(body, ensure_ascii=False)
        data = body.encode() if isinstance(body, str) else body
        if self.audit_action:
            ok = code < 400
            try:
                ok = ok and json.loads(data).get('ok', True)
            except (ValueError, AttributeError):
                pass
            security().audit(self.client_address[0], self.identity, self.audit_action, self.audit_target, code, ok)
            self.audit_action = None
        self.send_response(code)
        self.send_header('Content-Type', ctype)
        self.send_header('Content-Length', str(len(data)))
        self.send_header('Cache-Control', 'no-store')
        self.send_header('Referrer-Policy', 'no-referrer')
        self.send_header('X-Content-Type-Options', 'nosniff')
        self.send_header('X-Frame-Options', 'DENY')
        self.send_header('Content-Security-Policy', "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
        for k, v in headers or []:
            self.send_header(k, v)
        self.end_headers()
        try:
            self.wfile.write(data)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def check_transport(self):
        peer = ipaddress.ip_address(self.client_address[0])
        cidrs = os.environ.get('QW_ALLOWED_CLIENTS', '').strip()
        if cidrs and not any(peer in ipaddress.ip_network(c.strip(), strict=False) for c in cidrs.split(',')):
            raise AccessError(403, 'client_network_rejected')
        if os.environ.get('QW_REQUIRE_TLS') == '1' and not peer.is_loopback and not isinstance(self.connection, ssl.SSLSocket):
            raise AccessError(403, 'secure_transport_required')

    def origin_ok(self):
        origin = self.headers.get('Origin', '')
        expected = PUBLIC_ORIGIN or ('https://' if isinstance(self.connection, ssl.SSLSocket) else 'http://') + self.headers.get('Host', '')
        return bool(origin) and origin == expected

    def body(self):
        if self.headers.get('Transfer-Encoding'):
            raise AccessError(400, 'unsupported_encoding')
        if self.headers.get('Content-Type', '').split(';')[0].strip().lower() != 'application/json':
            raise AccessError(415, 'json_required')
        try:
            n = int(self.headers.get('Content-Length', '0'))
        except ValueError:
            raise AccessError(400, 'invalid_length')
        if not 0 <= n <= 65536:
            raise AccessError(413, 'body_too_large')
        try:
            d = json.loads(self.rfile.read(n) or b'{}')
        except (ValueError, UnicodeDecodeError):
            raise AccessError(400, 'invalid_json')
        if not isinstance(d, dict):
            raise AccessError(400, 'object_required')
        return d

    def do_GET(self):
        from urllib.parse import parse_qs
        u = urlsplit(self.path)
        if u.path in ('/', '/static/panel.css', '/static/panel.js', '/static/management.js', '/static/advanced.js'):
            name = 'index.html' if u.path == '/' else Path(u.path).name
            mime = {'index.html': 'text/html; charset=utf-8', 'panel.css': 'text/css; charset=utf-8', 'panel.js': 'text/javascript; charset=utf-8', 'management.js': 'text/javascript; charset=utf-8', 'advanced.js': 'text/javascript; charset=utf-8'}[name]
            return self.out(200, (ASSETS / name).read_bytes(), mime)
        if u.path == '/api/health':
            return self.out(200, {'ok': True, 'version': VERSION})
        try:
            self.check_transport()
            self.identity = security().authenticate(self.headers)
            role = self.identity['role']
            if u.path == '/api/whoami':
                return self.out(200, {'ok': True, 'level': role, 'csrf': self.identity['csrf'], 'version': VERSION,
                                      'transportSecure': isinstance(self.connection, ssl.SSLSocket)})
            if role == 'caller':
                raise AccessError(403, 'caller_route_rejected')
            if role == 'viewer' and u.path not in VIEWER_ROUTES:
                raise AccessError(403, 'admin_required')
            q = parse_qs(u.query)
            arg = lambda key: (q.get(key) or [''])[0]
            if u.path == '/api/state':
                d = panel_state(role)
            elif u.path == '/api/usage':
                d = usage_data()
            elif u.path == '/api/system/status':
                d = system_status()
            elif u.path == '/api/sessions/query':
                d = console_sessions(arg('waker'), arg('offset') or 0, arg('limit') or 30, arg('origin'))
            elif u.path == '/api/session/artifacts':
                d = session_artifacts(arg('session'))
            elif u.path == '/api/session/artifact-file':
                data, mime = artifact_file(arg('session'), arg('artifact'))
                return self.out(200, data, mime, [('Content-Disposition', 'attachment; filename="artifact.bin"')])
            elif u.path == '/api/skills':
                d = skills_list(arg('waker'))
            elif u.path == '/api/skill/content':
                d = skill_content(arg('waker'), arg('skill'))
            elif u.path == '/api/skill/versions':
                d = skill_versions(arg('waker'), arg('skill'))
            elif u.path == '/api/skill/diff':
                d = skill_diff(arg('waker'), arg('skill'), arg('version'))
            elif u.path == '/api/automations':
                d = automations(arg('page') or 1)
            elif u.path == '/api/automation/runs':
                d = automation_runs(arg('id'), arg('page') or 1)
            elif u.path == '/api/pairings':
                d = pending_pairings(arg('limit') or 50, arg('offset') or 0)
            elif u.path == '/api/plugins':
                d = plugins_state()
            elif u.path == '/api/plugins/catalog':
                d = plugin_catalog(arg('keyword'), arg('page') or '1')
            elif u.path == '/api/plugins/detail':
                d = daemon_json('GET', '/api/plugin-market/' + plugin_id(arg('id')) + '/detail?language=zh-CN').get('data') or {}
            elif u.path == '/api/plugins/installations':
                d = daemon_json('GET', '/api/plugin-market/' + plugin_id(arg('id')) + '/installations').get('data') or {}
            elif u.path == '/api/plugins/installed':
                path = '/api/plugin-market/installed'
                if arg('waker'):
                    path += '?' + urlencode({'wakerId': safe_id(arg('waker'))})
                d = daemon_json('GET', path).get('data') or {}
            elif u.path == '/api/channels':
                d = channels_state()
            elif u.path == '/api/channels/detail':
                d = channel_detail(arg('id'))
            elif u.path == '/api/runtime':
                d = runtime_state()
            elif u.path == '/api/egress':
                d = egress_data()
            elif u.path == '/api/backups':
                d = backups_list()
            elif u.path == '/api/audit':
                d = security().recent()
            elif u.path == '/api/callers':
                d = security().callers()
            elif u.path == '/api/chat/sessions':
                d = chat_sessions(arg('waker'))
            elif u.path == '/api/chat/messages':
                d = chat_messages(arg('session'))
            elif u.path == '/api/waker/detail':
                d = waker_detail(arg('id'))
            elif u.path == '/api/net/state':
                d = net_state()
            elif u.path == '/api/patch/state':
                d = patch_state()
            elif u.path == '/api/maintenance':
                d = ADMISSION.status()
            else:
                return self.out(404, {'ok': False, 'error': 'not_found'})
            return self.out(200, d)
        except DaemonRequestError as e:
            return self.out(e.status, {'ok': False, 'error': e.code, 'daemon': e.detail})
        except AccessError as e:
            return self.out(e.status, {'ok': False, 'error': e.code})
        except ValueError as e:
            return self.out(400, {'ok': False, 'error': str(e) if re.fullmatch('[a-z_]+', str(e)) else 'invalid_request'})
        except Exception:
            return self.out(502, {'ok': False, 'error': 'backend_unavailable'})

    def do_POST(self):
        p = urlsplit(self.path).path
        self.audit_action = p if p in POST_ROUTES | {'/api/login', '/api/logout', '/api/migrate'} else 'unknown_route'
        lease = None
        resource_locked = False
        try:
            self.check_transport()
            if self.headers.get('Sec-Fetch-Site') == 'cross-site' or (self.headers.get('Origin') and not self.origin_ok()):
                raise AccessError(403, 'origin_rejected')
            if p in ('/api/login', '/api/migrate'):
                b = self.body()
                value = b.get('token')
                if p == '/api/migrate':
                    from http.cookies import SimpleCookie
                    if not self.origin_ok():
                        raise AccessError(403, 'origin_rejected')
                    cookies = SimpleCookie()
                    cookies.load(self.headers.get('Cookie', ''))
                    value = cookies['qwtk'].value if 'qwtk' in cookies else None
                role = security().role_for(value)
                bucket = ('login-success:' + role if role else
                          'login-failed:' + self.client_address[0])
                limit = 30 if role else 10
                if not security().limit(bucket, limit, 300):
                    raise AccessError(429, 'login_rate_limited')
                raw, identity = security().login(value)
                self.identity = {'role': identity['role'], 'principal': 'login:' + identity['role']}
                secure = '; Secure' if isinstance(self.connection, ssl.SSLSocket) else ''
                return self.out(200, {'ok': True, 'level': identity['role'], 'csrf': identity['csrf']}, headers=[
                    ('Set-Cookie', COOKIE + '=' + raw + '; Path=/; HttpOnly; SameSite=Strict; Max-Age=604800' + secure),
                    ('Set-Cookie', 'qwtk=; Path=/; HttpOnly; SameSite=Strict; Max-Age=0')])
            self.identity = security().authenticate(self.headers)
            if self.identity['source'] == 'cookie':
                if not self.origin_ok() or not hmac.compare_digest(self.headers.get('X-CSRF-Token', ''), self.identity['csrf']):
                    raise AccessError(403, 'csrf_rejected')
            if p == '/api/logout':
                self.body()
                security().logout(self.identity)
                return self.out(200, {'ok': True}, headers=[('Set-Cookie', COOKIE+'=; Path=/; HttpOnly; SameSite=Strict; Max-Age=0')])
            caller = self.identity['role'] == 'caller'
            if caller and p != '/api/gw':
                raise AccessError(403, 'caller_route_rejected')
            if self.identity['role'] not in ('admin', 'caller'):
                raise AccessError(403, 'admin_required')
            if p not in POST_ROUTES:
                raise AccessError(404, 'not_found')
            if p == '/api/maintenance/exit':
                lease = None
            else:
                lease = ADMISSION.acquire(
                    self.identity['principal'], p in SPEND_ROUTES)
            if p not in MAINTENANCE_CONTROL_ROUTES and not security().limit('writes:'+self.identity['role'], 120, 60):
                raise AccessError(429, 'rate_limited')
            b = self.body()
            if p == '/api/deletion/preview':
                target = b.get('target') or ''
                pattern = r'[A-Za-z0-9_.-]{1,253}'
            elif p.startswith('/api/provider') or p in ('/api/test', '/api/test/saved'):
                target = b.get('name') or urlsplit(b.get('baseUrl', '')).hostname or ''
                pattern = r'[A-Za-z0-9_.-]{1,253}'
            else:
                target = b.get('wakerId') or b.get('sessionId') or b.get('id') or ''
                pattern = r'[A-Za-z0-9_-]{1,100}'
            self.audit_target = target if isinstance(target, str) and re.fullmatch(pattern, target) else ''
            if p in CONFIRM_ROUTES and b.get('confirm') != p:
                raise AccessError(409, 'confirmation_required')
            if p in RESOURCE_ROUTES:
                resource_locked = RESOURCE_LOCK.acquire(blocking=False)
                if not resource_locked:
                    raise AccessError(409, 'resource_mutation_in_progress')
                if p in DEPENDENCY_WRITE_ROUTES and security().has_unresolved_deletions():
                    raise AccessError(409, 'deletion_operation_unresolved')
            if p in SPEND_ROUTES:
                if not security().limit('spend-ip:'+self.client_address[0]) or not security().limit('spend-role:'+self.identity['role']):
                    raise AccessError(429, 'rate_limited')
            if caller:
                wid = safe_id(b.get('wakerId'))
                text(b.get('message'), 16000)
                if not security().limit('spend-caller:' + self.identity['caller_id']):
                    raise AccessError(429, 'rate_limited')
                security().consume_caller(self.identity, wid)
            if p == '/api/maintenance/enter':
                reason = text(b.get('reason', '管理员手动维护'), 200)
                ttl = b.get('ttl', 300)
                if type(ttl) is not int:
                    raise ValueError('invalid_maintenance_request')
                result = {'ok': True, **enter_maintenance(
                    self.identity['principal'], reason, ttl, lease)}
            elif p == '/api/maintenance/exit':
                ADMISSION.exit()
                result = {'ok': True, **ADMISSION.status()}
            elif p == '/api/callers/create':
                allowed = {w['id'] for w in api_state()['wakers']}
                scopes = b.get('wakers')
                if not isinstance(scopes, list) or any(not isinstance(w, str) or w not in allowed for w in scopes):
                    raise ValueError('invalid_waker_scope')
                result = {'ok': True, **security().create_caller(b.get('name'), scopes, b.get('quota'), b.get('days'))}
            elif p == '/api/callers/revoke':
                security().revoke_caller(safe_id(b.get('id')))
                result = {'ok': True}
            elif p == '/api/patch/plan':
                result = {'ok': True, **patches().plan(b.get('action'), self.identity['principal'])}
            elif p == '/api/patch/execute':
                result = patches().execute(b.get('plan'), self.identity['principal'])
            elif p in ('/api/plugins/install', '/api/plugins/remove', '/api/plugins/toggle'):
                if not PLUGIN_WRITES_ENABLED:
                    raise AccessError(403, 'experimental_plugin_writes_disabled')
                path = '/api/plugin-market/' + plugin_id(b.get('pluginId')) + '/installations/' + safe_id(b.get('wakerId'))
                if p.endswith('/install'):
                    daemon_json('POST', path, {'expectedVersion': text(b.get('expectedVersion'), 128)})
                elif p.endswith('/remove'):
                    daemon_json('DELETE', path)
                else:
                    if not isinstance(b.get('enabled'), bool):
                        raise ValueError('invalid_enabled')
                    daemon_json('PATCH', path, {'enabled': b['enabled']})
                result = {'ok': True}
            elif p == '/api/channels/action':
                action = b.get('action')
                if action not in ('start', 'stop', 'restart'):
                    raise ValueError('invalid_channel_action')
                daemon_json('POST', '/api/channels/' + safe_id(b.get('id')) + '/' + action, {})
                result = {'ok': True}
            elif p == '/api/channels/config':
                cid = write_channel(b.get('id', ''), b)
                result = {'ok': True, 'id': cid, 'message': '通道配置已保存；请单独启动以建立平台连接'}
            elif p == '/api/channels/delete':
                daemon_json('DELETE', '/api/channels/' + safe_id(b.get('id')))
                result = {'ok': True}
            elif p == '/api/runtime/policy':
                if not isinstance(b.get('hotDeploy'), bool) or not isinstance(b.get('embeddingDisabled'), bool):
                    raise ValueError('invalid_runtime_policy')
                policy = runtime_policy()
                policy.update({k: b[k] for k in ('hotDeploy', 'embeddingDisabled')})
                for key in UPLINK_SWITCHES:
                    if key in b:
                        if b[key] is not None and not isinstance(b[key], bool):
                            raise ValueError('invalid_runtime_policy')
                        policy[key] = b[key]
                if 'sdkByok' in b:
                    if not isinstance(b['sdkByok'], bool):
                        raise ValueError('invalid_runtime_policy')
                    policy['sdkByok'] = b['sdkByok']
                atomic_json(ROOT / 'runtime-policy.json', policy)
                result = {'ok': True, 'message': '已保存；daemon 未重启，当前运行状态不变'}
            elif p == '/api/runtime/apply':
                ok, msg = maintenance_transaction(
                    self.identity['principal'], '应用 daemon 启动策略',
                    restart_daemon, lease)
                result = {'ok': ok, 'message': msg or ('已按启动策略重启 daemon' if ok else '重启失败')}
            elif p == '/api/session/read':
                sid=opaque_id(b.get('sessionId'));payload={}
                if b.get('lastSeq') is not None:
                    if type(b['lastSeq']) is not int or b['lastSeq'] < 0:raise ValueError('invalid_sequence')
                    payload['last_seq']=b['lastSeq']
                daemon_json('POST','/api/sessions/'+quote(sid, safe='')+'/read',payload);result={'ok':True}
            elif p == '/api/session/delete':
                sid=opaque_id(b.get('sessionId'));force=b.get('force',False)
                if not isinstance(force,bool):raise ValueError('invalid_force')
                daemon_json('DELETE','/api/sessions/'+quote(sid, safe='')+('?force=true' if force else ''));result={'ok':True}
            elif p == '/api/skill/update':
                wid=safe_id(b.get('wakerId'));skill=safe_id(b.get('skillId'));base=text(b.get('baseVersionId'),200)
                require_editable_skill(wid, skill, base)
                content = b.get('content')
                if not isinstance(content, str) or len(content.encode()) > 60000:
                    raise ValueError('invalid_skill_content')
                daemon_json('PUT','/api/agents/%s/skills/%s/content'%(wid,skill),{'content':content,'baseVersionId':base});result={'ok':True}
            elif p == '/api/skill/rollback':
                wid=safe_id(b.get('wakerId'));skill=safe_id(b.get('skillId'))
                require_editable_skill(wid, skill)
                version = text(b.get('versionId'), 200)
                if not any(row.get('versionId') == version for row in skill_versions(wid, skill)):
                    raise ValueError('skill_version_not_found')
                daemon_json('POST','/api/agents/%s/skills/%s/rollback'%(wid,skill),{'versionId':version,'reason':text(b.get('reason'),500)});result={'ok':True}
            elif p == '/api/automation/toggle':
                if not isinstance(b.get('enabled'), bool):
                    raise ValueError('invalid_enabled')
                daemon_json('PATCH', '/api/triggers/' + safe_id(b.get('id')), {'enabled': b['enabled']})
                result = {'ok': True}
            elif p == '/api/automation/run':
                payload={}
                if b.get('subTriggerId'):payload['subTriggerId']=safe_id(b['subTriggerId'])
                daemon_json('POST','/api/triggers/'+safe_id(b.get('id'))+'/run-now',payload);result={'ok':True,'message':'已提交执行；请查看运行历史，不要盲目重试'}
            elif p == '/api/automation/delete':
                daemon_json('DELETE','/api/triggers/'+safe_id(b.get('id')));result={'ok':True}
            elif p == '/api/pairing/approve':
                payload=pairing_approval(b)
                daemon_json('POST','/api/channels/pairing/approve',payload);result={'ok':True}
            elif p == '/api/provider':
                changed = write_provider(b.get('name'), b.get('baseUrl'), b.get('apiKey', ''), b.get('model'), b.get('display', ''), b.get('baseRevision'))
                result = {'ok': True, 'providerRevision': changed['revision']}
            elif p == '/api/provider/delete':
                result = execute_deletion(
                    dict(b, kind='provider', target=b.get('name')),
                    self.identity['principal'],
                    self.headers.get('Idempotency-Key', ''))
            elif p == '/api/deletion/preview':
                result = {'ok': True, **deletion_preview(b, self.identity['principal'])}
            elif p == '/api/deletion/status':
                result = deletion_operation_status(b, self.identity['principal'])
            elif p == '/api/deletion/reconcile':
                result = {'ok': True, **reconcile_deletion(b)}
            elif p == '/api/deletion/release':
                result = {'ok': True, **release_deletion(b)}
            elif p == '/api/provider/probe-plan':
                result = {'ok': True, **provider_probe_plan(b, self.identity['principal'])}
            elif p in ('/api/test', '/api/test/saved'):
                if not security().limit('provider-probe:' + self.identity['principal'], 10, 300):
                    raise AccessError(429, 'rate_limited')
                mode = 'saved' if p.endswith('/saved') else 'adhoc'
                ok, msg = execute_provider_probe(dict(b, mode=mode), self.identity['principal'])
                result = {'ok': ok, 'message': msg}
            elif p == '/api/preference':
                ok, msg = set_preference(safe_id(b.get('wakerId')), text(b.get('model'), 300))
                result = {'ok': ok, 'message': '已更新默认模型' if ok else '默认模型更新失败'}
            elif p == '/api/apply':
                ok, msg = maintenance_transaction(
                    self.identity['principal'], '应用 Provider 配置',
                    restart_daemon, lease)
                result = {'ok': ok, 'message': msg}
            elif p == '/api/telemetry':
                enabled = b.get('enabled')
                if not isinstance(enabled, bool):
                    raise ValueError('invalid_telemetry_request')
                r = cli('config', 'set', 'telemetry', 'true' if enabled else 'false', timeout=25)
                if r.returncode:
                    result = {'ok': False, 'message': (r.stdout + r.stderr).strip()[-160:] or 'daemon 配置写入失败'}
                else:
                    result = {'ok': True, 'telemetry': enabled,
                              'message': '已写入 daemon 配置：telemetry=' + ('true' if enabled else 'false')}
            elif p == '/api/backup':
                result = {'ok': True, 'name': do_backup()}
            elif p == '/api/backup/delete':
                ok, msg = del_backup(b.get('name'))
                result = {'ok': ok, 'message': msg}
            elif p == '/api/waker':
                name = text(b.get('name'), 100)
                d = daemon_json('POST', '/api/agents', {'name': name, 'title': name})
                wid = (d.get('data') or {}).get('agentId')
                result = {'ok': bool(wid), 'id': wid}
                if wid and b.get('model'):
                    result['preferenceApplied'] = set_preference(wid, text(b['model'], 300))[0]
            elif p == '/api/waker/update':
                wid = safe_id(b.get('id'))
                body = {'name': text(b.get('name'), 100), 'description': text(b.get('description', ''), 4000, True)}
                skills = b.get('skills', [])
                if not isinstance(skills, list) or len(skills) > 200:
                    raise ValueError('invalid_skills')
                body['skills'] = [{'skillId': safe_id(x.get('skillId')), 'enabled': x['enabled']} for x in skills if isinstance(x, dict) and isinstance(x.get('enabled'), bool)]
                if len(body['skills']) != len(skills):
                    raise ValueError('invalid_skills')
                with RESOURCE_LOCK:
                    if security().unresolved_deletion('waker', wid):
                        raise AccessError(409, 'deletion_operation_unresolved')
                    daemon_json('PUT', '/api/agents/'+wid, body)
                result = {'ok': True}
            elif p == '/api/waker/delete':
                result = execute_deletion(
                    dict(b, kind='waker', target=b.get('id')),
                    self.identity['principal'],
                    self.headers.get('Idempotency-Key', ''))
            elif p == '/api/chat/new':
                sid = chat_new(safe_id(b.get('wakerId')), text(b.get('title', '新会话'), 100))
                result = {'ok': bool(sid), 'id': sid}
            elif p == '/api/chat/send':
                result = {'ok': chat_send(safe_id(b.get('sessionId')), text(b.get('message'), 16000))}
            elif p == '/api/gw':
                ok, msg = gw_ask(safe_id(b.get('wakerId')), text(b.get('message'), 16000))
                result = {'ok': ok, 'message': msg}
            elif p == '/api/net/config':
                cfg = validate_net_config(b)
                ok, msg = maintenance_transaction(
                    self.identity['principal'], '切换受管网关配置',
                    lambda: apply_net_config(cfg), lease)
                result = {'ok': ok, 'message': msg}
            elif p == '/api/net/firewall':
                ok, msg = fw_action(b.get('action'))
                result = {'ok': ok, 'message': msg}
            invalidate_state()
            if result.get('status') in ('pending', 'unknown'):
                return self.out(202, result)
            return self.out(200 if result.get('ok') else 502, result)
        except DaemonRequestError as e:
            return self.out(e.status, {'ok': False, 'error': e.code, 'daemon': e.detail},
                            headers=[('Retry-After', '300')] if e.status == 429 else None)
        except AccessError as e:
            return self.out(e.status, {'ok': False, 'error': e.code}, headers=[('Retry-After', '300')] if e.status == 429 else None)
        except (DaemonConflict, SettingsConflict):
            return self.out(409, {'ok': False, 'error': 'version_conflict'})
        except (ValueError, TypeError) as e:
            return self.out(400, {'ok': False, 'error': str(e) if re.fullmatch('[a-z_]+', str(e)) else 'invalid_request'})
        except Exception:
            return self.out(502, {'ok': False, 'error': 'backend_unavailable'})
        finally:
            if resource_locked:
                RESOURCE_LOCK.release()
            if lease is not None and not lease.released:
                lease.release()

    def log_message(self, *a):
        pass


if __name__ == '__main__':
    init_db()
    security()
    server = ThreadingHTTPServer((BIND, PORT), H)
    cert, key = os.environ.get('QW_TLS_CERT'), os.environ.get('QW_TLS_KEY')
    if bool(cert) != bool(key):
        raise RuntimeError('Both TLS certificate and key are required')
    if cert:
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        context.load_cert_chain(cert, key)
        server.socket = context.wrap_socket(server.socket, server_side=True)
    print('QoderWake Panel %s listening on %s:%d (%s)' % (VERSION, BIND, PORT, 'HTTPS' if cert else 'HTTP'), flush=True)
    server.serve_forever()
