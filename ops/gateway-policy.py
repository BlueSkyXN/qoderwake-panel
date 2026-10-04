#!/usr/bin/env python3
"""Generate and validate strict proxy policy from redacted observations; never activates it."""
import argparse
import json
import os
import re
import tempfile
from pathlib import Path

METHODS = {'GET','HEAD','POST','PUT','PATCH','DELETE'}
SAFE_HEADERS = {'accept','content-type','authorization','x-qoderwake-frontend-session-client'}


def read_records(path):
    rows=[]
    with Path(path).open(errors='replace') as stream:
        for number,line in enumerate(stream,1):
            try: row=json.loads(line)
            except ValueError: continue
            path_value=row.get('path')
            if row.get('action') != 'forward' or row.get('forward_attempted') is not True:
                continue
            if (row.get('method') not in METHODS or not isinstance(path_value,str) or
                    not re.fullmatch(r'/[A-Za-z0-9/_-]*',path_value)):
                continue
            rows.append((number,row))
    return rows


def candidates(rows):
    grouped={}
    for number,row in rows:
        key=(row['method'],row['path'])
        item=grouped.setdefault(key,{'method':key[0],'path':key[1],'observed':0,'forwarded':0,'max_body':0,'statuses':{},'firstLine':number,'lastLine':number})
        item['observed']+=1;item['forwarded']+=1;item['lastLine']=number
        size=row.get('q',0);item['max_body']=max(item['max_body'],size if type(size) is int and size>=0 else 0)
        status=str(row.get('status',0));item['statuses'][status]=item['statuses'].get(status,0)+1
    return sorted(grouped.values(),key=lambda x:(x['method'],x['path']))


def make_policy(items, approvals):
    approved={(x['method'],x['path']):x for x in approvals}
    rules=[]
    for item in items:
        review=approved.get((item['method'],item['path']))
        if not review or review.get('allow') is not True:
            continue
        headers=review.get('headers',[]);query=review.get('query',[])
        if (not isinstance(headers,list) or any(h not in SAFE_HEADERS for h in headers) or
                not isinstance(query,list) or any(not isinstance(q,str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,80}',q) for q in query)):
            raise ValueError('invalid approval fields')
        body=review.get('max_body',item['max_body'])
        if type(body) is not int or body < item['max_body'] or body > 1024*1024:
            raise ValueError('invalid approved body limit')
        rules.append({'method':item['method'],'path':item['path'],'query':query,'headers':headers,'max_body':body})
    if not rules:
        raise ValueError('refusing empty strict policy')
    return {'mode':'strict','token_policy':'balanced','sink_post_prefixes':[],
            'audit_paths':[rule['path'] for rule in rules],'rules':rules}


def atomic(path,value):
    path=Path(path);path.parent.mkdir(parents=True,exist_ok=True)
    if path.is_symlink():raise ValueError('symlink rejected')
    fd,tmp=tempfile.mkstemp(prefix='.gw-policy-',dir=path.parent)
    try:
        with os.fdopen(fd,'w') as stream:
            json.dump(value,stream,ensure_ascii=False,indent=2);stream.flush();os.fsync(stream.fileno())
        os.chmod(tmp,0o600);os.replace(tmp,path)
    finally:
        if os.path.exists(tmp):os.unlink(tmp)


def main():
    parser=argparse.ArgumentParser(description=__doc__);sub=parser.add_subparsers(dest='command',required=True)
    observe=sub.add_parser('candidates');observe.add_argument('log');observe.add_argument('output')
    build=sub.add_parser('build');build.add_argument('candidates');build.add_argument('approvals');build.add_argument('output')
    args=parser.parse_args()
    if args.command=='candidates':
        value={'generatedFrom':'redacted gateway log','candidates':candidates(read_records(args.log))}
    else:
        source=json.loads(Path(args.candidates).read_text()).get('candidates',[])
        approvals=json.loads(Path(args.approvals).read_text()).get('approvals',[])
        value=make_policy(source,approvals)
    atomic(args.output,value);print(json.dumps({'ok':True,'count':len(value.get('rules',value.get('candidates',[]))),'output':str(args.output)}))


if __name__=='__main__':main()
