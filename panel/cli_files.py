"""操作工作台的临时输入/输出文件；浏览器只能下载自己创建的文件槽。"""
import base64
import hashlib
import os
from pathlib import Path
import re
import stat
import threading
import uuid

from panel_accounts import AccessError

MAX_FILE = 64 * 1024 * 1024


class OperationFiles:
    def __init__(self, root):
        self.root = Path(root)
        self.root.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.lock = threading.RLock()

    def directory(self, identity):
        if identity['role'] != 'admin':
            raise AccessError(403, 'permission_denied')
        path = self.root/hashlib.sha256(identity['principal'].encode()).hexdigest()
        path.mkdir(exist_ok=True, mode=0o700)
        return path

    def create(self, identity, name, output=False):
        if not isinstance(name, str) or not re.fullmatch(r'[^/\\\x00-\x1f]{1,150}', name) or name in ('.','..'):
            raise ValueError('invalid_cli_filename')
        directory = self.directory(identity)
        if sum(1 for p in directory.iterdir()) >= 100:
            raise AccessError(409,'cli_file_slots_full')
        folder = directory/uuid.uuid4().hex
        folder.mkdir(mode=0o700)
        (folder/'files').mkdir(mode=0o700)
        path = folder/'files'/name
        if not output:
            fd = os.open(path, os.O_CREAT|os.O_EXCL|os.O_WRONLY, 0o600)
            os.close(fd)
        (folder/'name').write_text(name)
        os.chmod(folder/'name',0o600)
        (folder/'type').write_text('output' if output else 'input')
        os.chmod(folder/'type',0o600)
        # 引用不是宿主机路径；执行前在当前主体下解析。
        return {'id':folder.name,'name':name,'reference':'@panel-file:'+folder.name,'output':output}

    def list(self, identity):
        result=[]
        for folder in self.directory(identity).iterdir():
            if not folder.is_dir() or folder.is_symlink():continue
            try:
                path=self.path(identity,folder.name)
                result.append({'id':folder.name,'name':path.name,'reference':'@panel-file:'+folder.name,
                    'output':(folder/'type').read_text()=='output','ready':path.is_file()})
            except (OSError,ValueError,AccessError):continue
        return result

    def path(self, identity, file_id):
        if not isinstance(file_id,str) or not re.fullmatch('[0-9a-f]{32}',file_id):
            raise ValueError('invalid_cli_file')
        folder = self.directory(identity)/file_id
        if folder.is_symlink() or not (folder/'name').is_file():
            raise AccessError(404,'cli_file_not_found')
        name = (folder/'name').read_text()
        if Path(name).name != name or name in ('.','..'):
            raise ValueError('invalid_cli_file')
        path = folder/'files'/name
        if (folder/'files').is_symlink() or path.is_symlink():
            raise AccessError(403,'cli_file_rejected')
        return path

    def append(self, identity, file_id, offset, encoded):
        if type(offset) is not int or offset < 0 or not isinstance(encoded,str) or len(encoded)>48000:
            raise ValueError('invalid_cli_upload')
        try:
            data = base64.b64decode(encoded,validate=True)
        except ValueError:
            raise ValueError('invalid_cli_upload') from None
        with self.lock:
            path = self.path(identity,file_id)
            fd = os.open(path,os.O_WRONLY|os.O_APPEND|getattr(os,'O_NOFOLLOW',0))
            with os.fdopen(fd,'ab') as stream:
                info = os.fstat(stream.fileno())
                if not stat.S_ISREG(info.st_mode) or info.st_size != offset:
                    raise AccessError(409,'cli_upload_offset_changed')
                if offset+len(data)>MAX_FILE:
                    raise AccessError(413,'cli_file_too_large')
                stream.write(data)
        return {'ok':True,'offset':offset+len(data)}

    def resolve(self, identity, value):
        if isinstance(value,dict):
            return {k:self.resolve(identity,v) for k,v in value.items()}
        if isinstance(value,list):
            return [self.resolve(identity,v) for v in value]
        if isinstance(value,str) and value.startswith('@panel-file:'):
            return str(self.path(identity,value.removeprefix('@panel-file:')))
        return value

    def read(self, identity, file_id):
        path = self.path(identity,file_id)
        try:
            fd = os.open(path,os.O_RDONLY|getattr(os,'O_NOFOLLOW',0))
        except FileNotFoundError:
            raise AccessError(404,'cli_file_not_ready') from None
        with os.fdopen(fd,'rb') as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_size>MAX_FILE:
                raise AccessError(413,'cli_file_too_large')
            return stream.read(MAX_FILE+1),path.name

    def delete(self, identity, file_id):
        with self.lock:
            path=self.path(identity,file_id)
            if path.exists():path.unlink()
            path.parent.rmdir()
            (path.parent.parent/'name').unlink()
            (path.parent.parent/'type').unlink()
            path.parent.parent.rmdir()
        return {'ok':True}
