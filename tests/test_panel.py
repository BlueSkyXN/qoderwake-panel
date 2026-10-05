import concurrent.futures
import http.client
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

PANEL = Path(__file__).resolve().parents[1] / 'panel'
sys.path.insert(0, str(PANEL))
spec = importlib.util.spec_from_file_location('panel_app', PANEL / 'qoderwake-panel.py')
app = importlib.util.module_from_spec(spec)
spec.loader.exec_module(app)
from panel_security import Security, AdmissionController, COOKIE, AccessError


class PanelTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.changes = patch.multiple(app, ROOT=self.root, HOME=self.root/'home', DB=self.root/'usage.db',
                                     SETTINGS=self.root/'settings.json', RUNS=self.root/'runs',
                                     BACKUPS=self.root/'backups', SECURITY=Security(self.root),
                                     ADMISSION=AdmissionController())
        self.changes.start()
        self.sec = app.SECURITY
        self.server = app.ThreadingHTTPServer(('127.0.0.1', 0), app.H)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.port = self.server.server_port
        self.origin = 'http://127.0.0.1:%d' % self.port

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()
        self.changes.stop()
        self.tmp.cleanup()

    def request(self, method, path, body=None, token=None, headers=None, raw=None):
        hdr = dict(headers or {})
        if token:
            hdr['Authorization'] = 'Bearer ' + token
        if body is not None:
            raw = json.dumps(body).encode()
            hdr.setdefault('Content-Type', 'application/json')
        c = http.client.HTTPConnection('127.0.0.1', self.port, timeout=10)
        c.request(method, path, raw, hdr)
        r = c.getresponse()
        data = r.read()
        result = (r.status, r.getheaders(), data)
        c.close()
        return result

    def login(self, token=None):
        status, headers, body = self.request('POST', '/api/login', {'token': token or self.sec.admin})
        self.assertEqual(status, 200)
        cookies = [v for k,v in headers if k.lower() == 'set-cookie']
        return cookies[0].split(';')[0], json.loads(body)['csrf'], cookies

    def test_anonymous_and_query_token_rejected(self):
        for path in ['/api/state', '/api/state?token='+self.sec.admin, '/api/chat/messages?session=x']:
            status, _, _ = self.request('GET', path)
            self.assertEqual(status, 401)

    def test_cookie_is_opaque_and_logout_revokes(self):
        cookie, csrf, cookies = self.login()
        self.assertNotIn(self.sec.admin, cookie)
        self.assertIn('HttpOnly', cookies[0])
        self.assertIn('SameSite=Strict', cookies[0])
        self.assertNotIn('; Secure', cookies[0])
        headers = {'Cookie': cookie}
        self.assertEqual(self.request('GET','/api/whoami',headers=headers)[0], 200)
        self.assertEqual(self.request('POST','/api/logout',{},headers=headers)[0], 403)
        headers.update({'Origin':self.origin,'X-CSRF-Token':csrf})
        self.assertEqual(self.request('POST','/api/logout',{},headers=headers)[0], 200)
        self.assertEqual(self.request('GET','/api/whoami',headers=headers)[0], 401)

    def test_native_tls_sets_secure_cookie(self):
        import ssl
        import subprocess
        cert,key=self.root/'cert.pem',self.root/'key.pem'
        subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(key),'-out',str(cert),'-days','1','-subj','/CN=localhost'],check=True,capture_output=True)
        server=app.ThreadingHTTPServer(('127.0.0.1',0),app.H)
        context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.minimum_version=ssl.TLSVersion.TLSv1_2
        context.load_cert_chain(cert,key)
        server.socket=context.wrap_socket(server.socket,server_side=True)
        thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
        try:
            trusted=ssl.create_default_context(cafile=str(cert))
            c=http.client.HTTPSConnection('localhost',server.server_port,context=trusted,timeout=10)
            body=json.dumps({'token':self.sec.admin})
            c.request('POST','/api/login',body,{'Content-Type':'application/json'})
            response=c.getresponse();response.read()
            self.assertEqual(response.status,200)
            self.assertIn('; Secure',response.getheader('Set-Cookie'))
            c.close()
        finally:
            server.shutdown();server.server_close();thread.join()

    def test_legacy_cookie_only_migrates_same_origin(self):
        headers={'Cookie':'qwtk='+self.sec.admin}
        self.assertEqual(self.request('GET','/api/whoami',headers=headers)[0],401)
        self.assertEqual(self.request('POST','/api/migrate',{},headers=headers)[0],403)
        headers['Origin']=self.origin
        status,h,body=self.request('POST','/api/migrate',{},headers=headers)
        self.assertEqual(status,200)
        self.assertNotIn(self.sec.admin,json.dumps(h))
        self.assertIn('qwp_session=',json.dumps(h))

    def test_cookie_csrf_and_cross_origin(self):
        cookie, csrf, _ = self.login()
        with patch.object(app,'do_backup',return_value='test') as call:
            self.assertEqual(self.request('POST','/api/backup',{},headers={'Cookie':cookie,'Origin':self.origin})[0],403)
            self.assertEqual(self.request('POST','/api/backup',{},headers={'Cookie':cookie,'Origin':'https://evil.example','X-CSRF-Token':csrf})[0],403)
            call.assert_not_called()
            self.assertEqual(self.request('POST','/api/backup',{},headers={'Cookie':cookie,'Origin':self.origin,'X-CSRF-Token':csrf})[0],200)

    def test_viewer_cannot_read_private_data_or_write(self):
        for path in ['/api/chat/messages?session=x','/api/chat/sessions?waker=x','/api/waker/detail?id=x','/api/audit','/api/net/state','/api/backups','/api/patch/state']:
            self.assertEqual(self.request('GET',path,token=self.sec.viewer)[0],403)
        self.assertEqual(self.request('POST','/api/backup',{},token=self.sec.viewer)[0],403)
        with patch.object(app,'api_state',return_value={'models':[],'wakers':[],'whoami':{'name':'private'},'daemon':'running'}):
            app.STATE_CACHE['at']=0
            app.STATE_CACHE['data']=None
            status,_,body=self.request('GET','/api/state',token=self.sec.viewer)
            self.assertEqual(status,200)
            self.assertIsNone(json.loads(body)['whoami'])
            self.assertEqual(json.loads(body)['providers'],[])

    def test_runtime_state_reports_defaults_without_proc(self):
        with patch.object(app,'ROOT',self.root/'new-root'), patch.object(app.Path,'is_dir',return_value=False):
            value=app.runtime_state()
        self.assertEqual(value['desired'],{'hotDeploy':False,'embeddingDisabled':True})
        self.assertFalse(value['effectiveOnPanelRestart']['hotDeploy'])
        self.assertTrue(value['effectiveOnPanelRestart']['embeddingDisabled'])

    def test_runtime_policy_defaults_fail_closed_and_invalid_file_rejected(self):
        with patch.object(app,'ROOT',self.root/'empty-root'):
            self.assertEqual(app.runtime_policy(),{'hotDeploy':False,'embeddingDisabled':True})
            app.ROOT.mkdir();(app.ROOT/'runtime-policy.json').write_text('{}')
            with self.assertRaises(ValueError):app.runtime_policy()

    def test_restart_requires_managed_state_and_matching_result(self):
        with patch.object(app, 'daemon_process_state', return_value=None), \
                patch.object(app, 'runtime_env', return_value={}):
            self.assertFalse(app.restart_daemon()[0])
        state = {
            'mode': 'direct', 'endpoint': None, 'pid': 123,
            'healthVersion': '1.1.6', 'port': 19830
        }
        status_result = app.subprocess.CompletedProcess(
            [], 0, stdout=json.dumps({
                'ok': True, 'managed': True, 'running': True,
                'healthy': True, 'pid': 123, 'mode': 'direct'
            }), stderr='')
        start_result = app.subprocess.CompletedProcess(
            [], 0, stdout=json.dumps({
                'ok': True, 'pid': 999, 'mode': 'direct',
                'version': '1.1.6'
            }), stderr='')
        with patch.object(app, 'daemon_process_state',
                          side_effect=[state, state]), \
                patch.object(app, 'runtime_env', return_value={}), \
                patch.object(app.subprocess, 'run',
                             side_effect=[status_result, start_result]):
            self.assertFalse(app.restart_daemon()[0])

    def test_restart_rejects_unhealthy_managed_status(self):
        state = {
            'mode': 'gateway', 'endpoint': 'http://127.0.0.1:19840',
            'pid': 123, 'healthVersion': '1.1.6', 'port': 19830
        }
        status_result = app.subprocess.CompletedProcess(
            [], 0, stdout=json.dumps({
                'ok': True, 'managed': True, 'running': True,
                'healthy': False, 'pid': 123, 'mode': 'gateway'
            }), stderr='')
        with patch.object(app, 'daemon_process_state', return_value=state), \
                patch.object(app, 'runtime_env', return_value={}), \
                patch.object(app.subprocess, 'run',
                             return_value=status_result) as run:
            self.assertFalse(app.restart_daemon()[0])
        run.assert_called_once()

    def test_restart_accepts_matching_managed_state_and_result(self):
        before = {
            'mode': 'direct', 'endpoint': None, 'pid': 123,
            'healthVersion': '1.1.6', 'port': 19830
        }
        after = dict(before, pid=456)
        status_result = app.subprocess.CompletedProcess(
            [], 0, stdout=json.dumps({
                'ok': True, 'managed': True, 'running': True,
                'healthy': True, 'pid': 123, 'mode': 'direct'
            }), stderr='')
        start_result = app.subprocess.CompletedProcess(
            [], 0, stdout=json.dumps({
                'ok': True, 'pid': 456, 'mode': 'direct',
                'version': '1.1.6'
            }), stderr='')
        with patch.object(app, 'daemon_process_state',
                          side_effect=[before, after]), \
                patch.object(app, 'runtime_env', return_value={}), \
                patch.object(app.subprocess, 'run',
                             side_effect=[status_result, start_result]):
            ok, message = app.restart_daemon()
        self.assertTrue(ok)
        self.assertIn('通过', message)

    def test_runtime_apply_requires_confirmation_and_uses_restart(self):
        with patch.object(app,'restart_daemon',return_value=(True,'restarted')) as restart, patch.object(app,'daemon_activity_clear',return_value=True):
            self.assertEqual(self.request('POST','/api/runtime/apply',{},token=self.sec.admin)[0],409)
            restart.assert_not_called()
            self.assertEqual(self.request('POST','/api/runtime/apply',{'confirm':'/api/runtime/apply'},token=self.sec.admin)[0],200)
            restart.assert_called_once()

    def test_confirmation_required_before_restart(self):
        with patch.object(app,'restart_daemon',return_value=(True,'ok')) as call, patch.object(app,'daemon_activity_clear',return_value=True):
            self.assertEqual(self.request('POST','/api/apply',{},token=self.sec.admin)[0],409)
            call.assert_not_called()
            self.assertEqual(self.request('POST','/api/apply',{'confirm':'/api/apply'},token=self.sec.admin)[0],200)
            call.assert_called_once()

    def test_maintenance_rejects_before_caller_quota_and_admin_can_exit(self):
        caller=self.sec.create_caller('test',['w1'],2,1)
        token=caller['token']
        app.ADMISSION.begin_drain('admin','manual maintenance')
        app.ADMISSION.transition(app.ADMISSION.epoch,'maintenance')
        with patch.object(app,'gw_ask') as call:
            status,_,body=self.request('POST','/api/gw',{'wakerId':'w1','message':'test'},token=token)
        self.assertEqual(status,503)
        self.assertEqual(json.loads(body)['error'],'maintenance_mode')
        self.assertEqual(self.sec.callers()[0]['used'],0)
        call.assert_not_called()
        self.assertEqual(self.request('GET','/api/maintenance',token=self.sec.admin)[0],200)
        status,_,body=self.request('POST','/api/maintenance/exit',{},token=self.sec.admin)
        self.assertEqual(status,200)
        self.assertEqual(json.loads(body)['mode'],'normal')

    def test_activity_unknown_fails_closed_before_restart(self):
        incomplete={'hasRunningWork':False,'runningSessions':0}
        with patch.object(app,'system_status',return_value={'activity':incomplete}), patch.object(app,'restart_daemon') as restart:
            status,_,body=self.request('POST','/api/apply',{'confirm':'/api/apply'},token=self.sec.admin)
        self.assertEqual(status,503)
        self.assertEqual(json.loads(body)['error'],'daemon_activity_unknown')
        restart.assert_not_called()
        self.assertEqual(app.ADMISSION.status()['mode'],'normal')

    def test_idle_requires_two_consecutive_observations(self):
        observations=iter([True,False,True,True])
        with patch.object(app,'daemon_activity_clear',side_effect=lambda:next(observations)), patch.object(app.time,'sleep') as sleep:
            app.wait_daemon_idle(timeout=1,interval=.01)
        self.assertEqual(sleep.call_count,3)

    def test_maintenance_action_failure_restores_normal(self):
        lease=app.ADMISSION.acquire('admin')
        with patch.object(app,'daemon_activity_clear',return_value=True):
            with self.assertRaisesRegex(RuntimeError,'failure'):
                app.maintenance_transaction('admin','test',lambda:(_ for _ in ()).throw(RuntimeError('failure')),lease,timeout=1)
        self.assertEqual(app.ADMISSION.status()['mode'],'normal')
        self.assertEqual(app.ADMISSION.status()['activeMutations'],0)

    def test_maintenance_enter_bypasses_saturated_write_bucket(self):
        for _ in range(120):
            self.assertTrue(self.sec.limit('writes:admin',120,60))
        self.assertFalse(self.sec.limit('writes:admin',120,60))
        body={'reason':'urgent safety gate','ttl':60,'confirm':'/api/maintenance/enter'}
        status,_,raw=self.request('POST','/api/maintenance/enter',body,token=self.sec.admin)
        self.assertEqual(status,200)
        self.assertEqual(json.loads(raw)['mode'],'maintenance')
        self.assertEqual(self.request('POST','/api/maintenance/exit',{},token=self.sec.admin)[0],200)

    def test_manual_maintenance_enter_and_exit(self):
        body={'reason':'planned local change','ttl':60,'confirm':'/api/maintenance/enter'}
        status,_,raw=self.request('POST','/api/maintenance/enter',body,token=self.sec.admin)
        self.assertEqual(status,200)
        self.assertEqual(json.loads(raw)['mode'],'maintenance')
        self.assertEqual(self.request('POST','/api/backup',{},token=self.sec.admin)[0],503)
        self.assertEqual(self.request('POST','/api/maintenance/exit',{},token=self.sec.admin)[0],200)

    def test_strict_and_arbitrary_sink_not_accepted(self):
        with patch.object(app,'gw_cfg',return_value={'mode':'enforce','sink_post_prefixes':['/algo/api/v1/tracking']}) as _, patch.object(app,'gw_restart') as restart:
            for b in [{'token_policy':'strict'}, {'sink_post_prefixes':['/']}, {'mode':'strict'}]:
                b['confirm']='/api/net/config'
                self.assertEqual(self.request('POST','/api/net/config',b,token=self.sec.admin)[0],400)
            restart.assert_not_called()

    def test_failed_login_rate_limit_never_locks_valid_admin(self):
        for _ in range(10):
            self.assertEqual(self.request('POST','/api/login',{'token':'bad'})[0],401)
        self.assertEqual(self.request('POST','/api/login',{'token':'bad'})[0],429)
        self.assertEqual(self.request('POST','/api/login',{'token':self.sec.admin})[0],200)

    def test_spend_limit_persists_and_errors_audited(self):
        with patch.object(app,'chat_send',side_effect=RuntimeError('secret-must-not-leak')):
            for _ in range(20):
                status,_,body=self.request('POST','/api/chat/send',{'sessionId':'example','message':'PRIVATE_MESSAGE'},token=self.sec.admin)
                self.assertEqual(status,502)
                self.assertNotIn(b'secret-must-not-leak',body)
            app.SECURITY=Security(self.root)
            self.assertEqual(self.request('POST','/api/chat/send',{'sessionId':'example','message':'PRIVATE_MESSAGE'},token=self.sec.admin)[0],429)
        audit=app.SECURITY.recent()
        self.assertEqual(len(audit),21)
        self.assertNotIn('PRIVATE_MESSAGE',json.dumps(audit))
        self.assertEqual(audit[0]['status'],429)

    def test_atomic_rate_limit(self):
        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as ex:
            results=list(ex.map(lambda _:self.sec.limit('concurrent',20,300),range(30)))
        self.assertEqual(sum(results),20)

    def test_invalid_body_and_content_types(self):
        self.assertEqual(self.request('POST','/api/backup',raw=b'{}',token=self.sec.admin,headers={'Content-Type':'text/plain'})[0],415)
        self.assertEqual(self.request('POST','/api/backup',raw=b'{',token=self.sec.admin,headers={'Content-Type':'application/json'})[0],400)
        self.assertEqual(self.request('POST','/api/backup',body=[],token=self.sec.admin)[0],400)
        self.assertEqual(self.request('POST','/api/backup',body={'large':'x'*65537},token=self.sec.admin)[0],413)

    def test_security_headers_and_no_inline_handlers(self):
        status,headers,body=self.request('GET','/')
        self.assertEqual(status,200)
        headers=dict((k.lower(),v) for k,v in headers)
        self.assertEqual(headers['referrer-policy'],'no-referrer')
        self.assertIn("script-src 'self'",headers['content-security-policy'])
        self.assertNotIn(b'onclick=',body)
        self.assertNotIn(b'onkeydown=',body)
        self.assertEqual(int(headers['content-length']),len(body))

    def test_daemon_error_detail_is_allowlisted_and_pending_preserved(self):
        class ErrorBody:
            def __init__(self,payload,status=422):
                self.code=status;self.payload=json.dumps(payload).encode();self.headers={}
            def read(self,n=-1):return self.payload[:n]
            def close(self):pass
        class Session:
            def open(self,*args,**kwargs):
                raise urllib.error.HTTPError('http://daemon',422,'bad',{},ErrorBody({'code':'OP_PENDING','message':'PRIVATE_MESSAGE','data':{'operationId':'op-1','status':'pending','secret':'PRIVATE_SECRET'}}))
        import urllib.error
        with patch.object(app,'console_op',return_value=(Session(),{})):
            with self.assertRaises(app.DaemonRequestError) as error:
                app.daemon_json('POST','/api/write',{})
        self.assertEqual(error.exception.status,202)
        self.assertEqual(error.exception.detail,{'code':'OP_PENDING','operationId':'op-1','status':'pending'})
        self.assertNotIn('PRIVATE',json.dumps(error.exception.detail))

    def test_daemon_error_status_precedes_operation_identifiers(self):
        for status, expected in ((401, 403), (403, 403), (404, 404), (429, 429)):
            with self.assertRaises(app.DaemonRequestError) as error:
                app.daemon_request_error(status, {
                    'operationId': 'op-1', 'status': 'failed'
                })
            self.assertEqual(error.exception.status, expected)
        with self.assertRaises(app.DaemonConflict):
            app.daemon_request_error(409, {
                'operationId': 'op-1', 'status': 'pending'
            })
        with self.assertRaises(app.DaemonRequestError) as error:
            app.daemon_request_error(422, {'operationId': 'op-1'})
        self.assertEqual(error.exception.status, 400)

    def test_daemon_http_202_is_preserved_with_allowlisted_detail(self):
        class Response:
            status = 202
            def __init__(self):
                self.body = json.dumps({
                    'operationId': 'op-1', 'status': 'pending',
                    'message': 'PRIVATE_MESSAGE'
                }).encode()
            def read(self, size=-1): return self.body[:size]
            def __enter__(self): return self
            def __exit__(self, *args): pass
        class Session:
            def open(self, *args, **kwargs): return Response()
        with patch.object(app, 'console_op', return_value=(Session(), {})):
            with self.assertRaises(app.DaemonRequestError) as error:
                app.daemon_json('POST', '/api/write', {})
        self.assertEqual(error.exception.status, 202)
        self.assertEqual(error.exception.detail, {
            'operationId': 'op-1', 'status': 'pending'
        })

    def test_advanced_routes_filter_shape_and_contract(self):
        responses={
            '/api/health':{'data':{'version':'1.1.6','instanceId':'private'}},
            '/api/v1/system/status':{'data':{'version':'1.1.6','update':{'runningVersion':'1.1.6','restartCommand':'private'},'activity':{'runningSessions':2,'private':True}}},
            '/api/agents/w1/console-sessions/query?offset=0&limit=30&pinned_first=true&task_source=all':{'data':{'items':[{'session_id':'s1','local_agent_id':'w1','title':'ok','secret':'private'},{'session_id':'s2','local_agent_id':'w2'}],'has_more':False}},
            '/api/sessions/s1/artifacts':{'data':{'target':'local','outputDir':'private','artifacts':[{'id':'a1','title':'A','sourcePath':'private','relativePath':'out.txt'}]}},
            '/api/triggers?page=1&pageSize=20':{'data':{'items':[{'triggerId':'t1','triggerName':'T','token':'private'}],'pagination':{'page':1}}},
            '/api/channels/pairing/pending?limit=50&offset=0&includeTotal=true':{'data':{'items':[{'pendingId':'p1','channelId':'c1','senderId':'s','private':'x'}],'total':1}},
        }
        def daemon(method,path,body=None,timeout=20):return responses[path]
        with patch.object(app,'daemon_json',side_effect=daemon):
            self.assertNotIn('private',json.dumps(app.system_status()))
            self.assertEqual([x['session_id'] for x in app.console_sessions('w1')['items']],['s1'])
            self.assertNotIn('private',json.dumps(app.session_artifacts('s1')))
            self.assertNotIn('private',json.dumps(app.automations()))
            self.assertNotIn('private',json.dumps(app.pending_pairings()))

    def test_artifact_download_is_bounded_and_forces_active_content_attachment(self):
        class Headers(dict):
            def get_content_type(self):return self.get('Content-Type','application/octet-stream')
        class Response:
            headers=Headers({'Content-Type':'text/html','Content-Length':'4'})
            def read(self,n):return b'test'
            def close(self):pass
        class Opener:
            def open(self,*a,**k):return Response()
        with patch.object(app,'console_op',return_value=(Opener(),{})), patch.object(app,'session_artifacts',return_value={'artifacts':[{'id':'artifact-1'}]}):
            data,mime=app.artifact_file('session.with.dot','artifact-1')
        self.assertEqual(data,b'test');self.assertEqual(mime,'application/octet-stream')
        with self.assertRaises(ValueError):app.artifact_file('../bad','x')

    def test_skill_conflict_and_advanced_writes(self):
        with patch.object(app,'daemon_json',side_effect=app.DaemonConflict('x')):
            body={'wakerId':'w1','skillId':'skill1','content':'new','baseVersionId':'v1','confirm':'/api/skill/update'}
            self.assertEqual(self.request('POST','/api/skill/update',body,token=self.sec.admin)[0],409)
        calls=[]
        def daemon(method,path,body=None,timeout=20):calls.append((method,path,body));return {'success':True}
        pending={'pendingId':'p1','pendingRevision':1,'remoteExpiresAt':'2099-01-01T00:00:00Z','channelId':'c1','senderId':'s1','conversationId':'v1','bindingKey':'b1','conversationType':'group'}
        with patch.object(app,'daemon_json',side_effect=daemon), patch.object(app,'pending_pairings',return_value={'items':[pending]}), patch.object(app,'waker_detail',return_value={'id':'w1'}):
            cases=[('/api/session/read',{'sessionId':'session.with.dot'}),
                   ('/api/automation/toggle',{'id':'t1','enabled':False,'confirm':'/api/automation/toggle'}),
                   ('/api/pairing/approve',dict(pending,wakerId='w1',confirm='/api/pairing/approve'))]
            for path,body in cases:self.assertEqual(self.request('POST',path,body,token=self.sec.admin)[0],200)
        pairing=calls[-1][2]
        self.assertFalse(pairing['allowQoderwakeCommands']);self.assertFalse(pairing['allowCustomModel'])
        self.assertEqual(pairing['targets'][0]['targetId'],'w1')

    def test_pairing_changed_expired_or_missing_request_never_approved(self):
        pending={'pendingId':'p1','pendingRevision':1,'remoteExpiresAt':'2099-01-01T00:00:00Z','channelId':'c1','senderId':'s1','conversationId':'v1','bindingKey':'b1','conversationType':'group'}
        body=dict(pending,wakerId='w1',confirm='/api/pairing/approve')
        for rows,change in [([],{}),([dict(pending,pendingRevision=2)],{}),([pending],{'senderId':'different'}),([dict(pending,remoteExpiresAt='2000-01-01T00:00:00Z')],{}),([dict(pending,remoteExpiresAt=None)],{})]:
            with patch.object(app,'pending_pairings',return_value={'items':rows}), patch.object(app,'daemon_json') as call:
                self.assertEqual(self.request('POST','/api/pairing/approve',dict(body,**change),token=self.sec.admin)[0],409)
                call.assert_not_called()

    def test_skill_unavailable_is_readonly_and_updates_are_rejected(self):
        metadata={'skillId':'builtin','name':'Built in','pinned':True,'mutableByAgent':False}
        def daemon(method,path,body=None,timeout=20):
            if path=='/api/agents/w1':return {'data':{'agentId':'w1','skills':[metadata]}}
            raise app.DaemonRequestError(404,'daemon_resource_not_found')
        with patch.object(app,'daemon_json',side_effect=daemon):
            status,_,body=self.request('GET','/api/skill/content?waker=w1&skill=builtin',token=self.sec.admin)
            self.assertEqual(status,200)
            self.assertFalse(json.loads(body)['editable'])
            self.assertFalse(json.loads(body)['contentAvailable'])
            status,_,_=self.request('POST','/api/skill/update',{'wakerId':'w1','skillId':'builtin','baseVersionId':'v1','content':'change','confirm':'/api/skill/update'},token=self.sec.admin)
            self.assertEqual(status,403)

    def test_skill_save_preserves_whitespace_and_checks_version(self):
        current={'editable':True,'contentAvailable':True,'skill':{'currentVersionId':'v1'}}
        with patch.object(app,'skill_content',return_value=current), patch.object(app,'daemon_json',return_value={'success':True}) as call:
            body={'wakerId':'w1','skillId':'s1','baseVersionId':'v0','content':'  value\n','confirm':'/api/skill/update'}
            self.assertEqual(self.request('POST','/api/skill/update',body,token=self.sec.admin)[0],409)
            call.assert_not_called()
            body['baseVersionId']='v1'
            self.assertEqual(self.request('POST','/api/skill/update',body,token=self.sec.admin)[0],200)
            self.assertEqual(call.call_args.args[2]['content'],'  value\n')

    def test_gateway_failure_leaves_saved_config_unchanged(self):
        path=self.root/'gateway.json';original={'mode':'enforce','token_allow_prefixes':[]};path.write_text(json.dumps(original))
        with patch.object(app,'GWC',path), patch.object(app,'gw_restart',return_value=(False,'preflight failed')):
            self.assertFalse(app.net_config({'mode':'observe'})[0])
        self.assertEqual(json.loads(path.read_text()),original)
        self.assertFalse(list(self.root.glob('.gateway-candidate-*')))

    def test_identifiers_cannot_inject_paths_queries_or_fragments(self):
        for value in ['..','%2f','x?force=true','x#fragment','x/y','x\\y']:
            with self.assertRaises(ValueError):app.opaque_id(value)

    def test_artifact_membership_checked_before_download(self):
        with patch.object(app,'session_artifacts',return_value={'artifacts':[]}),patch.object(app,'console_op') as call:
            with self.assertRaises(app.DaemonRequestError):app.artifact_file('session1','other')
            call.assert_not_called()
        for role in [None,self.sec.viewer]:
            self.assertEqual(self.request('GET','/api/session/artifact-file?session=s&artifact=a',token=role)[0],401 if role is None else 403)

    def test_runtime_display_uses_saved_policy_not_panel_environment(self):
        app.atomic_json(self.root/'runtime-policy.json',{'hotDeploy':True,'embeddingDisabled':False})
        with patch.dict(os.environ,{'QODERWAKE_HOT_DEPLOY':'0','QODER_MEMORY_DISABLE_EMBEDDING':'1'}):
            self.assertEqual(app.runtime_state()['effectiveOnPanelRestart'],{'hotDeploy':True,'embeddingDisabled':False})

    def test_new_turn_resets_completion_and_snapshot_ids(self):
        def event(role,text='',mid=None):
            return {'payload':{'type':role,'message':{'id':mid,'content':[{'type':'text','text':text}]}}}
        events=[event('user','one','u1'),event('assistant','part','a1'),event('assistant','final','a1'),{'payload':{'type':'result','subtype':'success'}},event('user','two','u2')]
        folded=app.fold_events(events)
        self.assertEqual(folded['status'],'open')
        self.assertEqual([x['text'] for x in folded['messages']],['one','final','two'])
        events += [event('assistant','other','a2'), event('assistant','distinct','a3')]
        self.assertEqual(len(app.fold_events(events)['messages']),5)

    def test_provider_edit_preserves_key_metadata_and_single_recovery(self):
        app.SETTINGS.write_text(json.dumps({'extra':True,'providers':{'test':{'apiKey':'private-example-value','type':'openai-compatible','authType':'bearer','custom':123,'models':[{'model':'old','maxOutputTokens':32}]}}}))
        before=app.provider_store().read()
        changed=app.write_provider('test','https://api.example.com/v1','','new','New',before['revision'])
        d=json.loads(app.SETTINGS.read_text())
        self.assertEqual(d['providers']['test']['apiKey'],'private-example-value')
        self.assertEqual(d['providers']['test']['custom'],123)
        self.assertEqual(d['providers']['test']['models'][0]['maxOutputTokens'],32)
        self.assertNotIn('private-example-value',json.dumps(app.providers_state()))
        self.assertEqual(app.SETTINGS.stat().st_mode&0o777,0o600)
        self.assertEqual((self.root/'provider-settings.previous.json').read_bytes(),before['raw'])
        self.assertEqual((self.root/'provider-settings.previous.json').stat().st_mode&0o777,0o600)
        self.assertEqual(changed['revision'],app.provider_store().read()['revision'])
        self.assertFalse(list(self.root.glob('settings.panel-bak-*')))

    def test_provider_revision_conflict_and_complex_delete_fail_closed(self):
        original={'providers':{'test':{'type':'openai-compatible','models':[{'model':'a'},{'model':'b'}]}}}
        app.SETTINGS.write_text(json.dumps(original))
        revision=app.provider_store().read()['revision']
        with self.assertRaises(ValueError):
            app.write_provider('test','https://api.example.com/v1','example-value','c','C',revision)
        with self.assertRaises(ValueError):
            app.delete_provider('test',revision)
        self.assertEqual(json.loads(app.SETTINGS.read_text()),original)
        self.assertFalse((self.root/'provider-settings.previous.json').exists())
        app.SETTINGS.write_text(json.dumps({'other':True,'providers':{}}))
        with self.assertRaises(app.SettingsConflict):
            app.write_provider('new','https://api.example.com/v1','example-value','c','C',revision)

    def test_provider_probe_requires_one_use_principal_bound_plan(self):
        app.SETTINGS.write_text(json.dumps({'providers':{'test':{'baseUrl':'https://api.example.com/v1','apiKey':'private-example-value','type':'openai-compatible','authType':'bearer','models':[{'model':'m'}]}}}))
        revision=app.provider_store().read()['revision']
        plan=app.provider_probe_plan({'mode':'saved','name':'test','baseRevision':revision},'admin-a')
        self.assertEqual(plan['target'],'https://api.example.com/v1/models')
        with patch.object(app,'test_provider',return_value=(True,'ok')) as probe:
            with self.assertRaises(ValueError):
                app.execute_provider_probe({'mode':'saved','name':'test','baseRevision':revision,'plan':plan['id']},'admin-b')
            probe.assert_not_called()
            plan=app.provider_probe_plan({'mode':'saved','name':'test','baseRevision':revision},'admin-a')
            self.assertEqual(app.execute_provider_probe({'mode':'saved','name':'test','baseRevision':revision,'plan':plan['id']},'admin-a'),(True,'ok'))
            with self.assertRaises(ValueError):
                app.execute_provider_probe({'mode':'saved','name':'test','baseRevision':revision,'plan':plan['id']},'admin-a')
            probe.assert_called_once_with('https://api.example.com/v1','private-example-value')

    def test_provider_api_requires_revision_and_probe_confirmation(self):
        app.SETTINGS.write_text(json.dumps({'providers':{'test':{'baseUrl':'https://api.example.com/v1','apiKey':'private-example-value','type':'openai-compatible','authType':'bearer','models':[{'model':'m'}]}}}))
        revision=app.provider_store().read()['revision']
        body={'name':'test','baseUrl':'https://api.example.com/v1','apiKey':'','model':'m2','display':'M2'}
        self.assertEqual(self.request('POST','/api/provider',body,token=self.sec.admin)[0],400)
        body['baseRevision']=revision
        self.assertEqual(self.request('POST','/api/provider',body,token=self.sec.admin)[0],200)
        revision=app.provider_store().read()['revision']
        status,_,raw=self.request('POST','/api/provider/probe-plan',{'mode':'saved','name':'test','baseRevision':revision},token=self.sec.admin)
        self.assertEqual(status,200)
        plan=json.loads(raw)
        with patch.object(app,'test_provider',return_value=(True,'ok')) as probe:
            request={'name':'test','baseRevision':revision,'plan':plan['id']}
            self.assertEqual(self.request('POST','/api/test/saved',request,token=self.sec.admin)[0],409)
            probe.assert_not_called()
            request['confirm']='/api/test/saved'
            self.assertEqual(self.request('POST','/api/test/saved',request,token=self.sec.admin)[0],200)
            probe.assert_called_once()
        audit=self.sec.recent()
        self.assertEqual(audit[0]['target'],'test')
        self.assertNotIn('private-example-value',json.dumps(audit))
        self.assertNotIn('api.example.com/v1',json.dumps(audit))

    def test_provider_adhoc_probe_audit_uses_only_origin(self):
        plan=app.provider_probe_plan({'mode':'adhoc','baseUrl':'https://api.example.com/v1','apiKey':'private-example-value'},'token:'+app.SECURITY.credential('admin')[:12])
        with patch.object(app,'test_provider',return_value=(True,'ok')):
            body={'mode':'adhoc','baseUrl':'https://api.example.com/v1','apiKey':'private-example-value','plan':plan['id'],'confirm':'/api/test'}
            self.assertEqual(self.request('POST','/api/test',body,token=self.sec.admin)[0],200)
        audit=self.sec.recent()[0]
        self.assertEqual(audit['target'],'api.example.com')
        self.assertNotIn('private-example-value',json.dumps(audit))
        self.assertNotIn('/v1',json.dumps(audit))

    def test_deletion_preview_blocks_without_plan(self):
        blocked={'kind':'waker','target':'w1','revision':'','impactDigest':'a'*64,
                 'checks':[{'id':'groups','label':'Groups','status':'unknown','completeness':'unavailable','required':True,'count':0,'refs':[],'note':''}],
                 'warningsRequired':[],'deletable':False,'completeness':'unavailable'}
        with patch.object(app,'deletion_scan',return_value=blocked):
            status,_,raw=self.request('POST','/api/deletion/preview',{'kind':'waker','target':'w1'},token=self.sec.admin)
        self.assertEqual(status,200)
        result=json.loads(raw)
        self.assertFalse(result['deletable'])
        self.assertIsNone(result['id'])
        with self.sec.db() as con:
            self.assertEqual(con.execute('SELECT COUNT(*) FROM deletion_previews').fetchone()[0],0)

    def test_waker_deletion_preview_and_idempotent_replay_delete_once(self):
        preview={'kind':'waker','target':'w1','revision':'','impactDigest':'a'*64,
                 'checks':[{'id':'boundary','label':'Boundary','status':'warning','completeness':'partial','required':False,'count':0,'refs':[],'note':'confirm'}],
                 'warningsRequired':['boundary'],'deletable':True,'completeness':'partial'}
        with patch.object(app,'deletion_scan',return_value=preview), patch.object(app,'daemon_json',return_value={'success':True}) as delete:
            status,_,raw=self.request('POST','/api/deletion/preview',{'kind':'waker','target':'w1'},token=self.sec.admin)
            plan=json.loads(raw)
            body={'id':'w1','preview':plan['id'],'impactDigest':plan['impactDigest'],
                  'acknowledgedWarnings':['boundary'],'confirm':'/api/waker/delete'}
            headers={'Idempotency-Key':'delete-waker-0001'}
            first=self.request('POST','/api/waker/delete',body,token=self.sec.admin,headers=headers)
            second=self.request('POST','/api/waker/delete',body,token=self.sec.admin,headers=headers)
        self.assertEqual(first[0],200)
        self.assertEqual(second[0],200)
        self.assertTrue(json.loads(second[2])['replay'])
        delete.assert_called_once_with('DELETE','/api/agents/w1')

    def test_provider_deletion_replay_survives_changed_revision(self):
        app.SETTINGS.write_text(json.dumps({'providers':{'test':{'baseUrl':'https://api.example.com/v1','apiKey':'private-example-value','type':'openai-compatible','authType':'bearer','models':[{'model':'m'}]}}}))
        revision=app.provider_store().read()['revision']
        preview={'kind':'provider','target':'test','revision':revision,'impactDigest':'c'*64,
                 'checks':[],'warningsRequired':[],'deletable':True,'completeness':'complete'}
        with patch.object(app,'deletion_scan',return_value=preview):
            _,_,raw=self.request('POST','/api/deletion/preview',{'kind':'provider','target':'test','baseRevision':revision},token=self.sec.admin)
            plan=json.loads(raw)
            body={'name':'test','baseRevision':revision,'preview':plan['id'],
                  'impactDigest':plan['impactDigest'],'acknowledgedWarnings':[],
                  'confirm':'/api/provider/delete'}
            headers={'Idempotency-Key':'delete-provider-0001'}
            first=self.request('POST','/api/provider/delete',body,token=self.sec.admin,headers=headers)
            second=self.request('POST','/api/provider/delete',body,token=self.sec.admin,headers=headers)
        self.assertEqual(first[0],200)
        self.assertEqual(second[0],200)
        self.assertTrue(json.loads(second[2])['replay'])
        self.assertNotIn('test',json.loads(app.SETTINGS.read_text())['providers'])

    def test_unknown_deletion_result_replays_without_second_delete(self):
        preview={'kind':'waker','target':'w1','revision':'','impactDigest':'b'*64,
                 'checks':[],'warningsRequired':[],'deletable':True,'completeness':'complete'}
        with patch.object(app,'deletion_scan',return_value=preview), patch.object(app,'daemon_json',side_effect=TimeoutError('lost')) as delete:
            _,_,raw=self.request('POST','/api/deletion/preview',{'kind':'waker','target':'w1'},token=self.sec.admin)
            plan=json.loads(raw)
            body={'id':'w1','preview':plan['id'],'impactDigest':plan['impactDigest'],
                  'acknowledgedWarnings':[],'confirm':'/api/waker/delete'}
            headers={'Idempotency-Key':'delete-waker-unknown'}
            first=self.request('POST','/api/waker/delete',body,token=self.sec.admin,headers=headers)
            second=self.request('POST','/api/waker/delete',body,token=self.sec.admin,headers=headers)
        self.assertEqual(first[0],202)
        self.assertEqual(second[0],202)
        self.assertEqual(json.loads(first[2])['status'],'unknown')
        self.assertTrue(json.loads(second[2])['replay'])
        self.assertEqual(delete.call_count,1)

    def test_deletion_preview_audit_and_operation_do_not_store_secrets(self):
        secret='SECRET-CANARY-DO-NOT-STORE'
        app.SETTINGS.write_text(json.dumps({'providers':{'test':{'baseUrl':'https://api.example.com/v1','apiKey':secret,'type':'openai-compatible','authType':'bearer','models':[{'model':'m'}]}}}))
        revision=app.provider_store().read()['revision']
        preview={'kind':'provider','target':'test','revision':revision,'impactDigest':'f'*64,
                 'checks':[{'id':'boundary','label':'Boundary','status':'warning','completeness':'partial','required':False,'count':0,'refs':['safe-id'],'note':'safe'}],
                 'warningsRequired':['boundary'],'deletable':True,'completeness':'partial'}
        with patch.object(app,'deletion_scan',return_value=preview):
            _,_,raw=self.request('POST','/api/deletion/preview',{'kind':'provider','target':'test','baseRevision':revision},token=self.sec.admin)
            plan=json.loads(raw)
            body={'name':'test','baseRevision':revision,'preview':plan['id'],'impactDigest':plan['impactDigest'],
                  'acknowledgedWarnings':['boundary'],'confirm':'/api/provider/delete'}
            self.request('POST','/api/provider/delete',body,token=self.sec.admin,headers={'Idempotency-Key':'delete-secret-canary'})
        self.assertNotIn(secret,raw.decode())
        self.assertNotIn(secret,json.dumps(self.sec.recent()))
        self.assertNotIn(secret,self.sec.path.read_bytes().decode(errors='ignore'))

    def test_unknown_daemon_5xx_locks_target_and_status_never_redeletes(self):
        preview={'kind':'waker','target':'w1','revision':'fingerprint','impactDigest':'d'*64,
                 'checks':[],'warningsRequired':[],'deletable':True,'completeness':'complete'}
        with patch.object(app,'deletion_scan',return_value=preview), patch.object(app,'daemon_json',side_effect=app.DaemonRequestError(502,'daemon_request_failed')) as delete:
            _,_,raw=self.request('POST','/api/deletion/preview',{'kind':'waker','target':'w1'},token=self.sec.admin)
            plan=json.loads(raw)
            body={'id':'w1','preview':plan['id'],'impactDigest':plan['impactDigest'],
                  'acknowledgedWarnings':[],'confirm':'/api/waker/delete'}
            headers={'Idempotency-Key':'delete-waker-5xx'}
            result=self.request('POST','/api/waker/delete',body,token=self.sec.admin,headers=headers)
            blocked=self.request('POST','/api/deletion/preview',{'kind':'waker','target':'w1'},token=self.sec.admin)
            status=self.request('POST','/api/deletion/status',{'idempotencyKey':'delete-waker-5xx'},token=self.sec.admin)
        self.assertEqual(result[0],202)
        self.assertEqual(blocked[0],409)
        self.assertEqual(json.loads(blocked[2])['error'],'deletion_operation_unresolved')
        self.assertEqual(json.loads(status[2])['status'],'unknown')
        self.assertEqual(delete.call_count,1)

    def test_reconcile_unknown_never_deletes_and_only_unlocks_absent_target(self):
        result={'ok':False,'status':'unknown','error':'deletion_result_unknown'}
        preview={'kind':'waker','target':'w1','revision':'fp','impactDigest':'e'*64,
                 'warningsRequired':[]}
        raw,_=self.sec.create_deletion_preview('token:'+self.sec.credential('admin')[:12],preview)
        self.sec.begin_deletion(raw,'token:'+self.sec.credential('admin')[:12],
                                'waker','w1','e'*64,[],'delete-reconcile')
        self.sec.finish_deletion('token:'+self.sec.credential('admin')[:12],
                                 'delete-reconcile','unknown',result)
        with patch.object(app,'waker_detail',return_value={'agentId':'w1'}) as read, patch.object(app,'daemon_json') as delete:
            status,_,body=self.request('POST','/api/deletion/reconcile',{'kind':'waker','target':'w1'},token=self.sec.admin)
        self.assertEqual(status,202)
        self.assertEqual(json.loads(body)['error'],'deletion_target_still_exists_unresolved')
        read.assert_called_once_with('w1');delete.assert_not_called()
        with patch.object(app,'waker_detail',side_effect=app.DaemonRequestError(404,'daemon_resource_not_found')):
            status,_,body=self.request('POST','/api/deletion/reconcile',{'kind':'waker','target':'w1'},token=self.sec.admin)
        self.assertEqual(status,200)
        self.assertTrue(json.loads(body)['reconciled'])
        self.assertIsNone(self.sec.unresolved_deletion('waker','w1'))

    def test_reconcile_absent_target_without_unresolved_operation_succeeds(self):
        with patch.object(app,'waker_detail',side_effect=app.DaemonRequestError(404,'daemon_resource_not_found')):
            status,_,body=self.request('POST','/api/deletion/reconcile',{'kind':'waker','target':'already-gone'},token=self.sec.admin)
        self.assertEqual(status,200)
        self.assertEqual(json.loads(body)['status'],'succeeded')
        self.assertTrue(json.loads(body)['reconciled'])

    def test_usage_idempotent_append_and_rotation(self):
        p=app.RUNS/'test-run'/'qodercli.log';p.parent.mkdir(parents=True)
        p.write_text('2026-10-03T08:00:00 turn.started model="test-model"\n')
        self.assertEqual(app.usage_data()['perModel'],[('test-model',1)])
        self.assertEqual(app.usage_data()['perModel'],[('test-model',1)])
        with p.open('a') as f:f.write('2026-10-03T08:01:00 turn.started model="test-model"\n')
        self.assertEqual(app.usage_data()['perModel'],[('test-model',2)])
        p.unlink()
        self.assertEqual(app.usage_data()['perModel'],[('test-model',2)])

    def test_gateway_health_requires_process_identity_before_http(self):
        store = self.root / 'gateway-runtime'
        store.mkdir()
        (store / 'current.json').write_text('{}')
        record = {
            'generation': {'mode': 'strict', 'configHash': 'a' * 64},
            'process': {'pid': 123},
            'previous': None
        }
        with patch.object(app, 'load_state', return_value=record), \
                patch.object(app, 'verify_generation_files'), \
                patch.object(app, 'identity_status', return_value='mismatch'), \
                patch.object(app, 'gateway_port_status') as port_status, \
                patch.object(app.urllib.request, 'build_opener') as opener:
            result = app.gateway_active()
        self.assertTrue(result['managed'])
        self.assertFalse(result['healthy'])
        port_status.assert_not_called()
        opener.assert_not_called()

    def test_gateway_health_requires_owned_listener_before_http(self):
        store = self.root / 'gateway-runtime'
        store.mkdir()
        (store / 'current.json').write_text('{}')
        record = {
            'generation': {'mode': 'strict', 'configHash': 'a' * 64},
            'process': {'pid': 123},
            'previous': None
        }
        with patch.object(app, 'load_state', return_value=record), \
                patch.object(app, 'verify_generation_files'), \
                patch.object(app, 'identity_status', return_value='match'), \
                patch.object(app, 'gateway_port_status', return_value='other'), \
                patch.object(app.urllib.request, 'build_opener') as opener:
            result = app.gateway_active()
        self.assertTrue(result['managed'])
        self.assertFalse(result['healthy'])
        opener.assert_not_called()

    def test_runtime_guard_path_and_unknown(self):
        with patch.object(app,'api_state',return_value={'models':[],'wakers':[],'whoami':None,'daemon':'running'}):
            app.STATE_CACHE['at']=0
            app.STATE_CACHE['data']=None
            self.assertIsNone(app.panel_state('admin')['guard'])
            p=app.HOME/'runtime-generations';p.mkdir(parents=True);p.chmod(0o500)
            self.assertTrue(app.panel_state('admin')['guard'])
            p.chmod(0o700)
            self.assertFalse(app.panel_state('admin')['guard'])

    def test_client_network_and_tls_policy(self):
        with patch.dict(os.environ, {'QW_ALLOWED_CLIENTS':'192.0.2.0/24'}):
            self.assertEqual(self.request('GET','/api/state',token=self.sec.admin)[0],403)
            self.assertEqual(self.request('POST','/api/login',{'token':self.sec.admin})[0],403)
        with patch.dict(os.environ, {'QW_ALLOWED_CLIENTS':'127.0.0.0/8','QW_REQUIRE_TLS':'1'}):
            self.assertEqual(self.request('GET','/api/whoami',token=self.sec.admin)[0],200)
        obj=object.__new__(app.H)
        obj.client_address=('192.0.2.1',1234)
        obj.connection=object()
        with patch.dict(os.environ, {'QW_ALLOWED_CLIENTS':'','QW_REQUIRE_TLS':'1'}), self.assertRaises(AccessError) as error:
            obj.check_transport()
        self.assertEqual(error.exception.code,'secure_transport_required')

    def test_caller_routes_scope_and_quota(self):
        result = self.sec.create_caller('test', ['w1'], 1, 1)
        token = result['token']
        for path in ['/api/state', '/api/chat/messages?session=x', '/api/callers']:
            self.assertEqual(self.request('GET', path, token=token)[0], 403)
        self.assertEqual(self.request('POST', '/api/backup', {}, token=token)[0], 403)
        with patch.object(app, 'gw_ask', return_value=(True, 'response')) as call:
            self.assertEqual(self.request('POST','/api/gw',{'wakerId':'w2','message':'test'},token=token)[0],403)
            call.assert_not_called()
            self.assertEqual(self.request('POST','/api/gw',{'wakerId':'w1','message':'test'},token=token)[0],200)
            self.assertEqual(self.request('POST','/api/gw',{'wakerId':'w1','message':'test'},token=token)[0],429)
            call.assert_called_once()
        self.assertIn('caller:', self.sec.recent()[0]['principal'])

    def test_plugin_contract_confirmation_and_viewer_denial(self):
        with patch.object(app,'daemon_json',return_value={'success':True,'data':{}}) as call:
            body={'pluginId':'example.plugin','wakerId':'w1','expectedVersion':'1.2.3'}
            self.assertEqual(self.request('POST','/api/plugins/install',body,token=self.sec.admin)[0],409)
            call.assert_not_called()
            body['confirm']='/api/plugins/install'
            self.assertEqual(self.request('POST','/api/plugins/install',body,token=self.sec.admin)[0],403)
            call.assert_not_called()
            with patch.object(app,'PLUGIN_WRITES_ENABLED',True):
                self.assertEqual(self.request('POST','/api/plugins/install',body,token=self.sec.admin)[0],200)
            call.assert_called_once_with('POST','/api/plugin-market/example.plugin/installations/w1',{'expectedVersion':'1.2.3'})
            self.assertEqual(self.request('GET','/api/plugins/catalog',token=self.sec.viewer)[0],403)
        with self.assertRaises(ValueError): app.plugin_id('../escape')

    def test_channel_secrets_not_returned_and_runtime_not_applied(self):
        with patch.object(app,'daemon_json',return_value={'data':[{'id':'c1','type':'feishu','config':{'appSecret':'private'},'appSecret':'private'}]}):
            status,_,body=self.request('GET','/api/channels',token=self.sec.admin)
            self.assertEqual(status,200)
            self.assertNotIn(b'private',body)
        with patch.object(app,'restart_daemon') as restart:
            self.assertEqual(app.runtime_env()['QODERWAKE_HOT_DEPLOY'],'0')
            self.assertEqual(app.runtime_env()['QODER_MEMORY_DISABLE_EMBEDDING'],'1')
            body={'hotDeploy':True,'embeddingDisabled':False,'confirm':'/api/runtime/policy'}
            self.assertEqual(self.request('POST','/api/runtime/policy',body,token=self.sec.admin)[0],200)
            restart.assert_not_called()
            self.assertEqual(app.runtime_env()['QODERWAKE_HOT_DEPLOY'],'1')
            self.assertEqual(app.runtime_env()['QODER_MEMORY_DISABLE_EMBEDDING'],'0')

    def test_channel_config_preserves_secrets_and_requires_confirmation(self):
        existing={'data':{'config':{'type':'feishu','appId':'old-id','appSecret':'private','unknown':7}}}
        calls=[]
        def daemon(method,path,body=None,timeout=20):
            calls.append((method,path,body))
            return existing if method=='GET' else {'success':True}
        body={'id':'channel1','type':'feishu','appId':'','appSecret':'','botName':'Bot','accessPolicy':'open','bindingTarget':'waker1','artifactDeliveryEnabled':False,'enabled':True}
        with patch.object(app,'daemon_json',side_effect=daemon):
            self.assertEqual(self.request('POST','/api/channels/config',body,token=self.sec.admin)[0],409)
            body['confirm']='/api/channels/config'
            status,_,response=self.request('POST','/api/channels/config',body,token=self.sec.admin)
            self.assertEqual(status,200)
            saved=calls[-1][2]
            self.assertEqual(saved['appSecret'],'***')
            self.assertEqual(saved['appId'],'old-id')
            self.assertEqual(saved['unknown'],7)
            self.assertEqual(saved['bindingTarget'],{'targetKind':'waker','targetId':'waker1','displayName':None,'enabled':True})
            self.assertEqual(saved['model'],'auto')
            self.assertEqual(saved['workspacePath'],'')
            self.assertEqual(saved['mode'],'conversation')
            self.assertNotIn(b'private',response)
        existing_open={'model':'custom-model','workspace':{'kind':'folder','path':'/tmp/example'},'workspacePath':'keep','mode':'workflow','workflowId':'wf','workflowName':'Name','appId':'id','appSecret':'secret','type':'feishu','accessPolicy':'open','bindingTarget':{'targetKind':'waker','targetId':'w1','enabled':True}}
        kept=app.channel_config({'type':'feishu','appId':'id','appSecret':'','accessPolicy':'open','bindingTarget':'w1','enabled':False,'artifactDeliveryEnabled':True},existing_open)
        for key in ('model','workspace','workspacePath','mode','workflowId','workflowName'):self.assertEqual(kept[key],existing_open[key])
        with self.assertRaises(ValueError):
            app.channel_config({'type':'feishu','appId':'x','appSecret':'y','accessPolicy':'open','bindingTarget':''})
        with self.assertRaises(ValueError):
            app.channel_config({'type':'lark','appId':'x','appSecret':'y','accessPolicy':'paired'})

    def test_channel_defaults_never_enable_new_or_disabled_channel(self):
        body={'type':'feishu','appId':'x','appSecret':'y','accessPolicy':'paired'}
        self.assertFalse(app.channel_config(body)['enabled'])
        self.assertFalse(app.channel_config(body, {'enabled':False})['enabled'])
        self.assertTrue(app.channel_config(body, {'enabled':True})['enabled'])

    def test_new_channel_id_matches_official_cli_shape(self):
        calls=[]
        def daemon(method,path,body=None,timeout=20):
            calls.append((method,path,body));return {'success':True}
        with patch.object(app,'daemon_json',side_effect=daemon):
            cid=app.write_channel('',{'type':'feishu','appId':'x','appSecret':'y','accessPolicy':'paired','enabled':True,'artifactDeliveryEnabled':True})
        self.assertRegex(cid,r'^global-[0-9a-z]{6}-feishu$')
        self.assertEqual(calls[0][1],'/api/channels/'+cid+'/config')
        self.assertIsNone(calls[0][2]['bindingTarget'])
        self.assertNotIn('id',calls[0][2])
        self.assertNotIn('channelId',calls[0][2])

    def test_channel_detail_never_exposes_nested_secret(self):
        payload={'data':{'channel':{'id':'c1','status':'running','config':{'type':'feishu','appId':'visible-id','appSecret':'private','accessPolicy':'paired'}}}}
        with patch.object(app,'daemon_json',return_value=payload):
            status,_,body=self.request('GET','/api/channels/detail?id=c1',token=self.sec.admin)
        self.assertEqual(status,200)
        self.assertNotIn(b'private',body)
        self.assertIn(b'visible-id',body)
        self.assertEqual(json.loads(body)['type'],'feishu')
        self.assertEqual(json.loads(body)['id'],'c1')

    def test_backup_permissions_and_traversal(self):
        app.SETTINGS.write_text('{}')
        name=app.do_backup()
        self.assertEqual((app.BACKUPS/name).stat().st_mode&0o777,0o600)
        with self.assertRaises(ValueError):app.del_backup('../settings.json')
        self.assertTrue(app.del_backup(name)[0])


if __name__=='__main__':
    unittest.main()
