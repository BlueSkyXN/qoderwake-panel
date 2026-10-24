import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../lib/api';
import { useIdentity } from '../context/session';
import { usePanelState } from '../components/resources';
import { Card, Empty, ErrorNotice, Loading, Table, useConfirm, useToast } from '../components/ui';
interface Backup {
  name: string;
  size: number;
  mtime: string;
}
export function Security() {
  const identity = useIdentity();
  const state = usePanelState();
  const client = useQueryClient();
  const confirm = useConfirm();
  const toast = useToast();
  const backups = useQuery({
    queryKey: ['backups'],
    queryFn: ({ signal }) => api<Backup[]>('backups', { signal }),
    enabled: identity.level === 'admin',
  });
  const create = useMutation({
    mutationFn: () => api<{ name: string }>('backup', { body: {} }),
    onSuccess: async () => {
      toast('备份已保存到宿主机');
      await client.invalidateQueries({ queryKey: ['backups'] });
    },
  });
  const remove = useMutation({
    mutationFn: async (row: Backup) => {
      if (!(await confirm('删除 ' + row.name + '？此备份无法恢复。', '删除备份'))) return;
      await api('backup/delete', { body: { name: row.name, confirm: '/api/backup/delete' } });
      toast('备份已删除');
      await client.invalidateQueries({ queryKey: ['backups'] });
    },
  });
  const telemetry = useMutation({
    mutationFn: async (enabled: boolean) => {
      if (!(await confirm((enabled ? '开启' : '关闭') + '官方遥测？'))) return;
      await api('telemetry', { body: { enabled, confirm: '/api/telemetry' } });
      toast('遥测配置已保存');
      await client.invalidateQueries({ queryKey: ['state'] });
    },
  });
  if (identity.level !== 'admin') return <Empty>此功能仅管理员可用。</Empty>;
  if (state.isPending) return <Loading />;
  if (state.error) return <ErrorNotice error={state.error} />;
  const data = state.data!;
  return (
    <>
      <Card title="访问与更新状态">
        <dl className="facts">
          <dt>登录方式</dt>
          <dd>{identity.userId ? '独立账号与角色' : '共享访问令牌'}</dd>
          <dt>运行时更新目录</dt>
          <dd>{data.guard == null ? '未读取' : data.guard ? '未设置写权限' : '具有写权限'}</dd>
        </dl>
      </Card>
      <Card
        title="本地备份"
        actions={
          <button className="primary" disabled={create.isPending} onClick={() => create.mutate()}>
            {create.isPending ? '正在备份…' : '创建备份'}
          </button>
        }
      >
        <p className="fine muted">备份包含凭据，仅在宿主机保存，文件权限为 600。</p>
        {backups.isPending ? (
          <Loading />
        ) : backups.error ? (
          <ErrorNotice error={backups.error} retry={() => void backups.refetch()} />
        ) : backups.data?.length ? (
          <Table head={['文件', '大小', '时间', '操作']}>
            {backups.data.map((row) => (
              <tr key={row.name}>
                <td className="mono break-text">{row.name}</td>
                <td>{(row.size / 1024).toFixed(1)} KiB</td>
                <td>{row.mtime}</td>
                <td>
                  <button
                    className="danger"
                    disabled={remove.isPending}
                    onClick={() => remove.mutate(row)}
                  >
                    删除
                  </button>
                </td>
              </tr>
            ))}
          </Table>
        ) : (
          <Empty>尚未创建备份。</Empty>
        )}
        <ErrorNotice error={create.error || remove.error} />
      </Card>
      <Card title="遥测开关">
        <p>
          当前配置：
          <strong>
            {data.telemetry === true ? '开启' : data.telemetry === false ? '关闭' : '未读取'}
          </strong>
        </p>
        <div className="row">
          <button
            disabled={telemetry.isPending || data.telemetry === false}
            onClick={() => telemetry.mutate(false)}
          >
            关闭遥测
          </button>
          <button
            disabled={telemetry.isPending || data.telemetry === true}
            onClick={() => telemetry.mutate(true)}
          >
            开启遥测
          </button>
        </div>
        <ErrorNotice error={telemetry.error} />
      </Card>
      <Card title="出网白名单（静态清单）">
        {data.whitelist?.length ? (
          <Table head={['IP', '配置标签']}>
            {data.whitelist.map((row) => (
              <tr key={row.ip}>
                <td className="mono">{row.ip}</td>
                <td>{row.label}</td>
              </tr>
            ))}
          </Table>
        ) : (
          <Empty>尚未配置白名单。</Empty>
        )}
      </Card>
    </>
  );
}
