import concurrent.futures
import copy
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch
import test_panel as fixture
from panel_accounts import AccessError, password_matches
from panel_security import Security

app = fixture.app
PASSWORD = 'fixture-collaboration-password'


class AccountTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.sec = Security(Path(self.tmp.name))

    def tearDown(self):
        self.tmp.cleanup()

    def test_initialization_disables_legacy_but_preserves_callers_and_old_audit(self):
        raw, _ = self.sec.login(self.sec.admin)
        caller = self.sec.create_caller('fixture', ['w1'], 5, 2)
        self.sec.audit('local', {'principal':'legacy','role':'admin'}, 'old', 'w1', 200, True)
        owner = self.sec.bootstrap_admin('Owner', '所有者', PASSWORD)
        self.assertIsNone(self.sec.role_for(self.sec.admin))
        with self.assertRaises(AccessError):
            self.sec.authenticate({'Cookie':'qwp_session='+raw})
        self.assertEqual(self.sec.authenticate({'Authorization':'Bearer '+caller['token']})['role'],'caller')
        self.assertEqual(self.sec.recent()[0]['principal'],'legacy')
        with self.sec.db() as con:
            encoded = con.execute('SELECT password_hash FROM users WHERE id=?',(owner['id'],)).fetchone()[0]
        self.assertNotIn(PASSWORD, encoded)
        self.assertTrue(password_matches(PASSWORD, encoded))
        self.assertEqual(self.sec.login_user('OWNER',PASSWORD)[1]['principal'],'user:'+owner['id'])
        with self.assertRaises(AccessError):
            self.sec.bootstrap_admin('other','其他',PASSWORD)

    def test_repeated_schema_migration_preserves_old_sessions_and_unresolved_operations(self):
        other = Path(self.tmp.name)/'old';other.mkdir()
        with sqlite3.connect(other/'access.db') as con:
            con.executescript('''CREATE TABLE sessions(id TEXT PRIMARY KEY,role TEXT NOT NULL,credential TEXT NOT NULL,csrf TEXT NOT NULL,created REAL NOT NULL,seen REAL NOT NULL);
                INSERT INTO sessions VALUES('old','admin','digest','csrf',1,1);
                CREATE TABLE audit(ts TEXT NOT NULL,ip TEXT NOT NULL,principal TEXT NOT NULL,role TEXT NOT NULL,action TEXT NOT NULL,target TEXT NOT NULL,status INTEGER NOT NULL,ok INTEGER NOT NULL);
                INSERT INTO audit VALUES('old','local','legacy','admin','old','w1',200,1);''')
        sec = Security(other)
        with sec.db() as con:
            con.execute("INSERT INTO deletion_operations VALUES('legacy','key','digest','waker','w1','unknown','{}',1,1)")
        sec = Security(other)
        with sec.db() as con:
            self.assertEqual(con.execute('SELECT count(*) FROM sessions').fetchone()[0],1)
            self.assertEqual(con.execute('SELECT status FROM deletion_operations').fetchone()[0],'unknown')
        self.assertEqual(sec.recent()[0]['principal'],'legacy')

    def test_revoke_rechecks_identity_and_last_admin_is_preserved(self):
        owner=self.sec.bootstrap_admin('owner','Owner',PASSWORD)
        user=self.sec.create_user('alice','Alice',PASSWORD,'maintainer',['w1'])
        raw, identity=self.sec.login_user('alice',PASSWORD)
        self.sec.update_user(user['id'],'Alice','observer',['w2'],True,user['revision'])
        for action in [lambda:self.sec.authenticate({'Cookie':'qwp_session='+raw}),lambda:self.sec.refresh_identity(identity)]:
            with self.assertRaises(AccessError):action()
        with self.assertRaisesRegex(AccessError,'last_admin_required'):
            self.sec.update_user(owner['id'],'Owner','observer',[],True,owner['revision'])
        with self.assertRaisesRegex(AccessError,'version_conflict'):
            self.sec.update_user(user['id'],'Alice','maintainer',['w1'],True,user['revision'])

    def test_password_reset_revokes_all_sessions_and_requires_current_password(self):
        owner=self.sec.bootstrap_admin('owner','Owner',PASSWORD)
        one, identity=self.sec.login_user('owner',PASSWORD)
        two,_=self.sec.login_user('owner',PASSWORD)
        with self.assertRaisesRegex(AccessError,'current_password_invalid'):
            self.sec.change_password(identity,'incorrect',PASSWORD+'new')
        self.sec.change_password(identity,PASSWORD,PASSWORD+'new')
        for raw in (one,two):
            with self.assertRaises(AccessError):self.sec.authenticate({'Cookie':'qwp_session='+raw})
        with self.assertRaises(AccessError):self.sec.login_user('owner',PASSWORD)
        self.assertEqual(self.sec.login_user('owner',PASSWORD+'new')[1]['user_id'],owner['id'])


class CollaborationTests(unittest.TestCase):
    request=fixture.PanelTests.request
    tearDown=fixture.PanelTests.tearDown

    def setUp(self):
        fixture.PanelTests.setUp(self)
        self.sec.bootstrap_admin('owner','Owner',PASSWORD)
        for name,role,scope in [('alice','maintainer',['w1']),('bob','maintainer',['w1']),('other','maintainer',['w2']),('reader','observer',['w1'])]:
            self.sec.create_user(name,name.title(),PASSWORD,role,scope)
        self.details={w:{'agentId':w,'name':w,'description':'original','skills':[
            {'skillId':'s1','name':'Skill','enabled':True,'mutableByAgent':True,'pinned':False,'currentVersionId':'v1'}]} for w in ('w1','w2')}
        self.content={w:{'content':'original','version':'v1'} for w in self.details}
        self.writes=[]
        self.mock=patch.object(app,'daemon_json',side_effect=self.daemon);self.mock.start();self.addCleanup(self.mock.stop)
        self.state=patch.object(app,'api_state',return_value={'daemon':'running','models':[{'id':'global-model'}],
            'wakers':[{'id':w,'name':w} for w in self.details], 'whoami':{'name':'official'},'internal':'private'})
        self.state.start();self.addCleanup(self.state.stop)
        self.addCleanup(app.invalidate_state)
        app.invalidate_state()

    def daemon(self, method, path, body=None, **kwargs):
        parts=path.split('/');wid=parts[3]
        if path=='/api/agents/'+wid:
            if method=='PUT':self.details[wid].update(copy.deepcopy(body));self.writes.append((method,path))
            detail=copy.deepcopy(self.details[wid]);detail['skills'][0]['currentVersionId']=self.content[wid]['version']
            return {'data':detail}
        if path.endswith('/content'):
            if method=='PUT':
                if body['baseVersionId']!=self.content[wid]['version']:raise app.DaemonConflict()
                self.content[wid]={'content':body['content'],'version':'v2'};self.writes.append((method,path))
            return {'data':{'skill':dict(self.details[wid]['skills'][0],currentVersionId=self.content[wid]['version']),
                            'content':self.content[wid]['content']}}
        if path.endswith('/versions'):return {'data':[{'versionId':'v0'},{'versionId':'v1'}]}
        if path.endswith('/rollback'):
            self.content[wid]={'content':'rolled back','version':'v3'};self.writes.append((method,path));return {'success':True}
        raise AssertionError((method,path))

    def headers(self, username):
        status,headers,body=self.request('POST','/api/login',{'username':username,'password':PASSWORD})
        self.assertEqual(status,200,body)
        return {'Cookie':next(v for k,v in headers if k.lower()=='set-cookie').split(';')[0],
                'Origin':self.origin,'X-CSRF-Token':json.loads(body)['csrf']}

    def get(self,path,headers):
        status,_,body=self.request('GET',path,headers=headers)
        self.assertEqual(status,200,body)
        return json.loads(body)

    def test_stable_identity_and_scope_filtered_cache(self):
        one,two=self.headers('alice'),self.headers('alice')
        self.assertEqual(self.get('/api/whoami',one)['userId'],self.get('/api/whoami',two)['userId'])
        for name,wid in [('alice','w1'),('other','w2'),('reader','w1')]:
            data=self.get('/api/state',self.headers(name))
            self.assertEqual([w['id'] for w in data['wakers']],[wid])
            self.assertNotIn('internal',data)
            self.assertEqual(data['models'],[])
        self.assertEqual(len(app.cached_state()['wakers']),2)

    def test_every_unmapped_surface_stays_closed_and_waker_id_is_checked(self):
        headers=self.headers('alice')
        for path in ['/api/users','/api/usage','/api/net/state','/api/backups','/api/memory?waker=w1',
                     '/api/chat/messages?session=known','/api/session/artifact-file?session=known&artifact=file',
                     '/api/waker/detail?id=w2','/api/skills?waker=w2','/api/skill/diff?waker=w2&skill=s1&version=v1']:
            self.assertEqual(self.request('GET',path,headers=headers)[0],403,path)
        for path in ['/api/provider','/api/users/create','/api/runtime/apply','/api/preference','/api/waker/delete','/api/gw']:
            self.assertEqual(self.request('POST',path,{'wakerId':'w1'},headers=headers)[0],403,path)
        self.assertEqual(self.writes,[])

    def test_observer_read_only_and_unassigned_skill_rejected(self):
        reader=self.headers('reader')
        self.assertFalse(self.get('/api/skill/content?waker=w1&skill=s1',reader)['editable'])
        body={'wakerId':'w1','skillId':'s1','baseVersionId':'v1','content':'new','confirm':'/api/skill/update'}
        self.assertEqual(self.request('POST','/api/skill/update',body,headers=reader)[0],403)
        self.assertEqual(self.request('GET','/api/skill/versions?waker=w1&skill=other',headers=reader)[0],404)

    def test_skill_conflict_rollback_and_personal_audit(self):
        alice,bob=self.headers('alice'),self.headers('bob')
        body={'wakerId':'w1','skillId':'s1','baseVersionId':'v1','content':'private skill text','confirm':'/api/skill/update'}
        self.assertEqual(self.request('POST','/api/skill/update',body,headers=alice)[0],200)
        self.assertEqual(self.request('POST','/api/skill/update',body,headers=bob)[0],409)
        rollback={'wakerId':'w1','skillId':'s1','baseVersionId':'v1','versionId':'v0','reason':'recover','confirm':'/api/skill/rollback'}
        self.assertEqual(self.request('POST','/api/skill/rollback',rollback,headers=bob)[0],409)
        rollback['baseVersionId']='v2'
        self.assertEqual(self.request('POST','/api/skill/rollback',rollback,headers=bob)[0],200)
        rows=self.get('/api/audit',self.headers('reader'))
        self.assertEqual({r['displayName'] for r in rows},{'Alice','Bob'})
        self.assertTrue(any(r['beforeRevision']=='v2' and r['afterRevision']=='v3' for r in rows))
        self.assertNotIn('private skill text',json.dumps(rows))
        self.assertNotIn('ip',rows[0])
        self.assertEqual(self.get('/api/audit',self.headers('other')),[])

    def test_two_waker_editors_do_not_overwrite_each_other(self):
        alice,bob=self.headers('alice'),self.headers('bob')
        detail=self.get('/api/waker/detail?id=w1',alice)
        body={'id':'w1','name':'changed','description':'edit','skills':[{'skillId':'s1','enabled':False}],
              'baseRevision':detail['configRevision']}
        with concurrent.futures.ThreadPoolExecutor(2) as pool:
            statuses=list(pool.map(lambda h:self.request('POST','/api/waker/update',body,headers=h)[0],(alice,bob)))
        self.assertEqual(sorted(statuses),[200,409])
        self.assertEqual(len(self.writes),1)
        self.assertEqual(self.details['w1']['name'],'changed')

    def test_legacy_login_bearer_and_cookie_migration_are_closed(self):
        self.assertEqual(self.request('POST','/api/login',{'token':self.sec.admin})[0],401)
        self.assertEqual(self.request('GET','/api/state',token=self.sec.admin)[0],401)
        self.assertEqual(self.request('POST','/api/migrate',{},headers={'Cookie':'qwtk='+self.sec.admin,'Origin':self.origin})[0],401)

    def test_user_management_csrf_and_revocation(self):
        owner,alice=self.headers('owner'),self.headers('alice')
        users=self.get('/api/users',owner)['items'];target=next(u for u in users if u['username']=='alice')
        body={'id':target['id'],'displayName':'Alice','role':'maintainer','wakers':['w1'],
              'enabled':False,'revision':target['revision']}
        self.assertEqual(self.request('POST','/api/users/update',body,headers={'Cookie':owner['Cookie']})[0],403)
        self.assertEqual(self.request('POST','/api/users/update',body,headers=owner)[0],200)
        self.assertEqual(self.request('GET','/api/whoami',headers=alice)[0],401)


if __name__=='__main__':
    unittest.main()
