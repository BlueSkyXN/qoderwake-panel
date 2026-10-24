#!/usr/bin/env python3
"""Collect dependency licenses from the installed, locked Go and frontend dependencies."""
import json
import os
from pathlib import Path
import re
import subprocess

ROOT=Path(__file__).resolve().parents[1]

def json_stream(text):
    decoder=json.JSONDecoder()
    while text.strip():
        text=text.lstrip()
        value,end=decoder.raw_decode(text)
        yield value
        text=text[end:]

def license_files(directory):
    return sorted(p for p in directory.iterdir() if p.is_file() and re.match(r'(?i)^(LICENSE|LICENCE|COPYING|NOTICE)(?:[.\-_]|$)',p.name))

def collect():
    sections=['Third-party notices for QoderWake Panel\n\nThe project license applies to Panel source. Dependencies retain their own licenses.']
    missing=[]
    def add(label,directory):
        files=license_files(directory)
        if not files and label=='react-remove-scroll-bar 2.3.8':
            files=[ROOT/'licenses/react-remove-scroll-bar-2.3.8.txt']
        if not files:missing.append(label);return
        sections.append('\n\n'+'='*72+'\n'+label+'\n'+'='*72)
        for path in files:
            sections.append('\n'+path.name+'\n\n'+path.read_text(encoding='utf-8'))
    goroot=Path(subprocess.check_output(['go','env','GOROOT'],cwd=ROOT,text=True).strip())
    standard=next((p for p in [goroot,*list(goroot.parents)[:3]] if (p/'LICENSE').is_file()),None)
    if standard is None:missing.append('Go standard library')
    else:add('Go standard library',standard)
    modules=subprocess.check_output(['go','list','-m','-json','all'],cwd=ROOT,text=True)
    for module in json_stream(modules):
        if not module.get('Main'):
            directory=module.get('Dir')
            if not directory:missing.append(module['Path']);continue
            add(module['Path']+' '+module['Version'],Path(directory))
    packages=subprocess.check_output(['npm','ls','--omit=dev','--all','--parseable'],cwd=ROOT/'web',text=True)
    seen=set()
    for raw in packages.splitlines():
        path=Path(raw)
        if path.resolve()==(ROOT/'web').resolve():continue
        data=json.loads((path/'package.json').read_text())
        label=data['name']+' '+data['version']
        if label not in seen:add(label,path);seen.add(label)
    if missing:raise RuntimeError('License files missing: '+', '.join(missing))
    return '\n'.join(sections)+'\n'

if __name__=='__main__':
    print(collect(),end='')
