#!/usr/bin/env python3
"""Start the built Panel with disposable data and check health, embedded UI and login."""
import argparse
import http.cookiejar
import json
import os
from pathlib import Path
import re
import selectors
import subprocess
import tempfile
from urllib.request import Request,build_opener,HTTPCookieProcessor,ProxyHandler

ROOT=Path(__file__).resolve().parents[1]

def smoke(binary):
    binary=binary.resolve()
    expected=re.search(r'const Version = "([^"]+)"',(ROOT/'internal/server/server.go').read_text()).group(1)
    with tempfile.TemporaryDirectory(prefix='panel-smoke-') as tmp:
        root=Path(tmp);home=root/'wake';home.mkdir()
        env={k:v for k,v in os.environ.items() if not k.startswith('QW_')}
        env.update(QW_ROOT=str(root/'data'),QW_HOME=str(home),QW_DAEMON_URL='http://127.0.0.1:1')
        password='synthetic-release-smoke-password'
        subprocess.run([str(binary),'init-admin','--username','smoke','--password-stdin'],env=env,input=password+'\n',capture_output=True,text=True,check=True)
        process=subprocess.Popen([str(binary),'serve','--bind','127.0.0.1','--port','0'],env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(process.stdout,selectors.EVENT_READ)
                if not selector.select(timeout=20):raise RuntimeError('Panel did not become ready')
                line=process.stdout.readline()
            match=re.search(r'http://127\.0\.0\.1:\d+',line)
            if not match:raise RuntimeError('Panel startup did not return a loopback address')
            base=match.group();client=build_opener(ProxyHandler({}),HTTPCookieProcessor(http.cookiejar.CookieJar()))
            with client.open(base+'/api/health',timeout=10) as response:
                health=json.load(response)
            if health.get('version')!=expected:raise RuntimeError('Health version mismatch')
            with client.open(base+'/',timeout=10) as response:html=response.read().decode()
            asset=re.search(r'src="(/assets/[^\"]+\.js)"',html)
            if not asset:raise RuntimeError('Embedded UI entry missing')
            with client.open(base+asset.group(1),timeout=10) as response:
                if len(response.read())<100:raise RuntimeError('Embedded UI asset missing')
            body=json.dumps({'username':'smoke','password':password}).encode()
            with client.open(Request(base+'/api/login',data=body,headers={'Content-Type':'application/json'}),timeout=10) as response:
                if json.load(response).get('level')!='admin':raise RuntimeError('Login failed')
            with client.open(base+'/api/whoami',timeout=10) as response:
                if json.load(response).get('username')!='smoke':raise RuntimeError('Session readback failed')
            print('PASS: binary health, embedded UI and synthetic account login ('+expected+')')
        finally:
            process.terminate()
            try:process.wait(timeout=10)
            except subprocess.TimeoutExpired:process.kill();process.wait()

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('binary',type=Path)
    smoke(parser.parse_args().binary)
