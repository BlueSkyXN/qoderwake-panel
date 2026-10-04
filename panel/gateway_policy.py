"""Shared validation for Panel and the loopback control-plane gateway."""
import copy
import hashlib
import json
import re
from urllib.parse import parse_qsl, urlsplit

MAX_BODY = 1024 * 1024
METHODS = {'GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE'}
DEFAULT_CFG = {'mode': 'observe', 'token_policy': 'balanced', 'sink_post_prefixes': [],
               'token_allow_prefixes': [], 'rules': [], 'audit_paths': []}


def static_path(value):
    return (isinstance(value, str) and len(value) <= 2048 and
            re.fullmatch(r'/[A-Za-z0-9/_-]*', value) is not None and '//' not in value)


def validate_cfg(value):
    if not isinstance(value, dict) or set(value) - set(DEFAULT_CFG) or 'mode' not in value:
        raise ValueError('invalid_gateway_config')
    cfg = copy.deepcopy(DEFAULT_CFG)
    cfg.update(copy.deepcopy(value))
    if cfg['mode'] not in ('observe', 'enforce', 'strict') or cfg['token_policy'] != 'balanced':
        raise ValueError('invalid_gateway_mode')
    for key in ('sink_post_prefixes', 'token_allow_prefixes', 'audit_paths'):
        paths = cfg[key]
        if not isinstance(paths, list) or len(paths) > 200 or any(not static_path(p) for p in paths):
            raise ValueError('invalid_gateway_paths')
        if key != 'audit_paths' and '/' in paths:
            raise ValueError('gateway_root_prefix_rejected')
    rules = cfg['rules']
    if not isinstance(rules, list) or len(rules) > 200:
        raise ValueError('invalid_gateway_rules')
    seen = set()
    for rule in rules:
        if not isinstance(rule, dict) or set(rule) != {'method', 'path', 'query', 'headers', 'max_body'}:
            raise ValueError('invalid_gateway_rule')
        if not isinstance(rule['method'], str) or rule['method'] not in METHODS or not static_path(rule['path']):
            raise ValueError('invalid_gateway_rule_path')
        identity = (rule['method'], rule['path'])
        if identity in seen:
            raise ValueError('duplicate_gateway_rule')
        seen.add(identity)
        for key in ('query', 'headers'):
            values = rule[key]
            if not isinstance(values, list) or len(values) > 100 or any(
                    not isinstance(x, str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,80}', x) for x in values):
                raise ValueError('invalid_gateway_rule_fields')
        if type(rule['max_body']) is not int or not 0 <= rule['max_body'] <= MAX_BODY:
            raise ValueError('invalid_gateway_body_limit')
    return cfg


def policy_hash(cfg):
    return hashlib.sha256(json.dumps(validate_cfg(cfg), sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def decision(cfg, method, target, size):
    parsed = urlsplit(target)
    if parsed.scheme or parsed.netloc or parsed.fragment or not target.startswith('/') or target.startswith('//'):
        return 'deny', None
    path = parsed.path
    if '%' in path or '\\' in path or any(part in ('.', '..') for part in path.split('/')):
        return 'deny', None
    if cfg['mode'] == 'strict':
        for rule in cfg['rules']:
            if rule['method'] == method and rule['path'] == path:
                query = parse_qsl(parsed.query, keep_blank_values=True, max_num_fields=100)
                if size <= rule['max_body'] and all(key in rule['query'] for key, value in query):
                    return 'forward', rule
        return 'deny', None
    if cfg['mode'] == 'enforce' and method in ('POST', 'PUT', 'PATCH', 'DELETE'):
        def matches(prefix):
            return path == prefix or path.startswith(prefix.rstrip('/') + '/')
        if any(matches(p) for p in cfg['sink_post_prefixes']) and not any(matches(p) for p in cfg['token_allow_prefixes']):
            return 'sink', None
    return 'forward', None
