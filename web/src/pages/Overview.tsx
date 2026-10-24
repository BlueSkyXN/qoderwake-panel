import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { api } from '../lib/api';
import type { PanelState, AuditRow } from '../lib/types';
import { can, useIdentity } from '../context/session';
import { Card, Empty, ErrorNotice, Loading } from '../components/ui';
export function Overview() {
  const identity = useIdentity();
  const state = useQuery({
    queryKey: ['state'],
    queryFn: ({ signal }) => api<PanelState>('state', { signal }),
  });
  const audit = useQuery({
    queryKey: ['audit'],
    queryFn: ({ signal }) => api<AuditRow[]>('audit', { signal }),
    enabled: can(identity, 'audit.read'),
  });
  if (state.isPending) return <Loading />;
  if (state.error) return <ErrorNotice error={state.error} retry={() => void state.refetch()} />;
  const data = state.data!;
  return (
    <>
      <div className="grid">
        {[
          ['实例状态', data.error ? '暂不可用' : data.daemon === 'running' ? '已连接' : '未连接'],
          ['可维护的 Waker', data.wakers.length],
          ['当前身份', identity.displayName || identity.username || '共享访问'],
        ].map(([label, value]) => (
          <div className="kpi" key={label}>
            <span>{label}</span>
            <strong>{value}</strong>
          </div>
        ))}
      </div>
      <div className="quick-links">
        {can(identity, 'skill.read') && (
          <Link to="/skills">{can(identity, 'skill.write') ? '编辑技能' : '查看技能'}</Link>
        )}
        <Link to="/wakers">{can(identity, 'waker.write') ? '维护配置' : '查看配置'}</Link>
        {(identity.level === 'admin' || identity.userId) && <Link to="/native">查找官方操作</Link>}
      </div>
      <div className="overview-layout">
        <section>
          <div className="overview-heading">
            <h2>维护资源</h2>
            <Link to="/wakers">查看全部</Link>
          </div>
          <div className="resource-list">
            {data.wakers.map((w) => (
              <div className="resource-row" key={w.id}>
                <div>
                  <strong>{w.name}</strong>
                  <p>
                    {w.id} · {w.preference}
                  </p>
                </div>
                {can(identity, 'skill.read') && (
                  <Link className="text-link" to={'/skills?waker=' + encodeURIComponent(w.id)}>
                    查看技能
                  </Link>
                )}
              </div>
            ))}
            {!data.wakers.length && (
              <Empty>
                {data.error
                  ? '暂时无法读取资源。'
                  : identity.level === 'admin'
                    ? '请在机器人页面创建 Waker。'
                    : '尚未分配 Waker，请联系管理员。'}
              </Empty>
            )}
          </div>
        </section>
        <section>
          <div className="overview-heading">
            <h2>最近变更</h2>
            {can(identity, 'audit.read') && <Link to="/audit">查看记录</Link>}
          </div>
          <div className="activity-list">
            <ErrorNotice error={audit.error} />
            {audit.data?.slice(0, 6).map((a, i) => (
              <div key={i} className="activity-row">
                <span className={'activity-outcome' + (a.ok ? '' : ' failed')}>
                  {a.ok ? '完成' : '未完成'}
                </span>
                <p>{a.target || a.action}</p>
                <small>
                  {a.displayName || a.role} · {a.ts}
                </small>
              </div>
            ))}
            {!audit.data?.length && <Empty>暂无可查看的变更。</Empty>}
          </div>
        </section>
      </div>
      {identity.level === 'admin' && (
        <Card title="实例信息">
          <dl className="facts">
            <dt>Panel 版本</dt>
            <dd>{data.version}</dd>
            <dt>模型数量</dt>
            <dd>{data.models.length}</dd>
          </dl>
        </Card>
      )}
    </>
  );
}
