import { useState } from 'react';
import type { FormEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../lib/api';
import type { Identity } from '../lib/types';
import { ErrorNotice, Field } from '../components/ui';
export function Login() {
  const client = useQueryClient();
  const { data } = useQuery({
    queryKey: ['auth-mode'],
    queryFn: ({ signal }) => api<{ mode: string }>('auth/mode', { signal }),
  });
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const accounts = data?.mode === 'accounts';
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const values = new FormData(form);
    setBusy(true);
    setError(null);
    try {
      await api('login', {
        body: accounts
          ? { username: values.get('username'), password: values.get('password') }
          : { token: values.get('password') },
      });
      const identity = await api<Identity>('whoami');
      form.reset();
      await client.cancelQueries();
      client.removeQueries({ predicate: (q) => q.queryKey[0] !== 'identity' });
      client.setQueryData(['identity'], identity);
    } catch (error) {
      setError(error);
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="welcome">
      <div className="welcome-copy">
        <div className="login-brand">
          QoderWake <span>Panel</span>
        </div>
        <h1>欢迎回到工作区</h1>
        <p className="welcome-lead">一起维护技能与配置。</p>
        <form onSubmit={submit}>
          {accounts && (
            <Field label="用户名">
              <input name="username" autoComplete="username" required maxLength={64} />
            </Field>
          )}
          <Field label={accounts ? '密码' : '面板访问令牌'}>
            <input
              name="password"
              type="password"
              autoComplete={accounts ? 'current-password' : 'off'}
              required
              maxLength={1024}
            />
          </Field>
          <ErrorNotice error={error} />
          <button className="primary welcome-submit" disabled={busy || !data}>
            {busy ? '正在登录…' : '登录面板'}
          </button>
        </form>
        <p className="muted fine">
          {accounts ? '使用管理员分配的账号登录。' : '使用此实例的管理员或只读访问令牌。'}
        </p>
        {location.protocol === 'http:' && (
          <p className="transport-note">当前连接未加密，请使用可信网络、SSH 隧道或 HTTPS。</p>
        )}
      </div>
      <aside className="welcome-art">
        <p className="eyebrow">QODERWAKE / WORKSPACE</p>
        <h2>
          让每一次维护，
          <br />
          都有清晰的上下文。
        </h2>
        <p>技能、配置和运行记录，集中在同一处。</p>
        <dl className="welcome-features">
          <div>
            <dt>技能与配置</dt>
            <dd>围绕 Waker 编辑，查看版本与差异。</dd>
          </div>
          <div>
            <dt>共同维护</dt>
            <dd>{accounts ? '独立账号、角色和资源范围。' : '管理员与只读访问分开使用。'}</dd>
          </div>
          <div>
            <dt>运行与观测</dt>
            <dd>从操作结果追溯到维护记录。</dd>
          </div>
        </dl>
      </aside>
    </section>
  );
}
