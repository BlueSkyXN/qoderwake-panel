import contextlib
import json
import os
from pathlib import Path
import sys
import shlex
import tempfile
import time
import unittest
from unittest.mock import patch

sys.path.insert(0,str(Path(__file__).resolve().parents[1]/'panel'))
from cli_catalog import Catalog, REFERENCE, build_argv, help_entries, parse_help, redact
from cli_jobs import Operations, ACTIVE
from cli_files import OperationFiles
from panel_accounts import AccessError

ADMIN={'role':'admin','principal':'user:owner','user_id':'owner','wakers':[]}
MAINTAINER={'role':'maintainer','principal':'user:alice','user_id':'alice','wakers':['w1']}
OBSERVER=dict(MAINTAINER,role='observer',principal='user:reader',user_id='reader')


class CatalogTests(unittest.TestCase):
    def setUp(self):self.catalog=Catalog()

    def test_reference_tree_has_no_missing_child_and_every_leaf_is_registered(self):
        nodes=json.loads(REFERENCE.read_text())['nodes'];paths={tuple(n['path']) for n in nodes}
        leaves=[]
        for node in nodes:
            commands=help_entries(node['help'],'Commands')
            for declaration,_ in commands:
                name=declaration.split()[0].split('|')[0]
                if name!='help':self.assertIn(tuple(node['path']+[name]),paths)
            if not commands and node['path']:leaves.append('.'.join(node['path']))
        self.assertEqual(set(leaves),set(self.catalog.operations))
        self.assertEqual(len(leaves),373)
        self.assertEqual(sum(len(x['options']) for x in self.catalog.operations.values()),1817)

    def test_json_input_output_repeated_values_and_literal_positionals(self):
        mcp=self.catalog.get('mcp.add');waker=self.catalog.get('waker.list')
        self.assertTrue(next(o for o in mcp['options'] if o['name']=='--json')['value'])
        self.assertFalse(next(o for o in waker['options'] if o['name']=='--json')['value'])
        argv=build_argv(mcp,{'options':{'--json':'{"key":"value"}','--header':['X-A: one','X-B: two']}})
        self.assertIn('--header=X-A: one',argv);self.assertIn('--header=X-B: two',argv)
        argv=build_argv(self.catalog.get('config.set'),{'arguments':{'key':'--help','value':'$(touch forbidden); `date`'}})
        self.assertEqual(argv,['config','set','--','--help','$(touch forbidden); `date`'])
        with self.assertRaises(ValueError):build_argv(waker,{'options':{'--json':'payload'}})
        with self.assertRaises(ValueError):build_argv(waker,{'options':{'--command':'sh'}})

    def test_all_option_declarations_and_positionals_round_trip(self):
        for spec in self.catalog.operations.values():
            values={'options':{},'arguments':{}}
            for option in spec['options']:
                if option['value']:
                    value=option['choices'][0] if option['choices'] else 'synthetic value'
                    values['options'][option['name']]=[value] if option['variadic'] else value
                else:values['options'][option['name']]=True
            for arg in spec['arguments']:
                values['arguments'][arg['name']]=['literal'] if arg['variadic'] else 'literal'
            argv=build_argv(spec,values)
            self.assertEqual(argv[:len(spec['path'])],spec['path'])
            for option in spec['options']:
                self.assertTrue(any(a==option['name'] or a.startswith(option['name']+'=') for a in argv),spec['id'])

    def test_scope_rejects_other_wakers_aliases_public_paths_and_observer_writes(self):
        c=self.catalog
        c.authorize(MAINTAINER,c.get('project.list'),{'options':{'--waker-id':'w1'}})
        for options in ({'--waker-id':'w2'},{'--waker-id':'w1','--public':True},
                        {'--waker-id':'w1','--worker-id':'w2'}):
            with self.assertRaises(AccessError):c.authorize(MAINTAINER,c.get('project.list'),{'options':options})
        with self.assertRaises(AccessError):c.authorize(OBSERVER,c.get('skill.manage'),{'options':{'--waker-id':'w1'}})
        with self.assertRaises(AccessError):c.authorize(MAINTAINER,c.get('skill.manage'),{'options':{'--waker-id':'w1','--path':'../../outside'}})
        with self.assertRaises(AccessError):c.authorize(MAINTAINER,c.get('config.list'))

    def test_help_alias_and_contract_mismatch(self):
        self.assertEqual(parse_help(['thing','list'],'Usage: qoderwake thing list|ls [options]\n')['path'],['thing','list'])
        with self.assertRaises(ValueError):parse_help(['thing','list'],'Usage: qoderwake thing [options]\n')
        self.assertEqual(redact({'apiKey':'secret','content':'allowed'}),{'apiKey':'[已隐藏]','content':'allowed'})
        self.assertEqual(redact({'totalTokens':42,'accessToken':'secret'}),{'totalTokens':42,'accessToken':'[已隐藏]'})
        ready,pending=Operations._partial_output('received synthetic-inp',['synthetic-input-secret'])
        self.assertEqual(ready,'received ');self.assertEqual(pending,'synthetic-inp')


class JobTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.root=Path(self.temp.name)
        self.home=self.root/'home';self.home.mkdir();self.binary=self.root/'qoderwake'
        script=self.root/'native-fixture.py'
        script.write_text('''import json,os,sys,time
from pathlib import Path
reference=json.loads(Path('''+repr(str(REFERENCE))+''').read_text())
args=sys.argv[1:]
if args[-1:] == ['--help']:
    node=next((n for n in reference['nodes'] if n['path']==args[:-1]),None)
    if not node:raise SystemExit(2)
    print(node['help'],end='');raise SystemExit(0)
if args==['log']:
    print('流式一',flush=True);time.sleep(0.25);print('流式二',flush=True);raise SystemExit(0)
if args==['login']:
    print('Input: ',end='',flush=True);line=sys.stdin.readline().strip();print('received '+line,flush=True);raise SystemExit(0)
if args[:2]==['proxy','set']:
    value=sys.stdin.read();print('stdin-bytes='+str(len(value.encode())),flush=True);raise SystemExit(0)
if args==['stop']:
    print('submitted',flush=True);time.sleep(60)
if args==['status']:
    print('HOME='+os.environ.get('QODERWAKE_HOME',''),flush=True);print('CONTEXT='+os.environ.get('QODERWAKE_CONVERSATION_ID',''),flush=True)
print(json.dumps(args),flush=True)
''')
        # macOS 系统 shell 启动时会移除 DYLD_*；便携 Framework 需在 shell 内重建这两项。
        exports=''.join('export '+name+'='+shlex.quote(os.environ[name])+'\n'
                        for name in ('DYLD_FRAMEWORK_PATH','DYLD_LIBRARY_PATH') if name in os.environ)
        self.binary.write_text('#!/bin/sh\n'+exports+'exec '+shlex.quote(sys.executable)+' '+shlex.quote(str(script))+' "$@"\n')
        self.binary.chmod(0o700)
        self.events=[]
        self.ops=Operations(self.root/'jobs',self.home,str(self.binary),audit=lambda *a:self.events.append(a))

    def tearDown(self):
        self.ops.close()
        for _ in range(100):
            if not any(j['status'] in ACTIVE for j in self.ops.jobs.values()):break
            time.sleep(0.02)
        self.temp.cleanup()

    def submit(self,operation,identity=ADMIN,key='request_key_00000001',**kwargs):
        spec=self.ops.catalog.get(operation)
        body={'operation':operation,'revision':spec['revision'],'parameters':{},'confirm':operation,**kwargs}
        return self.ops.submit(identity,body,key)

    def wait(self,jid,identity=ADMIN):
        deadline=time.monotonic()+5
        while time.monotonic()<deadline:
            result=self.ops.get(identity,jid)
            if result['status'] not in ACTIVE:return result
            time.sleep(0.02)
        self.fail('job did not finish')

    def test_literal_argv_no_shell_and_duplicate_requests_execute_once(self):
        parameters={'arguments':{'key':'telemetry','value':'$(touch forbidden); `date`'}}
        job=self.submit('config.set',parameters=parameters)
        again=self.submit('config.set',parameters=parameters)
        self.assertEqual(job['id'],again['id']);result=self.wait(job['id'])
        self.assertEqual(result['status'],'succeeded',result)
        self.assertFalse((self.home/'forbidden').exists());self.assertEqual(len(self.events),1)
        self.assertNotIn('$(touch',result['output'])
        with self.assertRaises(AccessError):self.submit('version')
        with self.ops.db() as con:stored=json.dumps([dict(r) for r in con.execute('select * from jobs')])
        self.assertNotIn('telemetry',stored);self.assertNotIn('$(touch',stored)

    def test_stream_cursor_and_environment_isolation(self):
        with patch.dict(os.environ,{'QODERWAKE_CONVERSATION_ID':'other','QODERWAKE_RUN_TOKEN':'secret'}):
            job=self.submit('status');result=self.wait(job['id'])
        self.assertIn('HOME='+str(self.home),result['output']);self.assertIn('CONTEXT=\n',result['output'])
        job=self.submit('log',key='request_key_stream')
        first=self.ops.get(ADMIN,job['id']);result=self.wait(job['id'])
        next_page=self.ops.get(ADMIN,job['id'],result['nextOffset'])
        self.assertIn('流式一',result['output']);self.assertIn('流式二',result['output']);self.assertEqual(next_page['output'],'')

    def test_stdin_and_interactive_input_redacted(self):
        job=self.submit('proxy.set',stdin='synthetic stdin\n',parameters={'options':{'--stdin':True}})
        self.assertIn('stdin-bytes=16',self.wait(job['id'])['output'])
        job=self.submit('login',key='request_key_login',interactive=True)
        deadline=time.monotonic()+5
        while time.monotonic()<deadline:
            state=self.ops.get(ADMIN,job['id'])
            if state.get('canInput'):break
            time.sleep(0.02)
        self.ops.control(ADMIN,job['id'],'input','synthetic-input-secret\n')
        result=self.wait(job['id']);self.assertEqual(result['status'],'succeeded',result)
        self.assertNotIn('synthetic-input-secret',result['output']);self.assertIn('[已隐藏]',result['output'])

    def test_cancel_mutation_is_unknown_and_restart_does_not_replay(self):
        stopped_before_guard_release=[]
        @contextlib.contextmanager
        def guard(*args):
            try:yield
            finally:
                process=next(iter(self.ops.jobs.values()))['process']
                stopped_before_guard_release.append(process.poll() is not None)
        self.ops.guard=guard
        job=self.submit('stop')
        while self.ops.get(ADMIN,job['id'])['status']=='queued':time.sleep(0.01)
        self.ops.control(ADMIN,job['id'],'cancel');result=self.wait(job['id'])
        self.assertEqual(result['status'],'unknown')
        self.assertEqual(stopped_before_guard_release,[True])
        restarted=Operations(self.root/'jobs',self.home,str(self.binary))
        restored=restarted.get(ADMIN,job['id']);self.assertFalse(restored['outputAvailable'])
        self.assertEqual(restored['status'],'unknown')
        spec=restarted.catalog.get('stop')
        duplicate=restarted.submit(ADMIN,{'operation':'stop','revision':spec['revision'],'parameters':{},'confirm':'stop'},'request_key_00000001')
        self.assertEqual(duplicate['id'],job['id']);self.assertEqual(duplicate['status'],'unknown')

    def test_job_scope_context_and_panel_guards(self):
        params={'options':{'--waker-id':'w1'}}
        job=self.submit('waker.get',MAINTAINER,parameters=params);self.wait(job['id'],MAINTAINER)
        with self.assertRaises(AccessError):self.ops.get(OBSERVER,job['id'])
        with self.assertRaises(AccessError):self.ops.get(dict(MAINTAINER,wakers=[]),job['id'])
        with self.assertRaises(AccessError):self.submit('waker.get',MAINTAINER,key='request_key_context',parameters=params,context={'QODERWAKE_RUN_TOKEN':'token'})
        with self.assertRaises(AccessError):self.submit('waker.delete',key='request_key_delete')
        with self.assertRaises(AccessError):self.submit('skill.rollback',key='request_key_rollback')
        with self.assertRaises(AccessError):self.submit('config.reset',key='request_key_confirm',confirm='')

    def test_contract_drift_stops_execution(self):
        with patch('cli_jobs.subprocess.run') as run:
            run.return_value.returncode=0;run.return_value.stdout='Usage: qoderwake version [options]\n\nchanged\n'
            self.assertFalse(self.ops.detail(ADMIN,'version')['available'])
        with self.assertRaises(AccessError):self.submit('version')

    def test_output_limit_counts_utf8_bytes_and_preserves_cursor(self):
        job={'output':'','outputBytes':0,'offset':0,'secrets':[]}
        with patch('cli_jobs.MAX_OUTPUT',32):
            self.ops._append(job,'中'*30)
        self.assertLessEqual(len(job['output'].encode()),32)
        self.assertEqual(job['offset']+len(job['output']),30)
        self.assertEqual(job['output'],'中'*10)


class FileTests(unittest.TestCase):
    def test_upload_download_ownership_offsets_symlinks_and_cleanup(self):
        import base64
        with tempfile.TemporaryDirectory() as tmp:
            files=OperationFiles(Path(tmp)/'files');slot=files.create(ADMIN,'name')
            files.append(ADMIN,slot['id'],0,base64.b64encode(b'fixture').decode())
            self.assertEqual(files.read(ADMIN,slot['id']),(b'fixture','name'))
            with self.assertRaises(AccessError):files.read(dict(ADMIN,principal='other'),slot['id'])
            with self.assertRaises(AccessError):files.append(ADMIN,slot['id'],0,'YQ==')
            with self.assertRaises(ValueError):files.create(ADMIN,'../escape')
            output=files.create(ADMIN,'export.zip',True)
            with self.assertRaises(AccessError):files.read(ADMIN,output['id'])
            path=files.path(ADMIN,output['id']);path.symlink_to('/etc/passwd')
            with self.assertRaises(AccessError):files.read(ADMIN,output['id'])
            files.delete(ADMIN,slot['id'])
            with self.assertRaises(AccessError):files.path(ADMIN,slot['id'])


class NativeRoutesTests(unittest.TestCase):
    def setUp(self):
        import test_panel as fixture
        self.app=fixture.app
        fixture.PanelTests.setUp(self)
        self.request=lambda *a,**kw:fixture.PanelTests.request(self,*a,**kw)
        self.fixtures=JobTests();self.fixtures.setUp()
        self.sec.bootstrap_admin('owner','Owner','fixture-password')
        self.sec.create_user('alice','Alice','fixture-password','maintainer',['w1'])
        self.sec.create_user('reader','Reader','fixture-password','observer',['w1'])
        self.native=Operations(self.root/'native-operations',self.root,str(self.fixtures.binary),
                               audit=self.app.native_audit,guard=self.app.native_guard)
        self.native_patch=patch.object(self.app,'NATIVE_OPERATIONS',self.native);self.native_patch.start()
        self.guard_patch=patch.object(self.app,'waker_detail',return_value={'name':'original','description':'old','skills':[]})
        self.guard_patch.start()

    def tearDown(self):
        import test_panel as fixture
        self.native.close()
        for _ in range(100):
            if not any(j['status'] in ACTIVE for j in self.native.jobs.values()):break
            time.sleep(.02)
        self.native_patch.stop();self.guard_patch.stop();self.fixtures.tearDown()
        fixture.PanelTests.tearDown(self)

    def headers(self,username):
        status,headers,raw=self.request('POST','/api/login',{'username':username,'password':'fixture-password'})
        self.assertEqual(status,200)
        return {'Cookie':next(v for k,v in headers if k=='Set-Cookie').split(';')[0],
                'Origin':self.origin,'X-CSRF-Token':json.loads(raw)['csrf']}

    def execute(self,headers,op,params=None,**extra):
        spec=self.native.catalog.get(op)
        return self.request('POST','/api/native/execute',{'operation':op,'revision':spec['revision'],
            'confirm':op,'parameters':params or {},**extra},headers=dict(headers,**{'Idempotency-Key':'route_request_00001'}))

    def test_catalog_permissions_and_actual_execute_endpoint(self):
        for user in ('owner','alice','reader'):
            headers=self.headers(user)
            status,_,raw=self.request('GET','/api/native/catalog',headers=headers)
            self.assertEqual(status,200);items=json.loads(raw)['items']
            self.assertEqual(len(items)==373,user=='owner')
            if user=='reader':self.assertTrue(all(x['effect']=='read' for x in items))
        reader=self.headers('reader')
        self.assertEqual(self.execute(reader,'skill.toggle',{'options':{'--waker-id':'w1','--skill-id':'s1','--enabled':'false'}})[0],403)
        alice=self.headers('alice')
        self.assertEqual(self.execute(alice,'waker.get',{'options':{'--waker-id':'w2'}})[0],403)
        self.assertEqual(self.execute({'Cookie':alice['Cookie']},'waker.get',{'options':{'--waker-id':'w1'}})[0],403)
        status,_,raw=self.execute(alice,'waker.get',{'options':{'--waker-id':'w1'}})
        self.assertEqual(status,202,raw);jid=json.loads(raw)['id']
        deadline=time.monotonic()+5
        while time.monotonic()<deadline:
            status,_,raw=self.request('GET','/api/native/job?id='+jid,headers=alice)
            result=json.loads(raw)
            if result['status'] not in ACTIVE:break
            time.sleep(.02)
        self.assertEqual(result['status'],'succeeded',result)
        self.assertEqual(self.request('GET','/api/native/job?id='+jid,headers=reader)[0],404)
        self.assertEqual(self.request('POST','/api/native/file/create',{'name':'file'},headers=alice)[0],403)

    def test_native_waker_update_preserves_revision_guard(self):
        headers=self.headers('owner')
        status,_,raw=self.execute(headers,'waker.update.name',{'options':{'--waker-id':'w1'},'arguments':{'value':'new'}},baseRevision='stale')
        self.assertEqual(status,202,raw);jid=json.loads(raw)['id']
        deadline=time.monotonic()+5
        while time.monotonic()<deadline:
            result=self.native.get(ADMIN,jid)
            if result['status'] not in ACTIVE:break
            time.sleep(.02)
        self.assertEqual(result['error'],'version_conflict',result)
        self.assertEqual(result['output'],'')
        self.assertEqual(self.app.ADMISSION.status()['activeMutations'],0)

    def test_group_skill_contract_and_disabled_self_evolution_are_usable(self):
        # Conversation Skill 使用官方条件参数，不强迫选择无关的 Waker。
        spec=self.native.catalog.get('skill.manage')
        body={'parameters':{'options':{'--conversation-id':'conv1','--skill-id':'s1',
                                      '--action':'edit','--base-version-id':'v1'}}}
        identity={'role':'admin','principal':'fixture'}
        with self.app.native_guard(identity,spec,body):pass
        spec=self.native.catalog.get('skill.self-evolution')
        body={'parameters':{'options':{'--waker-id':'w1','--skill-id':'s1','--enabled':'true'}},'baseVersionId':'v1'}
        with patch.object(self.app,'skill_content',return_value={'skill':{'currentVersionId':'v1'},'editable':False}):
            with self.app.native_guard(identity,spec,body):pass
            body['baseVersionId']='stale'
            with self.assertRaises(AccessError):
                with self.app.native_guard(identity,spec,body):pass


if __name__=='__main__':unittest.main()
