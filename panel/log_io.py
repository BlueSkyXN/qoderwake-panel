"""Bounded, read-only windows over complete records in append-only logs."""
import os
from pathlib import Path
import stat
from gateway_runtime import _open_directory


def tail_records(paths, max_lines=5000, max_bytes=1024 * 1024):
    records, total_bytes, scanned_bytes, sources = [], 0, 0, 0
    truncated = False
    for path in paths:
        if len(records) >= max_lines or scanned_bytes >= max_bytes:
            truncated = True
            break
        path = Path(path)
        try:
            directory = _open_directory(path.parent)
        except FileNotFoundError:
            continue
        try:
            fd = os.open(path.name, os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0) | os.O_NONBLOCK,
                         dir_fd=directory)
        except FileNotFoundError:
            continue
        finally:
            os.close(directory)
        with os.fdopen(fd, 'rb') as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
                raise ValueError('unsafe_log_file')
            total_bytes += info.st_size
            sources += 1
            position = info.st_size
            chunks = []
            newlines = 0
            remaining = max_lines - len(records)
            while position and scanned_bytes < max_bytes and newlines <= remaining:
                size = min(position, 16384, max_bytes - scanned_bytes)
                position -= size
                stream.seek(position)
                data = stream.read(size)
                scanned_bytes += len(data)
                chunks.append(data)
                newlines += data.count(b'\n')
                if len(data) != size:
                    raise ValueError('log_changed_during_read')
            raw = b''.join(reversed(chunks))
            lines = raw.split(b'\n')
            if position:
                lines = lines[1:]
                truncated = True
            lines = lines[:-1]
            if len(lines) > remaining:
                truncated = True
            records = [line.decode('utf-8', errors='replace') for line in lines[-remaining:]] + records
    return {'lines': records, 'bytesRead': scanned_bytes, 'fileBytes': total_bytes,
            'sources': sources, 'truncated': truncated, 'maxBytes': max_bytes,
            'maxLines': max_lines}
