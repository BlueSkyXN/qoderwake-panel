"""官方命令的结构化参考。参数来自逐命令 --help，不生成 HTTP 路由。"""
import hashlib
import json
from pathlib import Path
import re

from panel_accounts import AccessError

REFERENCE = Path(__file__).with_name('config') / 'cli-reference.json'
POLICY = Path(__file__).with_name('config') / 'cli-policy.json'
GROUP_NAMES = {
    'start':'服务启动', 'stop':'服务停止', 'restart':'服务重启', 'status':'服务状态',
    'readiness':'运行就绪', 'portal':'官方控制台', 'config':'应用配置', 'proxy':'网络代理',
    'settings':'用户偏好', 'storage':'存储迁移', 'backup':'官方备份', 'plugin':'本地插件',
    'waker':'Waker 与模板', 'local':'本机员工', 'project':'项目', 'mcp':'MCP 服务器',
    'skill':'技能', 'permission':'Waker 权限', 'channel':'IM 通道与配对', 'extension':'扩展运行时',
    'session':'会话', 'group':'群协作', 'sop':'协作规程', 'messages':'会话消息',
    'atwaker':'IM 工作任务', 'todo':'待办', 'goal':'协作目标', 'runs':'运行任务', 'trace':'任务链路',
    'automation':'自动化', 'memory':'记忆生命周期', 'login':'官方登录', 'logout':'官方退出',
    'whoami':'官方身份', 'version':'版本', 'update':'官方升级', 'log':'日志', 'feedback':'反馈报告',
    'teams':'团队', 'workflow':'WakerFlow', 'models':'模型', 'account':'官方账号',
    'connector':'连接器', 'board':'任务看板', 'human-action':'人工处理', 'knowledge':'知识库',
    'remote':'跨设备员工', 'auth':'外部认证', 'diagnostics':'诊断',
}
SECRET = re.compile(r'(?:password|passwd|secret|token|api.?key|authorization|cookie|private.?key)(?:$|[-_](?:file|path)$)', re.I)


def help_entries(value, heading):
    """Commander 的条目按两空格起始，多行说明按缩进连接。"""
    active, entries = False, []
    for line in value.splitlines():
        if line == heading + ':':
            active = True
            continue
        if not active:
            continue
        if line and not line.startswith(' '):
            break
        match = re.match(r'^  (\S.*?)\s{2,}(\S.*)$', line)
        if match:
            entries.append([match[1], match[2]])
        elif line.startswith('  ') and line.strip():
            if line.startswith('    ') and entries:
                entries[-1][1] += ' ' + line.strip()
            else:
                entries.append([line.strip(), ''])
    return entries


def parse_help(path, value):
    first = value.splitlines()[0] if value else ''
    usage = re.match(r'^Usage: \S+\s*(.*)', first)
    suffix = usage.group(1) if usage else ''
    for part in path:
        word, _, rest = suffix.partition(' ')
        if part not in word.split('|'):
            raise ValueError('cli_help_mismatch')
        suffix = rest.lstrip()
    args = []
    descriptions = dict(help_entries(value, 'Arguments'))
    for match in re.finditer(r'(<[^>]+>|\[[^\]]+\])', suffix):
        token = match.group(0)
        name = token[1:-1].removesuffix('...')
        if name in ('options', 'command'):
            continue
        args.append({'name': name, 'required':token.startswith('<'),
                     'variadic':'...' in token, 'description':descriptions.get(name, '')})
    opts = []
    for declaration, description in help_entries(value, 'Options'):
        flags = re.findall(r'(?<!\S)(--?[A-Za-z0-9][A-Za-z0-9-]*)', declaration.replace(',', ' '))
        if not flags or '--help' in flags:
            continue
        name = next((f for f in flags if f.startswith('--')), flags[0])
        placeholder = re.search(r'(<[^>]+>|\[[^\]]+\])', declaration)
        val = placeholder.group(0) if placeholder else ''
        choices = re.search(r'choices: ((?:"[^"]+"(?:, )?)+)', description)
        opts.append({'name': name, 'aliases': flags, 'description':description,
                     'value':bool(val), 'optionalValue':val.startswith('['), 'variadic':'...' in val,
                     'repeatable':bool(re.search(r'default: \[\]|repeatable|repeat for|can be repeated', description, re.I)),
                     'placeholder':val[1:-1].removesuffix('...'),
                     'choices':re.findall(r'"([^"]+)"', choices.group(1)) if choices else [],
                     'secret':bool(SECRET.search(name))})
    paragraph = value.split('\n\n')
    return {'id':'.'.join(path), 'path':path, 'group':path[0] if path else '',
            'groupName':GROUP_NAMES.get(path[0], path[0]) if path else '',
            'description':paragraph[1].strip() if len(paragraph) > 1 and not re.match(r'\w+:', paragraph[1]) else '',
            'arguments':args, 'options':opts, 'help':value,
            'revision':hashlib.sha256(value.encode()).hexdigest()}


class Catalog:
    def __init__(self, reference=REFERENCE, policy=POLICY):
        reference = json.loads(Path(reference).read_text())
        self.version = reference['version']
        self.policies = json.loads(Path(policy).read_text())
        self.operations = {}
        for node in reference['nodes']:
            if not node['path'] or help_entries(node['help'], 'Commands'):
                continue
            spec = parse_help(node['path'], node['help'])
            spec['effect'] = 'read' if spec['id'] in self.policies['read'] else 'write'
            spec['scope'] = 'waker' if spec['id'] in self.policies['scoped'] else 'admin'
            spec['panelPage'] = self.policies.get('pages', {}).get(spec['group'])
            spec['panelOnly'] = spec['id'] in self.policies.get('panelOnly', [])
            self.operations[spec['id']] = spec

    def get(self, op):
        if not isinstance(op, str) or op not in self.operations:
            raise AccessError(404, 'cli_operation_not_found')
        return self.operations[op]

    def visible(self, identity, spec):
        return identity['role'] == 'admin' or (bool(identity.get('user_id')) and
            spec['scope'] == 'waker' and (spec['effect'] == 'read' or identity['role'] == 'maintainer'))

    def authorize(self, identity, spec, values=None):
        if not self.visible(identity, spec):
            raise AccessError(403, 'permission_denied')
        if values is None or identity['role'] == 'admin':
            return
        allowed = self.policies['scoped'][spec['id']]
        opts = values.get('options', {})
        if set(opts) - set(allowed):
            raise AccessError(403, 'cli_scoped_option_rejected')
        if opts.get('--waker-id') not in identity.get('wakers', []):
            raise AccessError(403, 'waker_scope_rejected')
        if '--path' in opts:
            path = opts['--path']
            if not isinstance(path,str) or Path(path).is_absolute() or '..' in Path(path).parts:
                raise AccessError(403, 'cli_scoped_option_rejected')

    def listing(self, identity):
        return {'referenceVersion':self.version, 'items':[
            {k:v for k,v in spec.items() if k not in ('options', 'arguments', 'help')}
            for spec in self.operations.values() if self.visible(identity, spec)]}


def string(value):
    if not isinstance(value, str) or '\0' in value or len(value.encode()) > 60000:
        raise ValueError('invalid_cli_parameter')
    return value


def build_argv(spec, values):
    """固定可执行程序与命令路径；值作为独立 argv，禁止 shell 拼接。"""
    if not isinstance(values, dict) or set(values) - {'arguments', 'options'}:
        raise ValueError('invalid_cli_parameters')
    opts, args = values.get('options', {}), values.get('arguments', {})
    if not isinstance(opts, dict) or not isinstance(args, dict):
        raise ValueError('invalid_cli_parameters')
    if set(opts) - {o['name'] for o in spec['options']} or set(args) - {a['name'] for a in spec['arguments']}:
        raise ValueError('unknown_cli_parameter')
    result = list(spec['path'])
    for option in spec['options']:
        name = option['name']
        if name not in opts:
            continue
        val = opts[name]
        if not option['value']:
            if type(val) is not bool:
                raise ValueError('invalid_cli_boolean')
            if val:
                result.append(name)
        elif option['optionalValue'] and val is True:
            result.append(name)
        else:
            vals = val if isinstance(val, list) else [val]
            if not isinstance(vals, list) or not 1 <= len(vals) <= 200:
                raise ValueError('invalid_cli_parameter')
            for item in vals:
                item = string(item)
                if option['choices'] and item not in option['choices']:
                    raise ValueError('invalid_cli_choice')
            # Commander 的 variadic option 消费后续值；以 -- 开始的值只允许单值等号形式。
            result.append(name + '=' + vals[0])
            for item in vals[1:]:
                if option['variadic'] and item.startswith('-'):
                    raise ValueError('invalid_cli_variadic_value')
                result.append(item if option['variadic'] else name + '=' + item)
    positionals, skipped = [], False
    for arg in spec['arguments']:
        val = args.get(arg['name'])
        if val is None:
            if arg['required']:
                raise ValueError('cli_argument_required')
            skipped = True
            continue
        if skipped:
            raise ValueError('cli_argument_gap')
        vals = val if arg['variadic'] else [val]
        if not isinstance(vals, list) or not 1 <= len(vals) <= 200:
            raise ValueError('invalid_cli_parameter')
        positionals.extend(string(item) for item in vals)
    if positionals:
        result.extend(['--', *positionals])
    return result


def redact(value, secrets=()):
    """输出与持久化元数据分离；输出中常见凭据字段和本次秘密值被遮蔽。"""
    if isinstance(value, dict):
        return {k: '[已隐藏]' if SECRET.search(k) else redact(v, secrets) for k,v in value.items()}
    if isinstance(value, list):
        return [redact(v, secrets) for v in value]
    if not isinstance(value, str):
        return value
    for secret in secrets:
        if isinstance(secret, str) and secret:
            value = value.replace(secret, '[已隐藏]')
    value = re.sub(r'(?i)((?:password|passwd|secret|token|api[_-]?key|authorization|cookie)["\s]*[:=]\s*)[^\r\n,}]+',
                   r'\1[已隐藏]', value)
    value = re.sub(r'(?i)\bBearer\s+[A-Za-z0-9._~+/-]+=*', 'Bearer [已隐藏]', value)
    return re.sub(r'\bsk-[A-Za-z0-9_-]{16,}', '[已隐藏]', value)
