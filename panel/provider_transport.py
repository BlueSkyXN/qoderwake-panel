"""Pinned DNS for panel-originated provider probes; does not govern daemon traffic."""
import http.client
import ipaddress
import json
import socket
import ssl
from urllib.parse import urlsplit


def addresses(host, port):
    values = socket.getaddrinfo(host, port, type=socket.SOCK_STREAM)
    if not values:
        raise ValueError('provider_dns_empty')
    loopback = host in ('localhost', '127.0.0.1', '::1')
    for family, kind, protocol, canon, address in values:
        ip = ipaddress.ip_address(address[0])
        if isinstance(ip, ipaddress.IPv6Address) and ip.ipv4_mapped:
            ip = ip.ipv4_mapped
        if not (ip.is_loopback if loopback else ip.is_global):
            raise ValueError('provider_private_destination_rejected')
    return values


def connect_pinned(host, port, timeout, resolved):
    last = None
    for family, kind, protocol, canon, address in resolved:
        sock = socket.socket(family, kind, protocol)
        sock.settimeout(timeout)
        try:
            sock.connect(address)
            return sock
        except OSError as error:
            last = error
            sock.close()
    raise last or OSError('connect_failed')


def probe(url, key):
    if not isinstance(key, str) or not key or len(key) > 2048 or '\r' in key or '\n' in key:
        raise ValueError('invalid_provider_key')
    parsed = urlsplit(url)
    port = parsed.port or (443 if parsed.scheme == 'https' else 80)
    resolved = addresses(parsed.hostname, port)
    raw = connect_pinned(parsed.hostname, port, 12, resolved)
    conn = http.client.HTTPConnection(parsed.hostname, port, timeout=12)
    try:
        conn.sock = ssl.create_default_context().wrap_socket(raw, server_hostname=parsed.hostname) if parsed.scheme == 'https' else raw
        path = (parsed.path.rstrip('/') or '') + '/models'
        conn.request('GET', path, headers={'Authorization':'Bearer '+key, 'Accept':'application/json'})
        response = conn.getresponse()
        if response.status != 200:
            return False, 'HTTP %d（重定向不跟随）' % response.status
        body = response.read(1024*1024+1)
        if len(body) > 1024*1024:
            raise ValueError('provider_response_too_large')
        value = json.loads(body)
        items = value.get('data', []) if isinstance(value, dict) else value
        if not isinstance(items, list):
            raise ValueError('provider_invalid_response')
        return True, 'HTTP 200 · 可用模型: ' + ', '.join(str(m.get('id',''))[:200] for m in items[:8] if isinstance(m, dict))
    finally:
        conn.close()
        raw.close()
