import { useQuery } from '@tanstack/react-query';
import { api } from '../lib/api';
import type { AuditRow } from '../lib/types';
import { Card, Empty, ErrorNotice, Loading, Table } from '../components/ui';
const actions: Record<string, string> = {
  '/api/skill/update': '保存技能',
  '/api/skill/rollback': '恢复技能版本',
  '/api/waker/update': '修改 Waker 配置',
  '/api/waker': '创建 Waker',
  '/api/waker/delete': '删除 Waker',
  '/api/provider': '保存 Provider',
  '/api/provider/delete': '删除 Provider',
  '/api/preference': '设置默认模型',
  '/api/native/execute': '执行官方操作',
  '/api/users/create': '新增维护者',
  '/api/users/update': '修改维护者',
  '/api/users/password': '重置账号密码',
  '/api/account/password': '修改密码',
};
export function Audit() {
  const data = useQuery({
    queryKey: ['audit'],
    queryFn: ({ signal }) => api<AuditRow[]>('audit', { signal }),
  });
  if (data.isPending) return <Loading />;
  return (
    <Card title="维护变更">
      <ErrorNotice error={data.error} retry={() => void data.refetch()} />
      {data.data?.length ? (
        <Table head={['时间 / 操作者', '动作 / 资源', '结果', '版本']}>
          {data.data.map((r, i) => (
            <tr key={i}>
              <td>
                {r.ts}
                <br />
                {r.displayName || r.principal}
              </td>
              <td>
                {actions[r.action] || r.action}
                <br />
                <span className="mono">{r.target}</span>
              </td>
              <td>
                {r.ok ? '完成' : '未完成'} · {r.status}
              </td>
              <td className="mono">
                {r.beforeRevision || '—'}
                <br />→ {r.afterRevision || '—'}
              </td>
            </tr>
          ))}
        </Table>
      ) : (
        <Empty>暂无可查看的变更。</Empty>
      )}
    </Card>
  );
}
