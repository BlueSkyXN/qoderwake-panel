import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, query } from '../lib/api';
import { useIdentity } from '../context/session';
import {
  usePanelState,
  hasNext,
  Pager,
  WakerPicker,
  activityStatus,
} from '../components/resources';
import type { PageResult } from '../components/resources';
import type { Waker } from '../lib/types';
import { Card, Empty, ErrorNotice, Loading, Table, useConfirm, useToast } from '../components/ui';
interface Automation {
  id: string;
  triggerId: string;
  triggerName: string;
  taskDescription?: string;
  model?: string;
  enabled: boolean;
}
interface Run {
  runId: string;
  startedAt: string;
  finishedAt: string;
  status: string;
  sessionId: string;
}
interface Pairing {
  pendingId: string;
  pendingRevision: string | number;
  remoteExpiresAt: string;
  channelId: string;
  senderId: string;
  conversationId: string;
  bindingKey: string;
  conversationType: string;
  conversationName?: string;
  displayName?: string;
  subjectName?: string;
}
interface System {
  version?: string;
  update?: { runningVersion?: string; installedVersion?: string; restartRequired?: boolean };
  activity?: {
    runningSessions?: number;
    runningConversationTasks?: number;
    runningTriggerTasks?: number;
    queuedTriggerTasks?: number;
  };
}
export function Operations() {
  const identity = useIdentity();
  const state = usePanelState();
  const confirm = useConfirm();
  const toast = useToast();
  const client = useQueryClient();
  const [page, setPage] = useState(1);
  const [pairingPage, setPairingPage] = useState(1);
  const [selected, setSelected] = useState('');
  const allowed = identity.level === 'admin';
  const system = useQuery({
    queryKey: ['system'],
    queryFn: ({ signal }) => api<System>('system/status', { signal }),
    enabled: allowed,
  });
  const triggers = useQuery({
    queryKey: ['automations', page],
    queryFn: ({ signal }) =>
      api<PageResult<Automation>>('automations?' + query({ page }), { signal }),
    enabled: allowed,
  });
  const pairings = useQuery({
    queryKey: ['pairings', pairingPage],
    queryFn: ({ signal }) =>
      api<{ items: Pairing[]; total: number }>(
        'pairings?' + query({ limit: 20, offset: (pairingPage - 1) * 20 }),
        { signal },
      ),
    enabled: allowed,
  });
  const action = useMutation({
    mutationFn: async ({
      row,
      action,
    }: {
      row: Automation;
      action: 'run' | 'toggle' | 'delete';
    }) => {
      const message =
        action === 'run'
          ? '立即执行可能消耗模型额度、运行命令或访问网络，确认提交？'
          : action === 'delete'
            ? '永久删除 ' + (row.triggerName || row.triggerId || row.id) + ' 及其定义？'
            : (row.enabled ? '停用 ' : '启用 ') +
              (row.triggerName || row.triggerId || row.id) +
              '？';
      if (!(await confirm(message))) return;
      await api('automation/' + action, {
        body: {
          id: row.triggerId || row.id,
          enabled: !row.enabled,
          confirm: '/api/automation/' + action,
        },
      });
      toast(action === 'run' ? '已提交执行，可查看运行历史。' : '自动化已更新');
      await client.invalidateQueries({ queryKey: ['automations'] });
      await client.invalidateQueries({ queryKey: ['automation-runs'] });
      await client.invalidateQueries({ queryKey: ['audit'] });
    },
  });
  if (!allowed) return <Empty>此功能仅管理员可用。</Empty>;
  return (
    <>
      <div className="grid">
        {[
          ['运行版本', system.data?.update?.runningVersion || system.data?.version || '未读取'],
          ['安装版本', system.data?.update?.installedVersion || '未读取'],
          ['运行会话', system.data?.activity?.runningSessions ?? '未读取'],
          ['待审批配对', pairings.data?.total ?? '未读取'],
        ].map(([label, value]) => (
          <div className="kpi" key={label}>
            <span>{label}</span>
            <strong>{value}</strong>
          </div>
        ))}
      </div>
      <ErrorNotice error={system.error} retry={() => void system.refetch()} />
      {system.data?.update?.restartRequired && (
        <p className="notice">已安装的版本需要重启后生效。</p>
      )}
      <Card title="自动化">
        {triggers.isPending ? (
          <Loading />
        ) : triggers.error ? (
          <ErrorNotice error={triggers.error} retry={() => void triggers.refetch()} />
        ) : triggers.data?.items.length ? (
          <Table head={['名称 / 模型', '状态', '操作']}>
            {triggers.data.items.map((row) => (
              <tr key={row.triggerId || row.id}>
                <td>
                  <strong>{row.triggerName || row.triggerId || row.id}</strong>
                  <p className="muted">{row.taskDescription || row.model}</p>
                </td>
                <td>{row.enabled ? '启用' : '停用'}</td>
                <td className="actions">
                  <button onClick={() => setSelected(row.triggerId || row.id)}>历史</button>
                  <button
                    disabled={action.isPending}
                    onClick={() => action.mutate({ row, action: 'toggle' })}
                  >
                    {row.enabled ? '停用' : '启用'}
                  </button>
                  <button
                    disabled={action.isPending}
                    onClick={() => action.mutate({ row, action: 'run' })}
                  >
                    立即运行
                  </button>
                  <button
                    className="danger"
                    disabled={action.isPending}
                    onClick={() => action.mutate({ row, action: 'delete' })}
                  >
                    删除
                  </button>
                </td>
              </tr>
            ))}
          </Table>
        ) : (
          <Empty>本页没有自动化。</Empty>
        )}
        <Pager
          page={page}
          more={hasNext(triggers.data, page)}
          busy={triggers.isFetching}
          onChange={setPage}
        />
        <ErrorNotice error={action.error} />
      </Card>
      {selected && <Runs key={selected} id={selected} />}
      <Card title="IM 配对审批">
        {pairings.isPending ? (
          <Loading />
        ) : pairings.error ? (
          <ErrorNotice error={pairings.error} retry={() => void pairings.refetch()} />
        ) : pairings.data?.items.length ? (
          <Table head={['请求', '目标机器人', '操作']}>
            {pairings.data.items.map((row) => (
              <PairingRow
                key={row.pendingId}
                row={row}
                offset={(pairingPage - 1) * 20}
                wakers={state.data?.wakers || []}
              />
            ))}
          </Table>
        ) : (
          <Empty>本页无待审批请求。</Empty>
        )}
        <ErrorNotice error={state.error} />
        <Pager
          page={pairingPage}
          more={pairingPage * 20 < (pairings.data?.total || 0)}
          busy={pairings.isFetching}
          onChange={setPairingPage}
        />
      </Card>
    </>
  );
}
function Runs({ id }: { id: string }) {
  const [page, setPage] = useState(1);
  const data = useQuery({
    queryKey: ['automation-runs', id, page],
    queryFn: ({ signal }) =>
      api<PageResult<Run>>('automation/runs?' + query({ id, page }), { signal }),
  });
  return (
    <Card title="运行历史">
      {data.isPending ? (
        <Loading />
      ) : data.error ? (
        <ErrorNotice error={data.error} />
      ) : data.data?.items.length ? (
        <Table head={['开始', '结束', '状态', '会话']}>
          {data.data.items.map((row, index) => (
            <tr key={row.runId || index}>
              <td>{row.startedAt}</td>
              <td>{row.finishedAt || '—'}</td>
              <td>{activityStatus(row.status)}</td>
              <td className="mono">{row.sessionId || '—'}</td>
            </tr>
          ))}
        </Table>
      ) : (
        <Empty>暂无运行记录。</Empty>
      )}
      <Pager
        page={page}
        more={hasNext(data.data, page)}
        busy={data.isFetching}
        onChange={setPage}
      />
    </Card>
  );
}
function PairingRow({ row, wakers, offset }: { row: Pairing; wakers: Waker[]; offset: number }) {
  const [target, setTarget] = useState('');
  const confirm = useConfirm();
  const client = useQueryClient();
  const toast = useToast();
  const wid = target || wakers[0]?.id || '';
  const approve = useMutation({
    mutationFn: async () => {
      if (
        !(await confirm(
          '允许 ' +
            (row.displayName || row.conversationName || row.senderId) +
            ' 触发 ' +
            (wakers.find((w) => w.id === wid)?.name || wid) +
            '？命令和自定义模型权限保持关闭。',
          '批准配对',
        ))
      )
        return;
      await api('pairing/approve', {
        body: { ...row, wakerId: wid, pendingOffset: offset, confirm: '/api/pairing/approve' },
      });
      toast('配对已批准');
      await client.invalidateQueries({ queryKey: ['pairings'] });
      await client.invalidateQueries({ queryKey: ['audit'] });
    },
  });
  return (
    <tr>
      <td>
        <strong>{row.displayName || row.conversationName || row.senderId}</strong>
        <p className="mono fine">
          {row.channelId} ·{' '}
          {row.conversationType === 'group'
            ? '群聊'
            : row.conversationType === 'direct'
              ? '私聊'
              : row.conversationType}
        </p>
        <p className="fine muted">有效期 {row.remoteExpiresAt || '未提供'}</p>
      </td>
      <td>
        <WakerPicker
          label={'配对目标 ' + (row.displayName || row.pendingId)}
          items={wakers}
          value={wid}
          disabled={approve.isPending}
          onChange={setTarget}
        />
      </td>
      <td>
        <button
          disabled={!wid || approve.isPending || row.pendingRevision == null}
          onClick={() => approve.mutate()}
        >
          批准配对
        </button>
        <ErrorNotice error={approve.error} />
      </td>
    </tr>
  );
}
