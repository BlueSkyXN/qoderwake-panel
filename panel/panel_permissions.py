"""页面权限与操作权限分层；原生操作由命令注册表再次校验资源范围。"""
from panel_accounts import AccessError

READ_ROUTES = {
    '/api/state': None, '/api/audit': None,
    '/api/waker/detail': 'id', '/api/skills': 'waker',
    '/api/skill/content': 'waker', '/api/skill/versions': 'waker', '/api/skill/diff': 'waker',
    '/api/sessions/query': 'waker',
}
WRITE_ROUTES = {'/api/waker/update': 'id', '/api/skill/update': 'wakerId', '/api/skill/rollback': 'wakerId'}


def authorize(identity, method, path, values):
    role = identity['role']
    if role == 'admin':
        return
    if not identity.get('user_id'):
        raise AccessError(403, 'admin_required')
    if ((method == 'GET' and path in ('/api/native/catalog', '/api/native/operation', '/api/native/jobs', '/api/native/job')) or
            (method == 'POST' and path in ('/api/native/execute','/api/native/cancel'))):
        return
    if method == 'POST' and path == '/api/account/password':
        return
    routes = READ_ROUTES if method == 'GET' else WRITE_ROUTES if role == 'maintainer' else {}
    if path not in routes:
        raise AccessError(403, 'permission_denied')
    field = routes[path]
    if field and (not isinstance(values.get(field), str) or values[field] not in identity.get('wakers', [])):
        raise AccessError(403, 'waker_scope_rejected')


def capabilities(identity):
    if identity['role'] == 'admin':
        return ['admin', 'waker.read', 'waker.write', 'skill.read', 'skill.write', 'audit.read']
    if identity.get('user_id'):
        return ['waker.read', 'skill.read', 'audit.read'] + (
            ['waker.write', 'skill.write'] if identity['role'] == 'maintainer' else [])
    return []
