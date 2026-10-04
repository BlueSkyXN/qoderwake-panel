import http.client
import importlib.util
import json
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('uplink', Path(__file__).resolve().parents[1]/'ops/uplink-gw.py')
gw = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gw)


class GatewayTests(unittest.TestCase):
    def test_strict_exact_routes_body_queries_and_sink_boundary(self):
        cfg = gw.validate_cfg({'mode':'strict', 'rules':[{'method':'GET','path':'/catalog','query':['page'],'headers':[],'max_body':0}]})
        self.assertEqual(gw.decision(cfg, 'GET', '/catalog?page=1', 0)[0], 'forward')
        for method, path, size in [('POST','/catalog',0),('GET','/catalog?token=x',0),('GET','/catalog/extra',0),('GET','/catalog',1),('GET','//evil.test/catalog',0),('GET','/%63atalog',0),('GET','/a/../catalog',0)]:
            self.assertEqual(gw.decision(cfg, method, path, size)[0], 'deny')
        cfg = gw.validate_cfg({'mode':'enforce','sink_post_prefixes':['/tracking']})
        self.assertEqual(gw.decision(cfg,'POST','/tracking/one',2)[0],'sink')
        self.assertEqual(gw.decision(cfg,'POST','/tracking-other',2)[0],'forward')

    def test_denial_never_contacts_upstream_and_redacts_log(self):
        with tempfile.TemporaryDirectory() as tmp:
            class Handler(gw.H):
                config = gw.validate_cfg({'mode':'strict'})
            server = gw.ThreadingHTTPServer(('127.0.0.1',0),Handler)
            thread = threading.Thread(target=server.serve_forever,daemon=True)
            thread.start()
            try:
                with patch.object(gw,'LOG',Path(tmp)/'audit.jsonl'), patch.object(gw.urllib.request,'build_opener') as opener:
                    con = http.client.HTTPConnection('127.0.0.1',server.server_port,timeout=3)
                    con.request('POST','/private-id?secret=value',b'secret-body',{'Authorization':'Bearer secret-key'})
                    response=con.getresponse()
                    self.assertEqual(response.status,403)
                    response.read();con.close()
                    server.shutdown();thread.join()
                    opener.assert_not_called()
                    log=(Path(tmp)/'audit.jsonl').read_text()
                    self.assertNotIn('secret',log)
                    self.assertNotIn('private-id',log)
                    self.assertFalse(json.loads(log)['forward_attempted'])
            finally:
                server.shutdown();server.server_close();thread.join()

    def test_forward_pins_origin_strips_headers_and_preserves_split_rewrite(self):
        import io
        raw=b'a'*(gw.CHUNK-10)+gw.REWRITE_FROM+b'z'*100
        class Response(io.BytesIO):
            code=200
            headers={'Content-Type':'application/json'}
        class Handler(gw.H):
            config=gw.validate_cfg({'mode':'strict','audit_paths':['/catalog'],'rules':[{'method':'GET','path':'/catalog','query':[],'headers':['accept'],'max_body':0}]})
        with tempfile.TemporaryDirectory() as tmp:
            server=gw.ThreadingHTTPServer(('127.0.0.1',0),Handler)
            thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
            try:
                with patch.object(gw,'LOG',Path(tmp)/'log'), patch.object(gw.urllib.request,'build_opener') as factory:
                    factory.return_value.open.return_value=Response(raw)
                    con=http.client.HTTPConnection('127.0.0.1',server.server_port,timeout=3)
                    con.request('GET','/catalog',headers={'Authorization':'Bearer private','Accept':'application/json','Cookie':'private','Proxy-Authorization':'private'})
                    response=con.getresponse()
                    self.assertEqual(response.status,200)
                    self.assertEqual(response.read(),raw.replace(gw.REWRITE_FROM,gw.REWRITE_TO))
                    con.close()
                    request=factory.return_value.open.call_args.args[0]
                    self.assertEqual(request.full_url,'https://'+gw.UPSTREAM+'/catalog')
                    self.assertNotIn('Authorization',request.headers)
                    self.assertNotIn('Cookie',request.headers)
                    self.assertNotIn('Proxy-authorization',request.headers)
                    self.assertEqual(request.headers['Accept'],'application/json')
                    self.assertEqual(json.loads((Path(tmp)/'log').read_text())['path'],'/catalog')
                    server.shutdown();thread.join()
            finally:
                server.shutdown();server.server_close();thread.join()

    def test_invalid_framing_is_rejected_and_audited_without_forwarding(self):
        import socket
        class Handler(gw.H):
            config=gw.validate_cfg({'mode':'enforce','token_allow_prefixes':[]})
        with tempfile.TemporaryDirectory() as tmp:
            server=gw.ThreadingHTTPServer(('127.0.0.1',0),Handler)
            thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
            try:
                with patch.object(gw,'LOG',Path(tmp)/'audit'), patch.object(gw.urllib.request,'build_opener') as opener:
                    for header in (b'Transfer-Encoding: chunked', b'Content-Length: 1\r\nContent-Length: 2', b'Content-Length: bad'):
                        with socket.create_connection(server.server_address,timeout=3) as sock:
                            sock.sendall(b'POST /private HTTP/1.1\r\nHost: localhost\r\n'+header+b'\r\n\r\n')
                            data=b''
                            while True:
                                chunk=sock.recv(4096)
                                if not chunk:break
                                data+=chunk
                            self.assertIn(b'400 Bad Request',data)
                    server.shutdown();thread.join()
                    opener.assert_not_called()
                    rows=[json.loads(x) for x in (Path(tmp)/'audit').read_text().splitlines()]
                    self.assertEqual(len(rows),3)
                    self.assertTrue(all(r['status']==400 and not r['forward_attempted'] for r in rows))
            finally:
                server.shutdown();server.server_close();thread.join()

    def test_rewrite_target_uses_validated_runtime_port(self):
        fresh = importlib.util.spec_from_file_location(
            'uplink_nondefault_port',
            Path(__file__).resolve().parents[1] / 'ops/uplink-gw.py')
        module = importlib.util.module_from_spec(fresh)
        with patch.dict(gw.os.environ, {'QW_GW_PORT': '19841'}):
            fresh.loader.exec_module(module)
        self.assertEqual(module.REWRITE_TO, b'http://qwgw.local.test:19841')

    def test_invalid_config_does_not_fall_back_to_observe(self):
        with tempfile.TemporaryDirectory() as tmp:
            path=Path(tmp)/'config.json';path.write_text('{')
            with patch.object(gw,'CFG_FILE',path), self.assertRaises(ValueError):
                gw.load_cfg()
            path.unlink()
            with patch.object(gw,'CFG_FILE',path), self.assertRaises(ValueError):
                gw.load_cfg()
        for value in [{'mdoe':'strict'},{'mode':'observe','unknown':True},{'mode':'strict','rules':[{'path':'/'}]}]:
            with self.assertRaises(ValueError):gw.validate_cfg(value)


if __name__=='__main__':
    unittest.main()
