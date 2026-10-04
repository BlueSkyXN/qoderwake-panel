"""Loopback-only synthetic UI fixture. No official account, daemon or provider is contacted."""
import argparse
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import time
from urllib.parse import parse_qs, urlsplit

PANEL = Path(__file__).resolve().parents[1]/'panel'
sys.path.insert(0, str(PANEL))
spec = importlib.util.spec_from_file_location('fixture_panel', PANEL/'qoderwake-panel.py')
app = importlib.util.module_from_spec(spec)
spec.loader.exec_module(app)
SKILLS = [{'skillId':'readonly','name':'内置只读技能','pinned':True,'mutableByAgent':False},
          {'skillId':'editable','name':'可编辑演示技能','pinned':False,'mutableByAgent':True,'currentVersionId':'v1'}]
CONTENT = {'value':'# 示例技能\n\n保留末尾换行。\n', 'version':'v1'}
DOWNLOADS = []


def deletion_scan(kind, target, revision=''):
    if kind == 'waker' and target == 'w1':
        return {'kind':'waker','target':'w1','revision':'fixture-fingerprint-1',
                'impactDigest':'a'*64,'checks':[{'id':'groups','label':'群组成员','status':'unknown','completeness':'unavailable','required':True,'count':0,'refs':[],'note':'合成场景：必需数据源不可确认，删除必须阻断。'}],
                'warningsRequired':[],'deletable':False,'completeness':'unavailable'}
    if kind == 'waker' and target == 'w2':
        return {'kind':'waker','target':'w2','revision':'fixture-fingerprint-2',
                'impactDigest':'b'*64,'checks':[{'id':'history','label':'历史会话','status':'warning','completeness':'partial','required':False,'count':2,'refs':['session-history-1','session-history-2'],'note':'合成场景：删除后历史关联可能无法继续管理。'}],
                'warningsRequired':['history'],'deletable':True,'completeness':'partial'}
    return {'kind':kind,'target':target,'revision':revision,'impactDigest':'c'*64,
            'checks':[{'id':'references','label':'引用检查','status':'clear','completeness':'complete','required':True,'count':0,'refs':[],'note':''}],
            'warningsRequired':[],'deletable':True,'completeness':'complete'}


def daemon(method, path, body=None, timeout=20):
    parsed=urlsplit(path);params=parse_qs(parsed.query);p=parsed.path
    if p in ('/api/agents/w1','/api/agents/w2') and method == 'GET':
        return {'data':{'agentId':p.rsplit('/',1)[-1],'skills':SKILLS,'name':'合成机器人'}}
    if p == '/api/agents/w2' and method == 'DELETE':
        raise app.DaemonRequestError(502,'daemon_request_failed')
    if p.endswith('/skills/readonly/content'):
        raise app.DaemonRequestError(404,'daemon_resource_not_found')
    if p.endswith('/skills/editable/content'):
        if method=='PUT':
            if body['baseVersionId']!=CONTENT['version']:raise app.DaemonConflict()
            CONTENT.update(value=body['content'],version='v2')
        return {'success':True,'data':{'skill':dict(SKILLS[1],currentVersionId=CONTENT['version']), 'content':CONTENT['value']}}
    if p.endswith('/versions'):
        return {'data':[] if '/readonly/' in p else [{'versionId':'v1','createdAt':'2026-10-03'}]}
    if p.endswith('/diff'):return {'data':{'before':'旧正文','after':CONTENT['value']}}
    if p.endswith('/rollback'):
        CONTENT.update(value='# 已回滚\n',version='v3');return {'success':True}
    if p.endswith('/console-sessions/query'):
        offset=int(params.get('offset',['0'])[0]);limit=int(params.get('limit',['20'])[0]);wid=p.split('/')[3]
        rows=[{'session_id':f'session-{i}','local_agent_id':wid,'title':f'会话 {i+1:02d}',
               'origin':'chat','session_status':'success','unread':True} for i in range(offset,min(43,offset+limit))]
        return {'data':{'items':rows,'has_more':offset+limit<43}}
    if p.endswith('/artifacts'):
        return {'data':{'artifacts':[{'id':'file1','title':'下载验收文本','relativePath':'report.txt','mimeType':'text/plain','type':'file'}]}}
    if p.endswith('/read'):return {'success':True}
    if p=='/api/health':return {'data':{'version':'1.1.6-fixture'}}
    if p=='/api/v1/system/status':return {'data':{'version':'fixture','update':{'runningVersion':'fixture','restartRequired':False},'activity':{'runningSessions':0}}}
    if p=='/api/triggers':
        page=int(params.get('page',['1'])[0]);rows=[{'triggerId':f't{i}','triggerName':f'演示自动化 {i+1}','enabled':False} for i in range((page-1)*20,min(page*20,23))]
        return {'data':{'items':rows,'pagination':{'page':page,'pageSize':20,'total':23}}}
    if p.endswith('/runs'):
        page=int(params.get('page',['1'])[0]);rows=[{'runId':f'r{i}','status':'success','startedAt':'2026-10-03','sessionId':f'session-{i}'} for i in range((page-1)*20,min(page*20,22))]
        return {'data':{'items':rows,'pagination':{'page':page,'pageSize':20,'total':22}}}
    if p=='/api/channels/pairing/pending':return {'data':{'items':[],'total':0}}
    if p=='/api/channels':return {'data':{'channels':[]}}
    if p=='/api/plugin-market/installed':return {'data':{'items':[]}}
    if p=='/api/plugin-market':return {'data':{'items':[],'total':0}}
    raise app.DaemonRequestError(404,'fixture_route_not_available')


class Response:
    from email.message import Message
    headers=Message()
    headers['Content-Type']='text/plain'
    payload=b'QW_ARTIFACT_DOWNLOAD_OK\n'
    headers['Content-Length']=str(len(payload))
    def read(self,n):return self.payload[:n]
    def close(self):pass


class Opener:
    def open(self, request, timeout=30):
        DOWNLOADS.append(urlsplit(request.full_url).path)
        print(json.dumps({'fixtureDownload':True,'count':len(DOWNLOADS)}),flush=True)
        return Response()


if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('--port',type=int,default=19839);args=parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='qw-ui-fixture-') as tmp:
        app.ROOT=Path(tmp);app.HOME=app.ROOT/'home';app.SETTINGS=app.HOME/'settings.json';app.DB=app.ROOT/'usage.db'
        app.SECURITY=app.Security(app.ROOT);app.SECURITY.admin='fixture-only-not-a-real-credential';app.SECURITY.viewer='fixture-viewer-not-a-real-credential'
        app.daemon_json=daemon;app.console_op=lambda:(Opener(),{})
        app.deletion_scan=deletion_scan
        app.api_state=lambda:{'daemon':'running','models':[{'id':'fixture/model-a','name':'示例模型 A','provider':'fixture'}],'wakers':[{'id':'w1','name':'阻断删除演示','preference':'fixture/model-a'},{'id':'w2','name':'警告与未知结果演示','preference':'fixture/model-a'}],'whoami':None}
        revision='d'*64
        provider={'name':'fixture','baseUrl':'https://api.example.test/v1','type':'openai-compatible','authType':'bearer','models':['model-a'],'displayNames':['示例模型 A'],'keyConfigured':True,'panelManaged':True,'readOnlyReason':None}
        app.provider_store=lambda:type('FixtureProviders',(),{'summaries':lambda self:([provider],revision),'legacy_backups':lambda self:{'count':0,'totalBytes':0,'oldest':None},'read':lambda self:{'revision':revision,'data':{'providers':{'fixture':{'baseUrl':provider['baseUrl'],'apiKey':'fixture-secret-never-sent','type':'openai-compatible','authType':'bearer','model':'model-a','models':[{'model':'model-a','displayName':'示例模型 A'}]}}}}})()
        app.test_provider=lambda url,key:(True,'合成 fixture 未出网')
        app.runtime_state=lambda:{'desired':{'hotDeploy':False,'embeddingDisabled':True},'effectiveOnPanelRestart':{'hotDeploy':False,'embeddingDisabled':True},'observed':[],'pendingRestart':True,'note':'合成数据，不连接官方服务'}
        print('UI fixture http://127.0.0.1:%d'%args.port,flush=True)
        app.ThreadingHTTPServer(('127.0.0.1',args.port),app.H).serve_forever()
