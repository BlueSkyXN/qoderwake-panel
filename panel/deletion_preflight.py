"""Fail-closed dependency scans and canonical impact digests for deletion."""
import hashlib
import json
import re
import time

STATUSES = {'clear', 'blocked', 'warning', 'unknown'}
COMPLETENESS = {'complete', 'partial', 'unavailable'}
ACTIVE_STATES = {
    'open', 'running', 'queued', 'pending', 'starting', 'needs_restart',
    'processing', 'in_progress'
}
TERMINAL_STATES = {
    'success', 'succeeded', 'completed', 'failed', 'canceled', 'cancelled',
    'terminated', 'closed', 'idle'
}


class SourceUnavailable(Exception):
    pass


def _text(value):
    return value.strip() if isinstance(value, str) else ''


def _model(value):
    if isinstance(value, str):
        return value.strip()
    if value is None:
        return ''
    if isinstance(value, dict):
        values = []
        for key in ('modelId', 'model_id', 'model', 'id'):
            if key in value:
                if not isinstance(value[key], str) or not value[key].strip():
                    raise SourceUnavailable('model_reference_invalid')
                values.append(value[key].strip())
        if values and len(set(values)) == 1:
            return values[0]
    raise SourceUnavailable('model_reference_invalid')


def _refs(rows, keys=('id', 'name', 'title'), limit=40):
    result = []
    for row in rows:
        if isinstance(row, str):
            value = row
        elif isinstance(row, dict):
            value = next((_text(row.get(key)) for key in keys if _text(row.get(key))), '')
        else:
            value = ''
        if value and value not in result:
            result.append(value[:160])
        if len(result) >= limit:
            break
    return sorted(result)


def _check(identifier, label, status='clear', completeness='complete', required=True,
           count=0, refs=None, note=''):
    if status not in STATUSES or completeness not in COMPLETENESS:
        raise ValueError('invalid_deletion_check')
    return {
        'id': identifier,
        'label': label,
        'status': status,
        'completeness': completeness,
        'required': bool(required),
        'count': int(count),
        'refs': sorted(set(refs or []))[:40],
        'note': note[:400]
    }


def _unknown(identifier, label, required=True, note='无法完整读取该依赖源'):
    return _check(identifier, label, 'unknown', 'unavailable', required, note=note)


TARGET_ID_KEYS = {
    'wakerId', 'agentId', 'workerId', 'targetId', 'target_key',
    'resource_locator', 'wakerKey', 'bindingTargetId'
}
TARGET_CONTAINER_KEYS = {
    'target', 'targets', 'executionTarget', 'execution_target', 'bindingTarget',
    'conversations', 'conversation', 'members', 'leader'
}
MODEL_KEYS = {'model', 'currentModel', 'current_model', 'modelId', 'model_id'}


def _target_ids(value):
    result = set()
    if isinstance(value, list):
        for item in value:
            result.update(_target_ids(item))
    elif isinstance(value, dict):
        for key, item in value.items():
            if key in TARGET_ID_KEYS:
                if not isinstance(item, str) or not item.strip():
                    raise SourceUnavailable('target_reference_invalid')
                result.add(item.strip().removeprefix('local:'))
            elif key in TARGET_CONTAINER_KEYS:
                if item is None:
                    continue
                result.update(_target_ids(item))
    elif isinstance(value, str) and value.strip():
        result.add(value.strip().removeprefix('local:'))
    else:
        raise SourceUnavailable('target_reference_invalid')
    return result


def _single_target_id(value):
    try:
        values = _target_ids(value)
    except SourceUnavailable:
        return ''
    return next(iter(values)) if len(values) == 1 else ''


def _models(value):
    result = set()
    if isinstance(value, list):
        for item in value:
            result.update(_models(item))
    elif isinstance(value, dict):
        for key, item in value.items():
            if key in MODEL_KEYS:
                candidate = _model(item)
                if item not in (None, '') and not candidate:
                    raise SourceUnavailable('model_reference_invalid')
                if candidate:
                    result.add(candidate)
            elif key in TARGET_CONTAINER_KEYS or key in ('config', 'channel'):
                result.update(_models(item))
    elif value is not None and not isinstance(value, str):
        raise SourceUnavailable('model_reference_invalid')
    return result


def _channel_payload(row):
    if not isinstance(row, dict):
        raise SourceUnavailable('channel_schema_invalid')
    channel = row['channel'] if 'channel' in row else row
    if not isinstance(channel, dict):
        raise SourceUnavailable('channel_schema_invalid')
    config = channel['config'] if 'config' in channel else channel
    if not isinstance(config, dict):
        raise SourceUnavailable('channel_schema_invalid')
    return config


def _channel_targets(row):
    config = _channel_payload(row)
    result = set()
    for key in ('agentId', 'wakerId', 'bindingTargetId'):
        if key in config:
            value = _text(config.get(key)).removeprefix('local:')
            if not value:
                raise SourceUnavailable('channel_target_invalid')
            result.add(value)
    if 'bindingTarget' in config and config.get('bindingTarget') is not None:
        binding = config.get('bindingTarget')
        if isinstance(binding, str):
            value = binding.strip().removeprefix('local:')
            if not value:
                raise SourceUnavailable('channel_target_invalid')
            result.add(value)
        elif isinstance(binding, dict):
            values = _target_ids(binding)
            if len(values) != 1:
                raise SourceUnavailable('channel_target_invalid')
            result.update(values)
        else:
            raise SourceUnavailable('channel_target_invalid')
    return result


def _session_state(row):
    if not isinstance(row, dict):
        raise SourceUnavailable('session_schema_invalid')
    raw = row.get('session_status') if 'session_status' in row else row.get('status')
    state = _text(raw).lower()
    if state in ACTIVE_STATES:
        return 'active'
    if state in TERMINAL_STATES:
        return 'terminal'
    raise SourceUnavailable('session_state_unknown')


def _workflow_model_match(row, models):
    if not isinstance(row, dict):
        raise SourceUnavailable('workflow_schema_invalid')
    structured = _models(row)
    if models.intersection(structured):
        return True
    strings = [row.get(key) for key in ('description', 'script')]
    for value in strings:
        if not isinstance(value, str):
            continue
        for model in models:
            pattern = r'(?<![A-Za-z0-9_.:/-])' + re.escape(model) + r'(?![A-Za-z0-9_.:/-])'
            if re.search(pattern, value):
                return True
    return False


def _group_parts(row):
    if not isinstance(row, dict):
        return None
    group = row.get('group', row)
    if not isinstance(group, dict):
        return None
    members = row.get('members', group.get('members'))
    leader = row.get('leader', group.get('leader'))
    if not isinstance(members, list) or leader is None:
        return None
    return group, members, leader


def _member_id(row):
    if isinstance(row, str):
        return _text(row).removeprefix('local:')
    if not isinstance(row, dict):
        return ''
    target = row.get('target', {})
    if not isinstance(target, dict):
        return ''
    values = []
    for container, keys in ((row, ('wakerKey', 'wakerId', 'agentId')),
                            (target, ('agentId', 'wakerId', 'id'))):
        for key in keys:
            if key in container:
                value = _text(container[key]).removeprefix('local:')
                if not value:
                    return ''
                values.append(value)
    if not values:
        values.append(_text(row.get('id')).removeprefix('local:'))
    return values[0] if values[0] and len(set(values)) == 1 else ''


def _group_member_models(member):
    if not _member_id(member):
        raise SourceUnavailable('group_member_invalid')
    def validate(value):
        if isinstance(value, dict):
            for key, item in value.items():
                if 'model' in key.lower() and key not in MODEL_KEYS:
                    raise SourceUnavailable('group_model_schema_unknown')
                if key in TARGET_CONTAINER_KEYS or key in ('config', 'channel'):
                    validate(item)
        elif isinstance(value, list):
            for item in value:
                validate(item)
    validate(member)
    return _models(member)


def _preference_models(value):
    if not isinstance(value, dict) or not {'noProject', 'byProject'}.intersection(value):
        raise SourceUnavailable('model_preference_invalid')
    result = []
    if 'noProject' in value:
        model = _model(value.get('noProject'))
        if model:
            result.append(model)
    by_project = value.get('byProject', {})
    if not isinstance(by_project, dict):
        raise SourceUnavailable('model_preference_invalid')
    for item in by_project.values():
        model = _model(item)
        if model:
            result.append(model)
    return result


def _waker_fingerprint(detail):
    if not isinstance(detail, dict):
        raise SourceUnavailable('waker_detail_invalid')
    public = {key: detail.get(key) for key in (
        'agentId', 'id', 'name', 'description', 'sessionTimeout',
        'revision', 'version', 'updatedAt', 'updated_at', 'skills'
    )}
    try:
        raw = json.dumps(public, ensure_ascii=False, sort_keys=True,
                         separators=(',', ':')).encode()
    except (TypeError, ValueError):
        raise SourceUnavailable('waker_detail_invalid')
    return hashlib.sha256(raw).hexdigest()


def canonical_impact(kind, target, revision, checks):
    normalized = {
        'kind': kind,
        'target': target,
        'revision': revision or '',
        'checks': [{key: check[key] for key in (
            'id', 'status', 'completeness', 'required', 'count', 'refs'
        )} for check in sorted(checks, key=lambda item: item['id'])]
    }
    raw = json.dumps(normalized, ensure_ascii=False, sort_keys=True,
                     separators=(',', ':')).encode()
    return hashlib.sha256(raw).hexdigest()


class DeletionPreflight:
    """The source object supplies bounded, fully validated lists or raises."""

    def __init__(self, source):
        self.source = source

    def scan(self, kind, target, revision=''):
        if kind == 'waker':
            checks, waker_revision = self._waker(target)
            revision = revision or waker_revision
        elif kind == 'provider':
            checks = self._provider(target)
        else:
            raise ValueError('invalid_deletion_kind')
        digest = canonical_impact(kind, target, revision, checks)
        blocked = any(check['required'] and (
            check['status'] in ('blocked', 'unknown') or
            check['completeness'] != 'complete'
        ) for check in checks)
        warnings = [check['id'] for check in checks if check['status'] == 'warning']
        completeness = ('unavailable' if any(check['required'] and check['completeness'] == 'unavailable' for check in checks)
                        else 'partial' if any(check['completeness'] != 'complete' for check in checks)
                        else 'complete')
        return {
            'kind': kind,
            'target': target,
            'revision': revision or '',
            'impactDigest': digest,
            'checks': checks,
            'warningsRequired': warnings,
            'deletable': not blocked,
            'completeness': completeness
        }

    def _call(self, identifier, label, callback, required=True):
        try:
            return callback(), None
        except Exception:
            return None, _unknown(identifier, label, required)

    def _shared(self):
        values = {}
        for key, label, callback, required in (
            ('wakers', 'Waker 清单', self.source.wakers, True),
            ('triggers', '自动化 Trigger', self.source.triggers, True),
            ('channels', 'IM Channel', self.source.channels, True),
            ('pairings', '待处理 IM 配对', self.source.pairings, True),
            ('groups', '协作 Group', self.source.groups, True),
        ):
            values[key] = self._call(key, label, callback, required)
        return values

    def _waker(self, target):
        checks = []
        detail, error = self._call('system_identity', '系统 Waker 保护',
                                   lambda: self.source.waker_detail(target))
        if error:
            checks.append(error)
        elif (not isinstance(detail, dict) or
              (detail.get('agentId') or detail.get('id')) != target):
            checks.append(_unknown('system_identity', '系统 Waker 保护'))
        else:
            protected = detail.get('systemIdentity') is not None
            checks.append(_check('system_identity', '系统 Waker 保护',
                                 'blocked' if protected else 'clear', count=int(protected),
                                 refs=['systemIdentity'] if protected else []))

        callers, error = self._call('caller_scope', 'Panel 调用者授权', self.source.callers)
        if error:
            checks.append(error)
        elif not isinstance(callers, list):
            checks.append(_unknown('caller_scope', 'Panel 调用者授权'))
        else:
            matched = [row for row in callers if isinstance(row, dict) and
                       target in (row.get('wakers') if isinstance(row.get('wakers'), list) else []) and
                       not row.get('revoked') and
                       isinstance(row.get('expires'), (int, float)) and row.get('expires') > time.time()]
            checks.append(_check('caller_scope', 'Panel 调用者授权',
                                 'blocked' if matched else 'clear', count=len(matched),
                                 refs=_refs(matched, ('id', 'name'))))

        shared = self._shared()
        triggers, error = shared['triggers']
        if error:
            checks.append(_unknown('trigger_target', 'Trigger 目标'))
        else:
            try:
                if not isinstance(triggers, list):
                    raise SourceUnavailable('trigger_list_invalid')
                matched = []
                for row in triggers:
                    targets = _target_ids(row)
                    if not targets:
                        raise SourceUnavailable('trigger_target_unknown')
                    if target in targets:
                        matched.append(row)
            except SourceUnavailable:
                checks.append(_unknown('trigger_target', 'Trigger 目标'))
            else:
                checks.append(_check('trigger_target', 'Trigger 目标',
                                     'blocked' if matched else 'clear', count=len(matched),
                                     refs=_refs(matched, ('triggerId', 'id', 'triggerName'))))

        channels, error = shared['channels']
        if error:
            checks.append(_unknown('channel_target', 'Channel 绑定目标'))
        else:
            matched = []
            invalid = False
            for row in channels:
                try:
                    if target in _channel_targets(row):
                        matched.append(row)
                except SourceUnavailable:
                    invalid = True
            if invalid:
                checks.append(_unknown('channel_target', 'Channel 绑定目标'))
            else:
                checks.append(_check('channel_target', 'Channel 绑定目标',
                                     'blocked' if matched else 'clear', count=len(matched),
                                     refs=_refs(matched, ('channelId', 'id', 'name'))))

        pairings, error = shared['pairings']
        if error:
            checks.append(_unknown('pairing_target', '待处理 IM 配对', False))
        else:
            try:
                if not isinstance(pairings, list):
                    raise SourceUnavailable('pairing_list_invalid')
                matched = [row for row in pairings if target in _target_ids(row)]
            except SourceUnavailable:
                checks.append(_unknown('pairing_target', '待处理 IM 配对', False))
            else:
                checks.append(_check('pairing_target', '待处理 IM 配对',
                                     'warning' if matched else 'clear', 'complete', False,
                                     len(matched), _refs(matched, ('pendingId', 'id', 'conversationName')),
                                     '待审批配对尚未形成活跃绑定，但删除后可能无法按原目标批准'))

        groups, error = shared['groups']
        if error:
            checks.append(_unknown('group_membership', 'Group 成员与负责人'))
        else:
            malformed = False
            matched = []
            for row in groups:
                parts = _group_parts(row)
                if parts is None:
                    malformed = True
                    continue
                group, members, leader = parts
                if not _member_id(leader) or any(not _member_id(item) for item in members):
                    malformed = True
                    continue
                if target == _member_id(leader) or any(_member_id(item) == target for item in members):
                    matched.append(group)
            if malformed:
                checks.append(_unknown('group_membership', 'Group 成员与负责人'))
            else:
                checks.append(_check('group_membership', 'Group 成员与负责人',
                                     'blocked' if matched else 'clear', count=len(matched),
                                     refs=_refs(matched, ('id', 'groupId', 'name', 'title'))))

        workflows, error = self._call('scoped_workflow', 'Waker 作用域 Workflow',
                                      lambda: self.source.workflows(target))
        if error or not isinstance(workflows, list):
            checks.append(_unknown('scoped_workflow', 'Waker 作用域 Workflow'))
        else:
            checks.append(_check('scoped_workflow', 'Waker 作用域 Workflow',
                                 'blocked' if workflows else 'clear', count=len(workflows),
                                 refs=_refs(workflows, ('workflowId', 'id', 'title', 'name'))))

        sessions, error = self._call('active_session', '运行中 Session',
                                     lambda: self.source.sessions(target))
        if error or not isinstance(sessions, list):
            checks.extend([_unknown('active_session', '运行中 Session'),
                           _unknown('session_history', '历史 Session', False)])
        else:
            active, history = [], []
            invalid = False
            for row in sessions:
                try:
                    state = _session_state(row)
                except SourceUnavailable:
                    invalid = True
                    continue
                (active if state == 'active' else history).append(row)
            if invalid:
                checks.append(_unknown('active_session', '运行中 Session'))
            else:
                checks.append(_check('active_session', '运行中 Session',
                                     'blocked' if active else 'clear', count=len(active),
                                     refs=_refs(active, ('session_id', 'sessionId', 'id', 'title'))))
            checks.append(_check('session_history', '历史 Session',
                                 'warning' if history else 'clear', 'complete', False,
                                 len(history), _refs(history, ('session_id', 'sessionId', 'id', 'title')),
                                 '删除 Waker 可能使历史会话、产物和安装状态失去关联'))

        activity, error = self._call('task_attribution', '运行/排队任务归因', self.source.activity)
        if error or not isinstance(activity, dict):
            checks.append(_unknown('task_attribution', '运行/排队任务归因'))
        else:
            keys = ('runningConversationTasks', 'runningTriggerTasks', 'queuedTriggerTasks')
            values = [activity.get(key) for key in keys]
            if any(type(value) is not int or value < 0 for value in values):
                checks.append(_unknown('task_attribution', '运行/排队任务归因'))
            elif sum(values):
                checks.append(_unknown('task_attribution', '运行/排队任务归因', True,
                                       'daemon 报告存在任务，但当前接口不能完整归因到单个 Waker'))
            else:
                checks.append(_check('task_attribution', '运行/排队任务归因'))

        skill_count = len(detail.get('skills') or []) if isinstance(detail, dict) and isinstance(detail.get('skills'), list) else 0
        checks.append(_check('installed_content_boundary', '插件、Skill 与外部脚本边界',
                             'warning', 'partial', False, skill_count,
                             note='已安装内容和面板外脚本无法统一枚举；删除前需人工确认'))
        try:
            fingerprint = _waker_fingerprint(detail)
        except SourceUnavailable:
            checks.append(_unknown('waker_revision', 'Waker 资源版本'))
            fingerprint = ''
        return checks, fingerprint

    def _provider(self, target):
        checks = []
        models, error = self._call('provider_models', 'Provider 模型集合',
                                   lambda: self.source.provider_models(target))
        if error or not isinstance(models, list) or not models or any(not _text(item) for item in models):
            return [_unknown('provider_models', 'Provider 模型集合')]
        models = set(models)
        shared = self._shared()
        wakers, error = shared['wakers']
        if error or not isinstance(wakers, list):
            checks.append(_unknown('model_preference', 'Waker 模型偏好'))
        else:
            matched = []
            failed = False
            for row in wakers:
                wid = _single_target_id(row)
                if not wid:
                    failed = True
                    continue
                try:
                    preferences = _preference_models(self.source.preference(wid))
                except Exception:
                    failed = True
                    continue
                if models.intersection(preferences):
                    matched.append(row)
            if failed:
                checks.append(_unknown('model_preference', 'Waker 模型偏好'))
            else:
                checks.append(_check('model_preference', 'Waker 模型偏好',
                                     'blocked' if matched else 'clear', count=len(matched),
                                     refs=_refs(matched, ('agentId', 'id', 'name'))))

        for source_key, check_id, label, getter, ref_keys in (
            ('triggers', 'trigger_model', 'Trigger 模型', _models,
             ('triggerId', 'id', 'triggerName')),
            ('channels', 'channel_model', 'Channel 模型',
             lambda row: _models(_channel_payload(row)), ('channelId', 'id', 'name')),
            ('pairings', 'pairing_model', '配对目标模型', _models,
             ('pendingId', 'id', 'conversationName')),
        ):
            rows, error = shared[source_key]
            if error or not isinstance(rows, list):
                checks.append(_unknown(check_id, label))
            else:
                matched = []
                invalid = False
                for row in rows:
                    try:
                        if models.intersection(getter(row)):
                            matched.append(row)
                    except SourceUnavailable:
                        invalid = True
                if invalid:
                    checks.append(_unknown(check_id, label))
                else:
                    checks.append(_check(check_id, label, 'blocked' if matched else 'clear',
                                         count=len(matched), refs=_refs(matched, ref_keys)))

        groups, error = shared['groups']
        if error or not isinstance(groups, list):
            checks.append(_unknown('group_model', 'Group 成员模型'))
        else:
            malformed = False
            matched = []
            for row in groups:
                parts = _group_parts(row)
                if parts is None:
                    malformed = True
                    continue
                group, members, leader = parts
                rows = list(members) + [leader]
                if any(not _member_id(member) for member in rows):
                    malformed = True
                    continue
                try:
                    references = [_group_member_models(member) for member in rows]
                    if any(models.intersection(value) for value in references):
                        matched.append(group)
                except SourceUnavailable:
                    malformed = True
            if malformed:
                checks.append(_unknown('group_model', 'Group 成员模型'))
            else:
                checks.append(_check('group_model', 'Group 成员模型',
                                     'blocked' if matched else 'clear', count=len(matched),
                                     refs=_refs(matched, ('id', 'groupId', 'name', 'title'))))

        sessions_failed = False
        active, history = [], []
        if isinstance(wakers, list):
            for waker in wakers:
                wid = _single_target_id(waker)
                if not wid:
                    sessions_failed = True
                    continue
                try:
                    rows = self.source.sessions(wid)
                    if not isinstance(rows, list):
                        raise SourceUnavailable('sessions_invalid')
                except Exception:
                    sessions_failed = True
                    continue
                for row in rows:
                    try:
                        state = _session_state(row)
                    except SourceUnavailable:
                        sessions_failed = True
                        continue
                    try:
                        model = _model(row.get('currentModel') if 'currentModel' in row else row.get('current_model'))
                    except SourceUnavailable:
                        if state == 'active':
                            sessions_failed = True
                        continue
                    if state == 'active':
                        if not model:
                            sessions_failed = True
                        elif model in models:
                            active.append(row)
                    elif model in models:
                        history.append(row)
        else:
            sessions_failed = True
        if sessions_failed:
            checks.append(_unknown('active_session_model', '运行中 Session 当前模型'))
        else:
            checks.append(_check('active_session_model', '运行中 Session 当前模型',
                                 'blocked' if active else 'clear', count=len(active),
                                 refs=_refs(active, ('session_id', 'sessionId', 'id', 'title'))))
        checks.append(_check('historical_session_model', '历史 Session 模型',
                             'warning' if history else 'clear', 'partial', False,
                             len(history), _refs(history, ('session_id', 'sessionId', 'id', 'title')),
                             '历史记录可能保留模型标识；缺失字段不能证明没有引用'))

        workflows = []
        workflow_failed = False
        scopes = [None] + [_single_target_id(row) for row in wakers] if isinstance(wakers, list) else [None]
        for scope in scopes:
            if scope == '':
                workflow_failed = True
                continue
            try:
                rows = self.source.workflows(scope)
                if not isinstance(rows, list):
                    raise SourceUnavailable('workflows_invalid')
                workflows.extend(rows)
            except Exception:
                workflow_failed = True
        if workflow_failed:
            checks.append(_unknown('workflow_text_reference', 'Workflow 文本引用', False))
        else:
            matched = []
            malformed = False
            for row in workflows:
                try:
                    if _workflow_model_match(row, models):
                        matched.append(row)
                except SourceUnavailable:
                    malformed = True
            if malformed:
                checks.append(_unknown('workflow_text_reference', 'Workflow 文本引用', False))
            else:
                checks.append(_check('workflow_text_reference', 'Workflow 文本引用',
                                     'warning' if matched else 'clear', 'partial', False,
                                     len(matched), _refs(matched, ('workflowId', 'id', 'title', 'name')),
                                     '仅检查结构化字段和带边界的脚本文本标识；动态引用无法证明不存在'))
        checks.append(_check('external_reference_boundary', '外部脚本与动态引用边界',
                             'warning', 'partial', False,
                             note='Panel 无法枚举面板外脚本、环境变量或运行时拼接的模型标识'))
        return checks
