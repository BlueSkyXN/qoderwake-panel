import { useState } from 'react';
import type { FormEvent } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, setCSRF } from '../lib/api';
import type { PanelState, User } from '../lib/types';
import { roles } from '../lib/messages';
import { useIdentity } from '../context/session';
import { Card, Empty, ErrorNotice, Field, Loading, Table, useToast } from '../components/ui';
export function Users() {
  const identity = useIdentity();
  const client = useQueryClient();
  const [editing, setEditing] = useState<User | null | undefined>(undefined);
  const [reset, setReset] = useState<User | null>(null);
  const list = useQuery({
    queryKey: ['users'],
    queryFn: ({ signal }) => api<{ items: User[]; mode: string }>('users', { signal }),
  });
  const state = useQuery({
    queryKey: ['state'],
    queryFn: ({ signal }) => api<PanelState>('state', { signal }),
  });
  const changed = async (id?: string) => {
    setEditing(undefined);
    setReset(null);
    await client.invalidateQueries({ queryKey: ['users'] });
    if (id === identity.userId) {
      setCSRF('');
      client.setQueryData(['identity'], null);
    }
  };
  if (list.isPending) return <Loading />;
  if (list.error) return <ErrorNotice error={list.error} retry={() => void list.refetch()} />;
  if (list.data?.mode !== 'accounts')
    return (
      <Card title="维护者账号">
        <p>请先在宿主机初始化管理员账号。</p>
      </Card>
    );
  return (
    <>
      <Card
        title="维护者"
        actions={
          <button
            className="primary"
            onClick={() => {
              setEditing(null);
              setReset(null);
            }}
          >
            新增维护者
          </button>
        }
      >
        <Table head={['账号', '角色', '范围', '状态', '操作']}>
          {list.data?.items.map((u) => (
            <tr key={u.id}>
              <td>
                <strong>{u.displayName}</strong>
                <br />
                <span className="muted">{u.username}</span>
              </td>
              <td>{roles[u.role]}</td>
              <td>
                {u.role === 'admin'
                  ? '全部'
                  : u.wakers
                      .map((w) => state.data?.wakers.find((item) => item.id === w)?.name || w)
                      .join('、') || '未分配'}
              </td>
              <td>{u.enabled ? '启用' : '停用'}</td>
              <td className="actions">
                <button
                  onClick={() => {
                    setEditing(u);
                    setReset(null);
                  }}
                >
                  编辑
                </button>
                <button
                  onClick={() => {
                    setReset(u);
                    setEditing(undefined);
                  }}
                >
                  重置密码
                </button>
              </td>
            </tr>
          ))}
        </Table>
        {!list.data?.items.length && <Empty />}
      </Card>
      {editing !== undefined && (
        <UserEditor
          key={editing?.id || 'new'}
          user={editing}
          wakers={state.data?.wakers || []}
          onSaved={changed}
          onCancel={() => setEditing(undefined)}
        />
      )}{' '}
      {reset && <PasswordReset key={reset.id} user={reset} onSaved={changed} />}
    </>
  );
}
function UserEditor({
  user,
  wakers,
  onSaved,
  onCancel,
}: {
  user: User | null;
  wakers: PanelState['wakers'];
  onSaved: (id?: string) => Promise<void>;
  onCancel: () => void;
}) {
  const [role, setRole] = useState(user?.role || 'maintainer');
  const mutation = useMutation({
    mutationFn: (body: unknown) =>
      api<{ user: User }>(user ? 'users/update' : 'users/create', { body }),
    onSuccess: () => onSaved(user?.id),
  });
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    mutation.mutate({
      displayName: form.get('displayName'),
      role,
      wakers: role === 'admin' ? [] : form.getAll('wakers'),
      ...(user
        ? { id: user.id, revision: user.revision, enabled: form.has('enabled') }
        : { username: form.get('username'), password: form.get('password') }),
    });
  }
  return (
    <Card title={user ? '编辑账号' : '新增账号'}>
      <form onSubmit={submit}>
        <fieldset disabled={mutation.isPending}>
          <div className="form-grid">
            <Field label="用户名">
              <input
                name="username"
                defaultValue={user?.username}
                disabled={!!user}
                required
                maxLength={64}
                pattern="[A-Za-z0-9][A-Za-z0-9_.-]*"
              />
            </Field>
            <Field label="显示名称">
              <input name="displayName" defaultValue={user?.displayName} required maxLength={80} />
            </Field>
            <Field label="角色">
              <select
                name="role"
                value={role}
                onChange={(e) => setRole(e.target.value as User['role'])}
              >
                {['admin', 'maintainer', 'observer'].map((r) => (
                  <option key={r} value={r}>
                    {roles[r]}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="管理范围（可多选）">
              <select
                name="wakers"
                multiple
                defaultValue={user?.wakers || []}
                disabled={role === 'admin'}
              >
                {wakers.map((w) => (
                  <option key={w.id} value={w.id}>
                    {w.name}
                  </option>
                ))}
              </select>
            </Field>
            {!user && (
              <Field label="初始密码">
                <input
                  type="password"
                  name="password"
                  minLength={10}
                  maxLength={1024}
                  autoComplete="new-password"
                  required
                />
              </Field>
            )}
          </div>
          {user && (
            <label className="check-row">
              <input name="enabled" type="checkbox" defaultChecked={user.enabled} />
              启用账号
            </label>
          )}
          <ErrorNotice error={mutation.error} />
          <div className="form-actions">
            <button type="button" onClick={onCancel}>
              取消
            </button>
            <button className="primary">保存</button>
          </div>
        </fieldset>
      </form>
    </Card>
  );
}
function PasswordReset({ user, onSaved }: { user: User; onSaved: (id?: string) => Promise<void> }) {
  const mutation = useMutation({
    mutationFn: (password: string) =>
      api('users/password', { body: { id: user.id, revision: user.revision, password } }),
    onSuccess: () => onSaved(user.id),
  });
  return (
    <Card title={'重置 ' + user.displayName + ' 的密码'}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          mutation.mutate(String(new FormData(e.currentTarget).get('password')));
        }}
      >
        <Field label="新密码">
          <input
            name="password"
            type="password"
            minLength={10}
            maxLength={1024}
            autoComplete="new-password"
            required
            disabled={mutation.isPending}
          />
        </Field>
        <p>保存后，该账号已有登录会话将失效。</p>
        <ErrorNotice error={mutation.error} />
        <button className="primary" disabled={mutation.isPending}>
          重置密码
        </button>
      </form>
    </Card>
  );
}
export function Account() {
  const identity = useIdentity();
  const client = useQueryClient();
  const toast = useToast();
  const mutation = useMutation({
    mutationFn: (body: unknown) => api('account/password', { body }),
    onSuccess: () => {
      setCSRF('');
      client.setQueryData(['identity'], null);
      toast('密码已更新，请重新登录');
    },
  });
  return (
    <Card title="我的账号">
      <p>
        {identity.displayName} · {identity.username} · {roles[identity.level]}
      </p>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          const data = new FormData(e.currentTarget);
          mutation.mutate({ currentPassword: data.get('current'), password: data.get('next') });
        }}
      >
        <Field label="当前密码">
          <input name="current" type="password" autoComplete="current-password" required />
        </Field>
        <Field label="新密码">
          <input
            name="next"
            type="password"
            minLength={10}
            maxLength={1024}
            autoComplete="new-password"
            required
          />
        </Field>
        <ErrorNotice error={mutation.error} />
        <button className="primary" disabled={mutation.isPending}>
          修改密码并重新登录
        </button>
      </form>
    </Card>
  );
}
