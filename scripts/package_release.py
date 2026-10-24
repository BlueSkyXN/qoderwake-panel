#!/usr/bin/env python3
"""Package versioned binaries or exact Git source, then write a release manifest and checksums."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile

from third_party_notices import collect

ROOT=Path(__file__).resolve().parents[1]

def version():
    match=re.search(r'const Version = "([^"]+)"',(ROOT/'internal/server/server.go').read_text())
    return match.group(1)

def revision():
    subprocess.run(['git','diff','--quiet','HEAD','--'],cwd=ROOT,check=True)
    return subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip()

def digest(path):
    with path.open('rb') as file:return hashlib.file_digest(file,'sha256').hexdigest()

def archive(path,prefix,entries,epoch):
    with path.open('xb') as file,gzip.GzipFile(filename='',fileobj=file,mode='wb',mtime=epoch) as compressed,tarfile.open(fileobj=compressed,mode='w') as tar:
        for name,data,mode in entries:
            item=tarfile.TarInfo(prefix+'/'+name);item.size=len(data);item.mode=mode;item.mtime=epoch
            tar.addfile(item,io.BytesIO(data))

def run(args):
    tag='v'+version();sha=revision()
    if args.tag and args.tag!=tag:raise ValueError('Tag must match the server version')
    epoch=int(subprocess.check_output(['git','show','-s','--format=%ct','HEAD'],cwd=ROOT,text=True))
    out=args.output.resolve();out.mkdir(parents=True,exist_ok=True)
    if args.platform:
        if not args.binary:raise ValueError('--binary is required with --platform')
        target_os,target_arch=args.platform.split('-')
        metadata=subprocess.check_output(['go','version','-m',str(args.binary.resolve())],text=True)
        for expected in ['GOOS='+target_os,'GOARCH='+target_arch,'vcs.revision='+sha,'vcs.modified=false']:
            if expected not in metadata:raise ValueError('Binary build metadata mismatch: '+expected)
        entries=[('panel',args.binary.read_bytes(),0o755),('LICENSE',(ROOT/'LICENSE').read_bytes(),0o644),('README.md',(ROOT/'README.md').read_bytes(),0o644)]
        for path in sorted((ROOT/'docs').rglob('*.md')):
            entries.append((path.relative_to(ROOT).as_posix(),path.read_bytes(),0o644))
        entries.append(('THIRD_PARTY_NOTICES.txt',collect().encode(),0o644))
        info={'version':version(),'sourceCommit':sha,'platform':args.platform,'binarySHA256':digest(args.binary)}
        entries.append(('BUILD-INFO.json',(json.dumps(info,indent=2)+'\n').encode(),0o644))
        name='qoderwake-panel-'+tag+'-'+args.platform
        archive(out/(name+'.tar.gz'),name,entries,epoch)
    if args.source:
        raw=subprocess.check_output(['git','archive','--format=tar','--prefix=qoderwake-panel-'+tag+'/',sha],cwd=ROOT)
        with (out/('qoderwake-panel-'+tag+'-source.tar.gz')).open('xb') as file,gzip.GzipFile(filename='',fileobj=file,mode='wb',mtime=epoch) as zipped:zipped.write(raw)
    if args.finalize:
        expected=['qoderwake-panel-'+tag+'-'+suffix+'.tar.gz' for suffix in ['darwin-arm64','linux-amd64','source']]
        files=sorted(out.glob('*.tar.gz'))
        if sorted(p.name for p in files)!=sorted(expected):raise ValueError('Expected exactly both platform archives and the matching source archive')
        manifest={'version':version(),'sourceCommit':sha,'artifacts':[{'name':p.name,'bytes':p.stat().st_size,'sha256':digest(p)} for p in files]}
        path=out/'RELEASE-MANIFEST.json';path.write_text(json.dumps(manifest,indent=2)+'\n');files.append(path)
        (out/'SHA256SUMS').write_text(''.join(digest(p)+'  '+p.name+'\n' for p in files))

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--tag')
    parser.add_argument('--platform',choices=['darwin-arm64','linux-amd64'])
    parser.add_argument('--binary',type=Path)
    parser.add_argument('--source',action='store_true')
    parser.add_argument('--finalize',action='store_true')
    parser.add_argument('--output',type=Path,default=ROOT/'dist')
    args=parser.parse_args()
    if not(args.platform or args.source or args.finalize):parser.error('choose --platform, --source or --finalize')
    run(args)
