import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../lib/api';
import { useIdentity } from '../context/session';
import { useDirtyGuard } from '../context/drafts';
import { usePanelState } from '../components/resources';
import { Card, Empty, ErrorNotice, Field, Loading, Table, useConfirm } from '../components/ui';
interface Caller {
  id: string;
  name: string;
  wakers: string[];
  quota: number;
  used: number;
  expires: number;
  revoked: boolean;
}
export function Callers() {
  const identity = useIdentity();
  const state = usePanelState();
  const confirm = useConfirm();
  const client = useQueryClient();
  const [name, setName] = useState('');
  const [quota, setQuota] = useState(100);
  const [days, setDays] = useState(7);
  const [scopes, setScopes] = useState<string[]>([]);
  const [token, setToken] = useState('');
  const [show, setShow] = useState(false);
  useDirtyGuard(!!name || scopes.length > 0 || !!token);
  const callers = useQuery({
    queryKey: ['callers'],
    queryFn: ({ signal }) => api<Caller[]>('callers', { signal }),
    enabled: identity.level === 'admin',
  });
  const create = useMutation({
    mutationFn: async () => {
      if (!(await confirm('创建仅能调用所选机器人的 API 密钥？'))) return;
      const value = await api<{ token: string }>('callers/create', {
        body: { name, quota, days, wakers: scopes, confirm: '/api/callers/create' },
      });
      setToken(value.token);
      setShow(false);
      setName('');
      setScopes([]);
      await client.invalidateQueries({ queryKey: ['callers'] });
    },
  });
  const revoke = useMutation({
    mutationFn: async (row: Caller) => {
      if (!(await confirm('撤销 ' + row.name + ' 的调用密钥？现有程序需更换密钥后才能继续调用。')))
        return;
      await api('callers/revoke', { body: { id: row.id, confirm: '/api/callers/revoke' } });
      await client.invalidateQueries({ queryKey: ['callers'] });
    },
  });
  if (identity.level !== 'admin') return <Empty>此功能仅管理员可用。</Empty>;
  if (state.isPending) return <Loading />;
  if (state.error) return <ErrorNotice error={state.error} />;
  return (
    <>
      <Card title="调用密钥">
        {callers.isPending ? (
          <Loading />
        ) : callers.error ? (
          <ErrorNotice error={callers.error} />
        ) : callers.data?.length ? (
          <Table head={['名称', '机器人范围', '已用 / 上限', '有效期', '状态', '操作']}>
            {callers.data.map((row) => (
              <tr key={row.id}>
                <td>{row.name}</td>
                <td>
                  {row.wakers
                    .map((id) => state.data?.wakers.find((w) => w.id === id)?.name || id)
                    .join('、')}
                </td>
                <td>
                  {row.used} / {row.quota}
                </td>
                <td>{new Date(row.expires * 1000).toLocaleString()}</td>
                <td>
                  {row.revoked ? '已撤销' : row.expires * 1000 < Date.now() ? '已到期' : '有效'}
                </td>
                <td>
                  <button
                    disabled={row.revoked || revoke.isPending}
                    onClick={() => revoke.mutate(row)}
                  >
                    撤销
                  </button>
                </td>
              </tr>
            ))}
          </Table>
        ) : (
          <Empty>尚未创建调用密钥。</Empty>
        )}
        <ErrorNotice error={revoke.error} />
      </Card>
      {token && (
        <Card title="保存调用密钥">
          <p>密钥仅在创建时显示。保存后关闭此卡片。</p>
          <Field label="调用密钥">
            <input
              type={show ? 'text' : 'password'}
              readOnly
              value={token}
              autoComplete="off"
              onFocus={(e) => e.target.select()}
            />
          </Field>
          <div className="row">
            <button onClick={() => setShow(!show)}>{show ? '隐藏' : '显示'}</button>
            <button onClick={() => setToken('')}>已保存，关闭</button>
          </div>
        </Card>
      )}
      <Card title="创建调用密钥">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <fieldset disabled={create.isPending || !!token}>
            <div className="form-grid">
              <Field label="名称">
                <input
                  value={name}
                  required
                  maxLength={80}
                  onChange={(e) => setName(e.target.value)}
                />
              </Field>
              <Field label="请求次数上限">
                <input
                  type="number"
                  min={1}
                  max={100000}
                  value={quota}
                  onChange={(e) => setQuota(Number(e.target.value))}
                />
              </Field>
              <Field label="有效天数">
                <input
                  type="number"
                  min={1}
                  max={365}
                  value={days}
                  onChange={(e) => setDays(Number(e.target.value))}
                />
              </Field>
            </div>
            <h3>可调用的机器人</h3>
            {state.data?.wakers.map((w) => (
              <label className="check-row" key={w.id}>
                <input
                  type="checkbox"
                  checked={scopes.includes(w.id)}
                  onChange={(e) =>
                    setScopes((old) =>
                      e.target.checked ? [...old, w.id] : old.filter((id) => id !== w.id),
                    )
                  }
                />
                <span>{w.name}</span>
              </label>
            ))}
            <div className="form-actions">
              <button className="primary" disabled={!scopes.length}>
                创建密钥
              </button>
            </div>
          </fieldset>
        </form>
        <ErrorNotice error={create.error} />
      </Card>
    </>
  );
}
