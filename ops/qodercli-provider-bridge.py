#!/usr/bin/env python3
"""为排除了 user 设置的 SDK 会话补读 Provider；原 CLI 保存在同路径的 .original。"""
import copy
import json
import os
from pathlib import Path
import sys

MAX_SETTINGS_BYTES = 4 * 1024 * 1024


class BridgeError(Exception):
    pass


def option(args, name):
    """CLI 的单值选项以最后一次出现为准；保留 -- 后的普通文本。"""
    value = None
    for index, arg in enumerate(args):
        if arg == '--':
            break
        if arg.startswith(name + '='):
            value = arg[len(name) + 1:]
        elif arg == name:
            if index + 1 >= len(args):
                raise BridgeError('option_value_missing')
            value = args[index + 1]
    return value


def read_settings(path, optional=False):
    try:
        with Path(path).open('rb') as stream:
            raw = stream.read(MAX_SETTINGS_BYTES + 1)
    except FileNotFoundError:
        if optional:
            return {}
        raise BridgeError('settings_file_missing') from None
    if len(raw) > MAX_SETTINGS_BYTES:
        raise BridgeError('settings_file_too_large')
    try:
        value = json.loads(raw)
    except (ValueError, UnicodeDecodeError):
        raise BridgeError('settings_json_invalid') from None
    if not isinstance(value, dict):
        raise BridgeError('settings_object_required')
    return value


def merge(base, overlay):
    result = copy.deepcopy(base)
    for key, value in overlay.items():
        if isinstance(value, dict) and isinstance(result.get(key), dict):
            result[key] = merge(result[key], value)
        else:
            result[key] = copy.deepcopy(value)
    return result


def providers(settings):
    value = settings.get('providers', {})
    if value is None:
        return {}
    if not isinstance(value, dict):
        raise BridgeError('providers_object_required')
    return value


def bridged_settings(args, env, cwd):
    sources = option(args, '--setting-sources')
    config = env.get('QODER_CONFIG_DIR')
    if sources is None or not config or env.get('QODER_SDK_CUSTOM_BASE_URL_BYOK') != '1':
        return None
    scopes = {part.strip() for part in sources.split(',') if part.strip()}
    if 'user' in scopes:
        return None
    inherited = providers(read_settings(Path(config) / 'settings.json', optional=True))
    if not inherited:
        return None
    for scope, filename in [('project', 'settings.json'), ('local', 'settings.local.json')]:
        if scope in scopes:
            inherited = merge(inherited, providers(read_settings(Path(cwd) / '.qoder' / filename, optional=True)))
    flag = option(args, '--settings')
    explicit = {}
    if flag is not None:
        if flag.lstrip().startswith('{'):
            try:
                explicit = json.loads(flag)
            except ValueError:
                raise BridgeError('inline_settings_json_invalid') from None
        else:
            explicit = read_settings(flag)
        if not isinstance(explicit, dict):
            raise BridgeError('inline_settings_object_required')
    return merge({'providers': inherited}, explicit)


def replace_settings(args, path):
    result = []
    index = 0
    while index < len(args):
        arg = args[index]
        if arg == '--':
            return result + ['--settings', path] + args[index:]
        if arg == '--settings':
            index += 2
            continue
        if not arg.startswith('--settings='):
            result.append(arg)
        index += 1
    return result + ['--settings', path]


def main():
    original = Path(str(Path(__file__).resolve()) + '.original')
    if not original.is_file() or not os.access(original, os.X_OK):
        raise BridgeError('original_cli_missing')
    args = sys.argv[1:]
    settings = bridged_settings(args, os.environ, Path.cwd())
    if settings is not None:
        if not hasattr(os, 'memfd_create'):
            raise BridgeError('linux_memfd_required')
        fd = os.memfd_create('qoderwake-provider-settings', flags=0)
        os.fchmod(fd, 0o600)
        with os.fdopen(os.dup(fd), 'wb') as stream:
            stream.write(json.dumps(settings, ensure_ascii=False).encode('utf-8'))
        os.lseek(fd, 0, os.SEEK_SET)
        os.set_inheritable(fd, True)
        args = replace_settings(args, '/proc/self/fd/' + str(fd))
    os.execv(str(original), [str(original), *args])


if __name__ == '__main__':
    try:
        main()
    except (BridgeError, OSError) as error:
        code = str(error) if isinstance(error, BridgeError) else 'settings_or_cli_unavailable'
        print('Provider bridge: ' + code + '. Check the original CLI and JSON settings.', file=sys.stderr)
        sys.exit(78)
