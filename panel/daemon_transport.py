"""Reusable authenticated daemon frontend session using the official CLI contract."""
import http.cookiejar
import os
from pathlib import Path
import stat
import threading
import urllib.error
import urllib.request
from urllib.parse import urlsplit

MAX_BOOTSTRAP_BYTES = 1024 * 1024
COOKIE_NAME = 'qoderwake_frontend_session'


class DaemonTransportError(Exception):
    def __init__(self, status, code, payload=None):
        self.status = status
        self.code = code
        self.payload = payload if isinstance(payload, dict) else {}
        super().__init__(code)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def read_private_token(path):
    path = Path(path)
    try:
        fd = os.open(path, os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0))
    except OSError:
        raise DaemonTransportError(503, 'daemon_frontend_token_unavailable')
    try:
        info = os.fstat(fd)
        if (not stat.S_ISREG(info.st_mode) or info.st_size > 4096 or
                info.st_uid != os.geteuid() or info.st_mode & 0o077):
            raise DaemonTransportError(503, 'daemon_frontend_token_unavailable')
        with os.fdopen(fd, 'rb') as stream:
            fd = -1
            try:
                token = stream.read(4097).decode().strip()
            except UnicodeDecodeError:
                raise DaemonTransportError(503, 'daemon_frontend_token_unavailable')
    finally:
        if fd >= 0:
            os.close(fd)
    if not 16 <= len(token) <= 4096 or '\r' in token or '\n' in token:
        raise DaemonTransportError(503, 'daemon_frontend_token_unavailable')
    return token


class DaemonSession:
    def __init__(self, base_url, token_path):
        self.base_url = base_url.rstrip('/')
        parsed = urlsplit(self.base_url)
        if parsed.scheme not in ('http', 'https') or not parsed.hostname or parsed.username or parsed.password:
            raise ValueError('invalid_daemon_url')
        self.origin = (parsed.scheme, parsed.hostname.lower(),
                       parsed.port or (443 if parsed.scheme == 'https' else 80))
        self.token_path = Path(token_path)
        self.lock = threading.Lock()
        self.jar = http.cookiejar.CookieJar()
        self.ready = False
        self.generation = 0

    def _opener(self, jar=None):
        return urllib.request.build_opener(
            urllib.request.ProxyHandler({}),
            urllib.request.HTTPCookieProcessor(jar if jar is not None else self.jar),
            NoRedirect())

    @staticmethod
    def _bounded_read(response, limit=MAX_BOOTSTRAP_BYTES):
        length = response.headers.get('Content-Length')
        if length:
            try:
                if int(length) > limit:
                    raise DaemonTransportError(502, 'daemon_response_too_large')
            except ValueError:
                raise DaemonTransportError(502, 'daemon_response_invalid')
        data = response.read(limit + 1)
        if len(data) > limit:
            raise DaemonTransportError(502, 'daemon_response_too_large')
        return data

    def _has_cookie(self):
        return any(cookie.name == COOKIE_NAME for cookie in self.jar)

    def ensure(self):
        with self.lock:
            if self.ready and self._has_cookie():
                return self.generation
            token = read_private_token(self.token_path)
            headers = {
                'content-type': 'application/json',
                'Authorization': 'Bearer ' + token,
                'X-QoderWake-Frontend-Session-Client': 'cli'
            }
            request = urllib.request.Request(
                self.base_url + '/api/frontend-auth/session/bootstrap',
                headers=headers, data=b'{}', method='POST')
            jar = http.cookiejar.CookieJar()
            try:
                response = self._opener(jar).open(request, timeout=8)
            except urllib.error.HTTPError as error:
                status = error.code
                try:
                    self._bounded_read(error)
                finally:
                    error.close()
                raise DaemonTransportError(
                    403 if status in (401, 403) else 502,
                    'daemon_frontend_bootstrap_rejected' if status in (401, 403)
                    else 'daemon_frontend_bootstrap_failed')
            except (OSError, TimeoutError):
                raise DaemonTransportError(502, 'daemon_unavailable')
            with response:
                self._bounded_read(response)
            if not any(cookie.name == COOKIE_NAME for cookie in jar):
                raise DaemonTransportError(502, 'daemon_frontend_session_missing')
            self.jar = jar
            self.ready = True
            self.generation += 1
            return self.generation

    def invalidate(self, generation):
        with self.lock:
            if self.generation != generation:
                return
            self.jar = http.cookiejar.CookieJar()
            self.ready = False

    def headers(self):
        return {'content-type': 'application/json',
                'User-Agent': 'QoderWake-Panel'}

    def _validate_request(self, request):
        parsed = urlsplit(request.full_url)
        origin = (parsed.scheme, (parsed.hostname or '').lower(),
                  parsed.port or (443 if parsed.scheme == 'https' else 80))
        if origin != self.origin:
            raise DaemonTransportError(500, 'daemon_request_origin_rejected')

    @staticmethod
    def _fresh_request(request):
        headers = {
            key: value for key, value in request.header_items()
            if key.lower() not in ('cookie', 'cookie2')
        }
        return urllib.request.Request(
            request.full_url, data=request.data, headers=headers,
            origin_req_host=request.origin_req_host,
            unverifiable=request.unverifiable, method=request.get_method())

    def _generation_opener(self, generation):
        with self.lock:
            if generation != self.generation or not self.ready:
                return None
            return self._opener(self.jar)

    def open(self, request, timeout=20, retry_auth=None):
        self._validate_request(request)
        method = request.get_method().upper()
        retry_auth = method in ('GET', 'HEAD') and retry_auth is not False
        generation = self.ensure()
        opener = self._generation_opener(generation)
        if opener is None:
            generation = self.ensure()
            opener = self._generation_opener(generation)
        try:
            return opener.open(request, timeout=timeout)
        except urllib.error.HTTPError as error:
            if error.code == 401 and retry_auth:
                error.close()
                self.invalidate(generation)
                generation = self.ensure()
                opener = self._generation_opener(generation)
                return opener.open(self._fresh_request(request), timeout=timeout)
            raise
