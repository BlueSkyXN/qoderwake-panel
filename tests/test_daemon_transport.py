import io
import json
from pathlib import Path
import sys
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'panel'))
import daemon_transport as transport


class Headers(dict):
    def get(self, key, default=None):
        return super().get(key, default)


class Response:
    def __init__(self, body=b'{}', headers=None):
        self.body = io.BytesIO(body)
        self.headers = Headers(headers or {})

    def read(self, size=-1): return self.body.read(size)
    def close(self): pass
    def __enter__(self): return self
    def __exit__(self, *args): self.close()


class Opener:
    def __init__(self, session, responses):
        self.session = session
        self.responses = responses
        self.requests = []

    def open(self, request, timeout=20):
        self.requests.append(request)
        response = self.responses.pop(0)
        if callable(response):
            return response(request)
        if isinstance(response, Exception):
            raise response
        return response


class DaemonTransportTests(unittest.TestCase):
    def test_encrypted_token_is_resolved_before_http_bootstrap(self):
        self.token.write_text('os-credential-v1:fixture-encrypted-placeholder')
        opener = Opener(self.session, [self.bootstrap])
        with patch.object(self.session, '_native_token', return_value='fixture-local-token-value') as native, \
                patch.object(self.session, '_opener', side_effect=self.opener_factory(opener)):
            self.session.ensure()
        native.assert_called_once()
        self.assertNotIn('os-credential', opener.requests[0].headers['Authorization'])

    def test_native_helper_keeps_credentials_out_of_arguments_and_errors(self):
        import subprocess
        self.session.native_cli = '/fixture/qoderwake'
        secret = 'a'*64
        result = subprocess.CompletedProcess([], 0, json.dumps({'token':secret}).encode(), b'')
        with patch.object(transport.subprocess, 'run', return_value=result) as run:
            self.assertEqual(self.session._native_token('os-credential-v1:fixture'), secret)
        self.assertNotIn(secret, str(run.call_args.args))
        self.assertEqual(run.call_args.kwargs['env']['BUN_BE_BUN'], '1')
        failed = subprocess.CompletedProcess([], 1, secret.encode(), ('unexpected '+secret).encode())
        with patch.object(transport.subprocess, 'run', return_value=failed):
            with self.assertRaises(transport.DaemonTransportError) as caught:
                self.session._native_token('os-credential-v1:fixture')
        self.assertNotIn(secret, str(caught.exception))
        self.assertEqual(caught.exception.code, 'daemon_credential_runtime_unavailable')

    def test_unknown_credential_version_and_locked_store_do_not_send_ciphertext(self):
        import subprocess
        self.session.native_cli = '/fixture/qoderwake'
        self.token.write_text('os-credential-v2:fixture-encrypted-placeholder')
        with patch.object(self.session, '_opener') as network, patch.object(transport.subprocess,'run') as run:
            with self.assertRaises(transport.DaemonTransportError) as caught:self.session.ensure()
            self.assertEqual(caught.exception.code,'daemon_credential_format_unsupported')
            network.assert_not_called();run.assert_not_called()
        self.token.write_text('os-credential-v1:fixture-encrypted-placeholder')
        failed=subprocess.CompletedProcess([],1,b'',b'credential_store_unavailable')
        with patch.object(transport.subprocess,'run',return_value=failed), patch.object(self.session,'_opener') as network:
            with self.assertRaises(transport.DaemonTransportError) as caught:self.session.ensure()
            self.assertEqual(caught.exception.code,'daemon_credential_store_unavailable')
            network.assert_not_called()

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.token = self.root / 'token'
        self.token.write_text('fixture-local-token-value')
        self.token.chmod(0o600)
        self.session = transport.DaemonSession('http://127.0.0.1:19830', self.token)

    def tearDown(self):
        self.tmp.cleanup()

    def opener_factory(self, opener):
        def factory(jar=None):
            if jar is not None:
                self.active_jar = jar
            return opener
        return factory

    def bootstrap(self, request):
        self.assertEqual(request.headers['Authorization'], 'Bearer fixture-local-token-value')
        self.assertEqual(request.headers['X-qoderwake-frontend-session-client'], 'cli')
        self.assertNotIn('Origin', request.headers)
        cookie = transport.http.cookiejar.Cookie(
            version=0, name=transport.COOKIE_NAME, value='session-value',
            port=None, port_specified=False, domain='127.0.0.1',
            domain_specified=False, domain_initial_dot=False, path='/',
            path_specified=True, secure=False, expires=None, discard=True,
            comment=None, comment_url=None, rest={}, rfc2109=False)
        self.active_jar.set_cookie(cookie)
        return Response()

    def test_bootstrap_uses_cli_contract_and_reuses_one_session(self):
        opener = Opener(self.session, [self.bootstrap, Response(b'{"ok":true}'), Response(b'{"ok":true}')])
        with patch.object(self.session, '_opener', side_effect=self.opener_factory(opener)):
            for _ in range(2):
                request = urllib.request.Request('http://127.0.0.1:19830/api/health')
                with self.session.open(request) as response:
                    self.assertTrue(response.read())
        bootstrap = [request for request in opener.requests if request.full_url.endswith('/bootstrap')]
        self.assertEqual(len(bootstrap), 1)
        self.assertEqual(self.session.generation, 1)

    def test_get_reauthenticates_once_but_post_never_replays(self):
        unauthorized = urllib.error.HTTPError('http://127.0.0.1:19830/api/health', 401, 'no', {}, io.BytesIO(b'{}'))
        opener = Opener(self.session, [self.bootstrap, unauthorized, self.bootstrap, Response(b'{}')])
        with patch.object(self.session, '_opener', side_effect=self.opener_factory(opener)):
            with self.session.open(urllib.request.Request('http://127.0.0.1:19830/api/health')) as response:
                self.assertEqual(response.read(), b'{}')
        self.assertEqual(len(opener.requests), 4)

        self.session.jar = transport.http.cookiejar.CookieJar();self.session.ready=False
        unauthorized = urllib.error.HTTPError('http://127.0.0.1:19830/api/write', 401, 'no', {}, io.BytesIO(b'{}'))
        opener = Opener(self.session, [self.bootstrap, unauthorized])
        with patch.object(self.session, '_opener', side_effect=self.opener_factory(opener)), self.assertRaises(urllib.error.HTTPError):
            self.session.open(urllib.request.Request('http://127.0.0.1:19830/api/write', data=b'{}', method='POST'), retry_auth=True)
        self.assertEqual(len(opener.requests), 2)

    def test_real_cookie_processor_replaces_stale_cookie_for_get_retry(self):
        requests = []
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                requests.append((self.path, self.headers.get('Cookie')))
                self.send_response(200)
                value = 'old' if len([row for row in requests if row[0].endswith('/bootstrap')]) == 1 else 'new'
                self.send_header('Set-Cookie', '%s=%s; Path=/' % (transport.COOKIE_NAME, value))
                self.send_header('Content-Length', '2')
                self.end_headers();self.wfile.write(b'{}')
            def do_GET(self):
                cookie = self.headers.get('Cookie')
                requests.append((self.path, cookie))
                body = b'{}'
                self.send_response(200 if cookie and 'new' in cookie else 401)
                self.send_header('Content-Length', str(len(body)))
                self.end_headers();self.wfile.write(body)
            def log_message(self, *args): pass
        server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True);thread.start()
        session = transport.DaemonSession('http://127.0.0.1:%d' % server.server_port, self.token)
        try:
            with session.open(urllib.request.Request(session.base_url + '/api/health')) as response:
                self.assertEqual(response.read(), b'{}')
        finally:
            server.shutdown();server.server_close();thread.join()
        gets = [cookie for path, cookie in requests if path == '/api/health']
        self.assertEqual(gets, [transport.COOKIE_NAME + '=old', transport.COOKIE_NAME + '=new'])

    def test_token_must_be_private_regular_owned_bounded_file(self):
        self.token.chmod(0o644)
        with self.assertRaisesRegex(transport.DaemonTransportError, 'daemon_frontend_token_unavailable'):
            transport.read_private_token(self.token)
        for value in ('short', 'x' * 4097):
            self.token.write_text(value);self.token.chmod(0o600)
            with self.assertRaisesRegex(transport.DaemonTransportError, 'daemon_frontend_token_unavailable'):
                transport.read_private_token(self.token)
        self.token.unlink()
        target = self.root / 'target'
        target.write_text('fixture-local-token-value');target.chmod(0o600)
        self.token.symlink_to(target)
        with self.assertRaisesRegex(transport.DaemonTransportError, 'daemon_frontend_token_unavailable'):
            transport.read_private_token(self.token)

    def test_cross_origin_request_is_rejected_before_bootstrap(self):
        with patch.object(self.session, 'ensure') as ensure, self.assertRaisesRegex(
                transport.DaemonTransportError, 'daemon_request_origin_rejected'):
            self.session.open(urllib.request.Request('http://example.com/api/health'))
        ensure.assert_not_called()


if __name__ == '__main__':
    unittest.main()
