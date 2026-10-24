import { Suspense, useEffect, useRef, useState } from 'react';
import { Link, NavLink, Outlet, useLocation } from 'react-router-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, setCSRF } from './lib/api';
import type { PanelState } from './lib/types';
import { roles } from './lib/messages';
import { SessionProvider, useIdentity, useSession } from './context/session';
import { DraftProvider, useDraftState } from './context/drafts';
import {
  ConfirmProvider,
  ErrorNotice,
  Loading,
  ToastProvider,
  useConfirm,
  useToast,
} from './components/ui';
import { Login } from './pages/Login';
import { DeletionProvider } from './components/Deletion';
import { MaintenanceBanner } from './components/Runtime';
const pages = [
  ['overview', '工作台', '工作空间', '维护技能与配置，查看最近变更'],
  ['chat', '聊天', '工作空间', '选择机器人与会话'],
  ['skills', '技能编辑', '维护资源', '正文与版本历史'],
  ['wakers', '机器人', '维护资源', '名称、描述与技能开关'],
  ['models', '模型配置', '维护资源', 'Provider 与默认模型'],
  ['memory', '记忆', '维护资源', '查看分身记忆与工作区文件'],
  ['plugins', '插件', '维护资源', '插件目录、版本与安装状态'],
  ['channels', 'IM 通道', '维护资源', '连接与管理消息通道'],
  ['native', '官方操作', '维护资源', '完整官方命令与参数'],
  ['audit', '变更记录', '运行与观测', '查看技能和配置的维护记录'],
  ['sessions', '会话管理', '运行与观测', '筛选会话、查看产物'],
  ['usage', '用量', '运行与观测', '本机日志中的模型回合'],
  ['operations', '运行管理', '运行与观测', '系统状态、自动化与配对审批'],
  ['users', '维护者', '系统管理', '账号、角色与管理范围'],
  ['gateway', 'API 调用', '系统管理', '连接业务程序与机器人'],
  ['callers', '调用密钥', '系统管理', '授权范围、请求额度与有效期'],
  ['security', '安全与备份', '系统管理', '本地备份、遥测与访问配置'],
  ['controls', '实例维护', '系统管理', '维护状态、启动策略与文件检查'],
  ['net', '网络与网关', '系统管理', '控制面策略与本机连接'],
  ['account', '我的账号', '系统管理', '修改密码与管理登录'],
] as const;
export function App() {
  const session = useSession();
  return (
    <ConfirmProvider>
      <ToastProvider>
        {session.isPending ? (
          <Loading />
        ) : session.data ? (
          <SessionProvider identity={session.data}>
            <DraftProvider>
              <DeletionProvider>
                <Shell />
              </DeletionProvider>
            </DraftProvider>
          </SessionProvider>
        ) : (
          <>
            <Login />
            {session.error &&
              session.error instanceof Error &&
              'status' in session.error &&
              session.error.status !== 401 && <ErrorNotice error={session.error} />}
          </>
        )}
      </ToastProvider>
    </ConfirmProvider>
  );
}
function Shell() {
  const identity = useIdentity();
  const client = useQueryClient();
  const location = useLocation();
  const confirm = useConfirm();
  const toast = useToast();
  const { dirty } = useDraftState();
  const [search, setSearch] = useState('');
  const [theme, setTheme] = useState(() => {
    try {
      return localStorage.getItem('qw-theme') || 'auto';
    } catch {
      return 'auto';
    }
  });
  const searchRef = useRef<HTMLInputElement>(null);
  const state = useQuery({
    queryKey: ['state'],
    queryFn: ({ signal }) => api<PanelState>('state', { signal }),
  });
  const current = pages.find((p) => location.pathname === '/' + p[0]) || pages[0];
  useEffect(() => {
    window.scrollTo({ top: 0, behavior: 'auto' });
  }, [location.pathname]);
  useEffect(() => {
    const media = matchMedia('(prefers-color-scheme: dark)');
    const update = () => {
      document.documentElement.dataset.theme =
        theme === 'auto' ? (media.matches ? 'dark' : 'light') : theme;
    };
    update();
    media.addEventListener('change', update);
    try {
      localStorage.setItem('qw-theme', theme);
    } catch {}
    return () => media.removeEventListener('change', update);
  }, [theme]);
  useEffect(() => {
    const listener = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        searchRef.current?.focus();
        searchRef.current?.select();
      }
    };
    window.addEventListener('keydown', listener);
    return () => window.removeEventListener('keydown', listener);
  }, []);
  const visible = pages
    .filter(([id]) =>
      identity.level === 'admin'
        ? id !== 'account' || !!identity.userId
        : identity.userId
          ? ['overview', 'wakers', 'skills', 'sessions', 'native', 'audit', 'account'].includes(id)
          : ['overview', 'wakers', 'models', 'plugins', 'usage'].includes(id),
    )
    .filter((p) => p.join(' ').toLowerCase().includes(search.toLowerCase()));
  async function logout() {
    if (dirty && !(await confirm('有未保存的修改，放弃草稿并退出？'))) return;
    try {
      await api('logout', { body: {} });
      await client.cancelQueries();
      client.removeQueries({ predicate: (q) => q.queryKey[0] !== 'identity' });
      setCSRF('');
      client.setQueryData(['identity'], null);
    } catch (error) {
      toast(error instanceof Error ? error.message : '退出未完成');
    }
  }
  return (
    <div className="app">
      <a
        className="skip-link"
        href="#content"
        onClick={(e) => {
          e.preventDefault();
          document.getElementById('content')?.focus();
        }}
      >
        跳到工作区
      </a>
      <aside className="sidebar">
        <Link className="brand" to="/overview">
          <strong>QoderWake</strong>
          <span className="brand-sub">Panel</span>
        </Link>
        <label className="sr-only" htmlFor="nav-search">
          查找页面
        </label>
        <input
          id="nav-search"
          ref={searchRef}
          type="search"
          placeholder="查找页面  ⌘ K"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <nav id="nav" aria-label="主导航">
          {['工作空间', '维护资源', '运行与观测', '系统管理'].map((group) => {
            const items = visible.filter((p) => p[2] === group);
            return items.length ? (
              <section className="react-nav-group" key={group}>
                <h2>{group}</h2>
                {items.map(([id, title]) => (
                  <NavLink
                    key={id}
                    className={({ isActive }) => 'nav-item' + (isActive ? ' active' : '')}
                    to={'/' + id}
                  >
                    {id === 'skills' &&
                    !identity.capabilities.includes('skill.write') &&
                    identity.level !== 'admin'
                      ? '技能'
                      : title}
                  </NavLink>
                ))}
              </section>
            ) : null;
          })}
        </nav>
        <div className="sidebar-bottom">
          <label htmlFor="theme">外观</label>
          <select id="theme" value={theme} onChange={(e) => setTheme(e.target.value)}>
            {Object.entries({
              auto: '跟随系统',
              light: '浅色',
              dark: '深色',
              'light-parchment': '暖白',
              'dark-parchment': '暖黑',
            }).map(([key, label]) => (
              <option value={key} key={key}>
                {label}
              </option>
            ))}
          </select>
          <small className="version">Panel {identity.version}</small>
        </div>
      </aside>
      <main>
        <header className="workspace-bar">
          <div className="instance-status">
            <span
              className={'status-dot ' + (state.data?.daemon === 'running' ? 'connected' : '')}
            />
            <span>当前实例</span>
            <span className="muted">
              {state.data?.error
                ? '暂不可用'
                : state.data?.daemon === 'running'
                  ? '已连接'
                  : '未连接'}
            </span>
          </div>
          <div className="workspace-meta">
            {!identity.transportSecure && (
              <details className="transport-status">
                <summary>HTTP</summary>
                <p>当前连接未加密，请使用可信网络、SSH 隧道或 HTTPS。</p>
              </details>
            )}
            <div className="identity">
              <strong>{identity.displayName || roles[identity.level]}</strong>
              <span className="role-label">{roles[identity.level]}</span>
              <button onClick={() => void logout()}>退出</button>
            </div>
          </div>
        </header>
        <div className="page-body">
          <MaintenanceBanner />
          <header className="page-header">
            <div>
              <div className="eyebrow">{current[2]}</div>
              <h1>
                {current[0] === 'skills' && identity.level === 'observer' ? '技能' : current[1]}
              </h1>
              <p className="muted">
                {current[0] === 'sessions' && identity.level !== 'admin'
                  ? '查看已分配机器人的会话'
                  : current[0] === 'overview' &&
                      !identity.capabilities.includes('skill.write') &&
                      identity.level !== 'admin'
                    ? '查看技能、配置与变更记录'
                    : current[3]}
              </p>
            </div>
            <button
              onClick={() =>
                void client.invalidateQueries({ predicate: (q) => q.queryKey[0] !== 'identity' })
              }
            >
              刷新
            </button>
          </header>
          <div id="content" tabIndex={-1}>
            <Suspense fallback={<Loading />}>
              <Outlet />
            </Suspense>
          </div>
          <footer className="page-footer">QoderWake Panel · 协作维护工作台</footer>
        </div>
      </main>
    </div>
  );
}
