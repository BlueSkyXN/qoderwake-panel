"""Incremental turn counts with bounded reads and persisted file cursors."""
import datetime as dt
import hashlib
import os
from pathlib import Path
import re
import sqlite3
import stat

MAX_SCAN_BYTES = 8 * 1024 * 1024
MAX_FILE_BYTES = 2 * 1024 * 1024
MAX_LINE_BYTES = 65536
GUARD_BYTES = 4096


def initialize(path):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | getattr(os, 'O_NOFOLLOW', 0), 0o600)
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode) or os.fstat(fd).st_nlink != 1:
            raise ValueError('unsafe_usage_database')
        os.fchmod(fd, 0o600)
    finally:
        os.close(fd)
    with sqlite3.connect(path) as con:
        con.executescript('''
            CREATE TABLE IF NOT EXISTS usage_events(event_key TEXT PRIMARY KEY, day TEXT, model TEXT, run TEXT, timestamp TEXT);
            CREATE TABLE IF NOT EXISTS usage_files(path TEXT PRIMARY KEY, size INTEGER, mtime INTEGER);
            CREATE TABLE IF NOT EXISTS usage_cursors(
                path TEXT PRIMARY KEY, device INTEGER, inode INTEGER, offset INTEGER,
                prefix TEXT, suffix TEXT, discarding INTEGER, skipped INTEGER);
            CREATE TABLE IF NOT EXISTS usage_occurrences(
                path TEXT, line_hash TEXT, count INTEGER, PRIMARY KEY(path,line_hash));
        ''')
    con.close()


def guards(stream, offset):
    size = min(offset, GUARD_BYTES)
    stream.seek(0)
    prefix = stream.read(size)
    stream.seek(offset - size)
    suffix = stream.read(size)
    return hashlib.sha256(prefix).hexdigest(), hashlib.sha256(suffix).hexdigest(), len(prefix) + len(suffix)


def scan_file(con, path, budget):
    name = str(path)
    fd = os.open(path, os.O_RDONLY | os.O_NONBLOCK | getattr(os, 'O_NOFOLLOW', 0))
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise ValueError('unsafe_usage_log')
        previous = con.execute('SELECT device,inode,offset,prefix,suffix,discarding,skipped FROM usage_cursors WHERE path=?', (name,)).fetchone()
        offset, discarding, skipped, checked = 0, False, 0, 0
        if previous and previous[:2] == (info.st_dev, info.st_ino) and previous[2] <= info.st_size:
            prefix, suffix, checked = guards(stream, previous[2])
            if (prefix, suffix) == previous[3:5]:
                offset, discarding, skipped = previous[2], bool(previous[5]), previous[6]
        if not offset:
            con.execute('DELETE FROM usage_occurrences WHERE path=?', (name,))
        stream.seek(offset)
        read = 0
        while read < budget:
            position = stream.tell()
            raw = stream.readline(min(MAX_LINE_BYTES + 1, budget - read))
            read += len(raw)
            if not raw:
                break
            if discarding:
                offset = stream.tell()
                discarding = not raw.endswith(b'\n')
                continue
            if not raw.endswith(b'\n'):
                if len(raw) > MAX_LINE_BYTES:
                    discarding = True
                    skipped += 1
                    offset = stream.tell()
                    continue
                stream.seek(position)
                break
            offset = stream.tell()
            if len(raw) > MAX_LINE_BYTES:
                skipped += 1
                continue
            line = raw.decode('utf-8', errors='replace').strip()
            match = re.search(r'turn\.started\s+model="([^"]+)"', line)
            if not match:
                continue
            line_hash = hashlib.sha256(line.encode()).hexdigest()
            old = con.execute('SELECT count FROM usage_occurrences WHERE path=? AND line_hash=?', (name, line_hash)).fetchone()
            occurrence = old[0] if old else 0
            key = hashlib.sha256((path.parent.name + '\0' + line + '\0' + str(occurrence)).encode()).hexdigest()
            stamp = re.search(r'(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2})', line)
            stamp = stamp.group(1) if stamp else dt.datetime.fromtimestamp(info.st_mtime).isoformat(timespec='seconds')
            con.execute('INSERT OR IGNORE INTO usage_events VALUES(?,?,?,?,?)', (key, stamp[:10], match.group(1), path.parent.name, stamp))
            con.execute('INSERT OR REPLACE INTO usage_occurrences VALUES(?,?,?)', (name, line_hash, occurrence + 1))
        prefix, suffix, extra = guards(stream, offset)
        con.execute('INSERT OR REPLACE INTO usage_cursors VALUES(?,?,?,?,?,?,?,?)', (name, info.st_dev, info.st_ino, offset, prefix, suffix, int(discarding), skipped))
        return {'bytesRead': read, 'guardBytesRead': checked + extra,
                'pending': offset < os.fstat(stream.fileno()).st_size, 'skippedLines': skipped}


def usage_data(database, runs):
    initialize(database)
    scan = {'bytesRead': 0, 'guardBytesRead': 0, 'pendingFiles': 0,
            'unavailableFiles': 0, 'skippedLines': 0, 'maxBytes': MAX_SCAN_BYTES}
    con = sqlite3.connect(database, timeout=10)
    try:
        paths = sorted(Path(runs).glob('*/qodercli.log'))
        with con:
            for index, path in enumerate(paths):
                if scan['bytesRead'] >= MAX_SCAN_BYTES:
                    scan['pendingFiles'] += len(paths) - index
                    break
                con.execute('SAVEPOINT usage_file')
                try:
                    value = scan_file(con, path, min(MAX_FILE_BYTES, MAX_SCAN_BYTES - scan['bytesRead']))
                    for key in ('bytesRead', 'guardBytesRead', 'skippedLines'):
                        scan[key] += value[key]
                    scan['pendingFiles'] += int(value['pending'])
                except (OSError, ValueError):
                    con.execute('ROLLBACK TO usage_file')
                    scan['unavailableFiles'] += 1
                finally:
                    con.execute('RELEASE usage_file')
        per_model = con.execute('SELECT model,COUNT(*) FROM usage_events GROUP BY model ORDER BY COUNT(*) DESC').fetchall()
        per_day = con.execute("SELECT day,COUNT(*) FROM usage_events WHERE day >= date('now','-29 days') GROUP BY day ORDER BY day").fetchall()
        recent = [{'day': ts, 'run': run, 'models': [model]} for ts, run, model in con.execute('SELECT timestamp,run,model FROM usage_events ORDER BY timestamp DESC LIMIT 12')]
        scan['complete'] = not any(scan[key] for key in ('pendingFiles', 'unavailableFiles', 'skippedLines'))
        return {'perModel': per_model, 'perDay': per_day, 'recent': recent, 'scan': scan,
                'note': '只统计已扫描日志的回合，不是 token、费用或完整历史。按游标增量扫描；未读完、未换行、超长或不可读日志会标明不完整。轮转或截断保留既有计数；首尾校验不等于检测任意中间改写。'}
    finally:
        con.close()
