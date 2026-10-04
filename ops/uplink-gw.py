#!/usr/bin/env python3
"""Loopback control-plane proxy. Strict policy covers only requests routed here."""
import argparse
import hashlib
import json
import os
import sys
import re
import threading
import time
import urllib.request
import urllib.error
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlsplit

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent / 'panel' if (HERE.parent / 'panel/gateway_policy.py').is_file() else HERE))
from gateway_policy import DEFAULT_CFG, MAX_BODY, decision, policy_hash, validate_cfg
from gateway_runtime import STATE_VERSION, validate_port, validate_upstream

PORT = validate_port(int(os.environ.get('QW_GW_PORT') or 19840))
ROOT = Path(os.environ.get('QW_ROOT') or Path.home() / 'qoderwake-panel').resolve(strict=False)
UPSTREAM = validate_upstream(os.environ.get('QW_GW_UPSTREAM') or 'openapi.qoder.com.cn')
CFG_FILE = Path(os.environ.get('QW_GW_CONFIG') or ROOT / 'config/uplink-gw.json')
EXPECTED_CONFIG_HASH = os.environ.get('QW_GW_CONFIG_HASH') or ''
LOG = ROOT / 'logs' / 'uplink-gw.jsonl'
CHUNK = 8192
MAX_BODY = 1024 * 1024
REWRITE_FROM = b'https://openapi.qoder.com.cn'
REWRITE_TO = ('http://qwgw.local.test:%d' % PORT).encode()
_loglock = threading.Lock()
HEALTH_PATH = '/__qwp_gateway_health'


def load_cfg():
    if not CFG_FILE.is_file() or CFG_FILE.is_symlink():
        raise ValueError('gateway_config_missing')
    cfg = validate_cfg(json.loads(CFG_FILE.read_text()))
    digest = policy_hash(cfg)
    if EXPECTED_CONFIG_HASH and digest != EXPECTED_CONFIG_HASH:
        raise ValueError('gateway_config_hash_mismatch')
    return cfg


def prepare_log():
    LOG.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd = os.open(LOG, os.O_WRONLY | os.O_CREAT | os.O_APPEND | getattr(os, 'O_NOFOLLOW', 0), 0o600)
    os.close(fd)


def logline(rec):
    with _loglock:
        LOG.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        fd = os.open(LOG, os.O_WRONLY | os.O_CREAT | os.O_APPEND | getattr(os, 'O_NOFOLLOW', 0), 0o600)
        with os.fdopen(fd, 'a') as stream:
            stream.write(json.dumps(rec, ensure_ascii=False)+'\n')


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class H(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    config = None

    def setup(self):
        super().setup()
        self.connection.settimeout(15)

    def reply(self, status, code):
        body = json.dumps({'success': False, 'error': code}).encode() if status != 204 else b''
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.send_header('Connection', 'close')
        self.end_headers()
        if self.command != 'HEAD':
            self.wfile.write(body)

    def forward(self):
        if self.path == '/__qwp_gateway_health' and self.command == 'GET':
            data = json.dumps({
                'schemaVersion': STATE_VERSION,
                'service': 'qw-control-gateway',
                'pid': os.getpid(),
                'nonce': os.environ.get('QW_GW_NONCE', ''),
                'sourceHash': getattr(self, 'source_hash', ''),
                'policyHash': getattr(self, 'policy_hash', ''),
                'runtimeHash': getattr(self, 'runtime_hash', ''),
                'configHash': policy_hash(self.config),
                'mode': self.config['mode'],
                'port': PORT,
                'root': str(ROOT),
                'upstream': UPSTREAM,
                'configPath': str(CFG_FILE)
            }).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(data)))
            self.send_header('Connection', 'close')
            self.end_headers()
            self.wfile.write(data)
            self.close_connection = True
            return
        start = time.monotonic()
        status, sent, received, size, action = 502, False, 0, 0, 'deny'
        headers_sent = False
        cfg = self.config or {}
        try:
            if self.headers.get('Transfer-Encoding') or len(self.headers.get_all('Content-Length', [])) > 1:
                status = 400
                return self.reply(status, 'unsupported_framing')
            try:
                size = int(self.headers.get('Content-Length', '0'))
            except ValueError:
                status = 400
                return self.reply(status, 'invalid_length')
            if not 0 <= size <= MAX_BODY:
                status = 413
                return self.reply(status, 'body_too_large')
            cfg = self.config if self.config is not None else load_cfg()
            action, rule = decision(cfg, self.command, self.path, size)
            if action == 'deny':
                status = 403
                return self.reply(status, 'gateway_policy_denied')
            if action == 'sink':
                status = 204
                return self.reply(status, '')
            body = self.rfile.read(size) if size else None
            if size and len(body) != size:
                status = 400
                return self.reply(status, 'incomplete_body')
            hop = {'host', 'connection', 'content-length', 'accept-encoding', 'transfer-encoding',
                   'proxy-authorization', 'proxy-authenticate', 'keep-alive', 'te', 'trailer', 'upgrade'}
            hop.update(x.strip().lower() for x in self.headers.get('Connection', '').split(','))
            allowed = {x.lower() for x in rule['headers']} if rule else None
            hdr = {k:v for k,v in self.headers.items() if k.lower() not in hop and (allowed is None or k.lower() in allowed)}
            upstream = urlsplit('https://' + validate_upstream(UPSTREAM))
            if upstream.hostname != UPSTREAM or upstream.port or upstream.path:
                raise ValueError('invalid_gateway_upstream')
            hdr.update({'Host': UPSTREAM, 'Connection': 'close', 'Accept-Encoding': 'identity'})
            request = urllib.request.Request('https://'+UPSTREAM+self.path, data=body, headers=hdr, method=self.command)
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
            sent = True
            try:
                response = opener.open(request, timeout=60)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                status = response.code
                self.send_response(status)
                for key, value in response.headers.items():
                    if key.lower() in ('content-type', 'cache-control', 'etag'):
                        self.send_header(key, value)
                self.send_header('Connection', 'close')
                self.end_headers()
                headers_sent = True
                if self.command != 'HEAD':
                    if 'event-stream' in response.headers.get('Content-Type', '').lower():
                        while True:
                            chunk = response.read1(CHUNK)
                            if not chunk:
                                break
                            received += len(chunk)
                            self.wfile.write(chunk)
                            self.wfile.flush()
                    else:
                        # Keep a suffix so a rewritten endpoint split across chunks is preserved.
                        pending = b''
                        while True:
                            chunk = response.read(CHUNK)
                            if not chunk:
                                self.wfile.write(pending.replace(REWRITE_FROM, REWRITE_TO))
                                received += len(pending)
                                break
                            pending += chunk
                            if len(pending) >= len(REWRITE_FROM):
                                end = len(pending)-len(REWRITE_FROM)+1
                                match = pending.find(REWRITE_FROM, max(0, end-len(REWRITE_FROM)+1))
                                if 0 <= match < end and match+len(REWRITE_FROM) > end:
                                    end = match
                                self.wfile.write(pending[:end].replace(REWRITE_FROM, REWRITE_TO))
                                received += end
                                pending = pending[end:]
        except (BrokenPipeError, ConnectionResetError):
            pass
        except Exception:
            status = 502
            if not headers_sent:
                try:
                    self.reply(status, 'gateway_upstream_unavailable')
                except OSError:
                    pass
        finally:
            self.close_connection = True
            logline({'ts': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()), 'method': self.command,
                     'path': (self.path.split('?', 1)[0] if self.path.split('?', 1)[0] in cfg.get('audit_paths', []) or
                              any(rule['path'] == self.path.split('?', 1)[0] for rule in cfg.get('rules', [])) else '/<redacted>'), 'q': size, 'status': status, 'r': received,
                     'action': action, 'forward_attempted': sent, 'ms': int((time.monotonic()-start)*1000)})

    do_GET = do_POST = do_PUT = do_PATCH = do_DELETE = do_HEAD = forward

    def log_message(self, *args):
        pass


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    H.config = load_cfg()
    H.source_hash = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
    H.policy_hash = hashlib.sha256(Path(__import__('gateway_policy').__file__).read_bytes()).hexdigest()
    H.runtime_hash = hashlib.sha256(Path(__import__('gateway_runtime').__file__).read_bytes()).hexdigest()
    if args.check:
        print(json.dumps({
            'ok': True,
            'configHash': policy_hash(H.config),
            'sourceHash': H.source_hash,
            'policyHash': H.policy_hash,
            'runtimeHash': H.runtime_hash,
            'port': PORT,
            'root': str(ROOT),
            'upstream': UPSTREAM
        }))
    else:
        prepare_log()
        print('uplink-gw mode=%s port=%d; strict scope is this proxy only' % (H.config['mode'], PORT), flush=True)
        ThreadingHTTPServer(('127.0.0.1', PORT), H).serve_forever()
