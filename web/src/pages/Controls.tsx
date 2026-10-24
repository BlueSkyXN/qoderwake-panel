import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../lib/api';
import { useIdentity } from '../context/session';
import { useDirtyGuard } from '../context/drafts';
import { maintenanceLabels, useMaintenance, useRuntime } from '../components/Runtime';
import type { Policy, RuntimeState } from '../components/Runtime';
import {
  Card,
  Empty,
  ErrorNotice,
  Field,
  Loading,
  Table,
  useConfirm,
  useToast,
} from '../components/ui';

const normalize = (p: Policy): Policy => ({
  hotDeploy: p.hotDeploy,
  embeddingDisabled: p.embeddingDisabled,
  sdkByok: p.sdkByok !== false,
  sessionProjectionUplink: p.sessionProjectionUplink ?? null,
  remoteExecutionUplink: p.remoteExecutionUplink ?? null,
});
const switchLabel = (value: unknown, fallback = '未读取') =>
  value === true ? '开启' : value === false ? '关闭' : fallback;

export function Controls() {
  const identity = useIdentity();
  const runtime = useRuntime();
  if (identity.level !== 'admin') return <Empty>此功能仅管理员可用。</Empty>;
  return (
    <>
      <MaintenanceControl />
      {runtime.isPending ? (
        <Loading />
      ) : runtime.error ? (
        <ErrorNotice error={runtime.error} />
      ) : (
        <RuntimeEditor state={runtime.data!} />
      )}
      <FileChecks />
      <PatchControl />
    </>
  );
}

interface PatchState {
  state: string;
  verified: boolean;
  sha256?: string;
  version?: string;
  platformSupported: boolean;
}
interface PatchPlan {
  id: string;
  version: string;
  module: string;
  sha256: string;
  warning: string;
}
function PatchControl() {
  const client = useQueryClient();
  const confirm = useConfirm();
  const toast = useToast();
  const state = useQuery({
    queryKey: ['patch'],
    queryFn: ({ signal }) => api<PatchState>('patch/state', { signal }),
  });
  const mutation = useMutation({
    mutationFn: async (action: 'apply' | 'restore') => {
      const plan = await api<PatchPlan>('patch/plan', { body: { action } });
      if (
        !(await confirm(
          `${action === 'apply' ? '应用' : '还原'} QCS 端点补丁 · ${plan.version}？\nSHA-256：${plan.sha256}\n${plan.warning}`,
          action === 'apply' ? '应用补丁' : '还原补丁',
        ))
      )
        return;
      await api('patch/execute', { body: { plan: plan.id, confirm: '/api/patch/execute' } });
      toast(action === 'apply' ? '补丁已应用' : '原文件已恢复');
    },
    onSettled: async () => {
      await client.invalidateQueries({ queryKey: ['patch'] });
    },
  });
  const labels: Record<string, string> = {
    original: '原始文件',
    patched: '已应用补丁',
    unsupported: '未匹配登记版本',
    unavailable: '文件或注册表无法读取',
  };
  return (
    <Card title="QCS 补丁">
      <span className="badge">CN 遗产兼容</span>
      {state.data && (
        <>
          <p>
            {labels[state.data.state] || '状态未知'}
            {state.data.version && ` · ${state.data.version}`}
          </p>
          {state.data.sha256 && <p className="mono break-text">{state.data.sha256}</p>}
          <p className="fine muted">
            仅适用于 Linux 上已登记的 CN 版本。应用或还原前，请先停止 daemon。
          </p>
          <div className="form-actions">
            <button
              disabled={
                !state.data.platformSupported ||
                state.data.state !== 'original' ||
                mutation.isPending
              }
              onClick={() => mutation.mutate('apply')}
            >
              准备应用
            </button>
            <button
              disabled={
                !state.data.platformSupported ||
                state.data.state !== 'patched' ||
                mutation.isPending
              }
              onClick={() => mutation.mutate('restore')}
            >
              准备还原
            </button>
          </div>
        </>
      )}
      <ErrorNotice error={state.error || mutation.error} />
    </Card>
  );
}
function MaintenanceControl() {
  const state = useMaintenance();
  const confirm = useConfirm();
  const client = useQueryClient();
  const [reason, setReason] = useState('管理员手动维护');
  const [ttl, setTTL] = useState(300);
  const mutation = useMutation({
    mutationFn: async (enter: boolean) => {
      if (
        enter &&
        !(await confirm('进入维护模式？新的业务写请求将暂停，已有请求会先排空。', '进入维护'))
      )
        return;
      await api(enter ? 'maintenance/enter' : 'maintenance/exit', {
        body: enter ? { reason, ttl, confirm: '/api/maintenance/enter' } : {},
      });
      await client.invalidateQueries({ queryKey: ['maintenance'] });
    },
  });
  const data = state.data;
  return (
    <Card title="维护模式">
      {state.isPending ? (
        <Loading />
      ) : state.error ? (
        <ErrorNotice error={state.error} />
      ) : (
        data && (
          <>
            <div className="row">
              <strong className="badge">{maintenanceLabels[data.mode]}</strong>
              <span>
                活动写请求 {data.activeMutations} · 消费请求 {data.activeCalls}
              </span>
            </div>
            {data.reason && data.mode !== 'normal' && <p>{data.reason}</p>}
            {data.expires && (
              <p className="fine muted">
                将在 {new Date(data.expires * 1000).toLocaleTimeString()} 自动退出
              </p>
            )}
            {data.mode === 'normal' ? (
              <form
                onSubmit={(event) => {
                  event.preventDefault();
                  mutation.mutate(true);
                }}
              >
                <div className="form-grid">
                  <Field label="维护原因">
                    <input
                      value={reason}
                      maxLength={200}
                      required
                      onChange={(event) => setReason(event.target.value)}
                    />
                  </Field>
                  <Field label="维护时长（秒）">
                    <input
                      type="number"
                      min={30}
                      max={3600}
                      required
                      value={ttl}
                      onChange={(event) => setTTL(Number(event.target.value))}
                    />
                  </Field>
                </div>
                <div className="form-actions">
                  <button disabled={mutation.isPending}>进入维护</button>
                </div>
              </form>
            ) : (
              <div className="form-actions">
                <button
                  disabled={data.mode !== 'maintenance' || mutation.isPending}
                  onClick={() => mutation.mutate(false)}
                >
                  退出维护
                </button>
              </div>
            )}
            <p className="fine muted">维护模式作用于 Panel 的写请求，其他客户端仍可访问 daemon。</p>
          </>
        )
      )}
      <ErrorNotice error={mutation.error} />
    </Card>
  );
}
function RuntimeEditor({ state }: { state: RuntimeState }) {
  const initial = normalize(state.desired);
  const [form, setForm] = useState(initial);
  const [saved, setSaved] = useState(initial);
  const client = useQueryClient();
  const confirm = useConfirm();
  const toast = useToast();
  const dirty = JSON.stringify(form) !== JSON.stringify(saved);
  const desired = JSON.stringify(initial);
  useDirtyGuard(dirty);
  useEffect(() => {
    if (!dirty) {
      const next: Policy = JSON.parse(desired);
      setForm(next);
      setSaved(next);
    }
  }, [desired]);
  const mutation = useMutation({
    mutationFn: async (restart: boolean) => {
      if (
        restart &&
        !(await confirm(
          '保存启动策略并重启 daemon？会先排空请求并等待 daemon 空闲。',
          '保存并重启',
        ))
      )
        return;
      await api('runtime/policy', { body: { ...form, confirm: '/api/runtime/policy' } });
      setSaved(form);
      toast('启动策略已保存');
      if (restart) await api('runtime/apply', { body: { confirm: '/api/runtime/apply' } });
    },
    onSettled: async () => {
      await Promise.all(
        ['runtime', 'state', 'maintenance'].map((key) =>
          client.invalidateQueries({ queryKey: [key] }),
        ),
      );
    },
  });
  const current = state.observed[0];
  const next = state.effectiveOnPanelRestart;
  const rows = [
    ['自动热部署', switchLabel(current?.hotDeploy), switchLabel(next.hotDeploy)],
    [
      '官方记忆向量化',
      switchLabel(current?.embeddingDisabled == null ? null : !current.embeddingDisabled),
      switchLabel(next.embeddingDisabled == null ? null : !next.embeddingDisabled),
    ],
    [
      '自定义 Provider 通道',
      switchLabel(current ? (current.sdkByok ?? false) : null),
      switchLabel(next.sdkByok),
    ],
    [
      '会话投影上行',
      switchLabel(
        current ? state.observedUplink.sessionProjectionUplink : null,
        current ? '未设置' : '未读取',
      ),
      switchLabel(next.sessionProjectionUplink, '继承环境'),
    ],
    [
      '远程执行上行',
      switchLabel(
        current ? state.observedUplink.remoteExecutionUplink : null,
        current ? '未设置' : '未读取',
      ),
      switchLabel(next.remoteExecutionUplink, '继承环境'),
    ],
  ];
  return (
    <Card title="启动策略">
      <p>
        {state.pendingRestart == null
          ? '当前进程策略未读取'
          : state.pendingRestart || state.sdkByokPending
            ? '已保存的策略待应用'
            : '运行配置与保存配置一致'}
      </p>
      <Table head={['选项', '当前进程', '已保存 · 下次启动']}>
        {rows.map((row) => (
          <tr key={row[0]}>
            {row.map((cell, i) => (
              <td key={i}>{cell}</td>
            ))}
          </tr>
        ))}
      </Table>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          mutation.mutate(false);
        }}
      >
        <fieldset disabled={mutation.isPending} className="runtime-fields">
          {(
            [
              ['sdkByok', '启用自定义 Provider 通道（BYOK）'],
              ['hotDeploy', '允许自动热部署'],
              ['embeddingDisabled', '禁用官方记忆向量化（Embedding）'],
            ] as const
          ).map(([key, title]) => (
            <label key={key} className="check-row">
              <input
                type="checkbox"
                checked={!!form[key]}
                onChange={(event) => setForm({ ...form, [key]: event.target.checked })}
              />
              {title}
            </label>
          ))}
          <div className="form-grid">
            {(
              [
                ['sessionProjectionUplink', '会话投影上行'],
                ['remoteExecutionUplink', '远程执行上行'],
              ] as const
            ).map(([key, title]) => (
              <Field key={key} label={title}>
                <select
                  value={form[key] == null ? 'inherit' : form[key] ? 'on' : 'off'}
                  onChange={(event) =>
                    setForm({
                      ...form,
                      [key]: event.target.value === 'inherit' ? null : event.target.value === 'on',
                    })
                  }
                >
                  <option value="inherit">继承启动环境</option>
                  <option value="on">开启</option>
                  <option value="off">关闭</option>
                </select>
              </Field>
            ))}
          </div>
          <details>
            <summary>选项影响</summary>
            <p>
              关闭 BYOK 后，自有 Provider
              将不可用。关闭上行选项会影响会话同步或远程执行反馈；已关闭的上行项重新开启时，需先停止再启动
              daemon。
            </p>
            <p>继承会保留启动环境中的开关；未配置时使用官方默认。自动热部署开关仅控制热部署。</p>
          </details>
          <div className="form-actions">
            <button className="primary">保存策略</button>
            <button
              type="button"
              disabled={!state.restartAllowed}
              onClick={() => mutation.mutate(true)}
            >
              保存并重启
            </button>
          </div>
        </fieldset>
        {!state.restartAllowed && (
          <p className="fine muted">无法核实受管进程，请在宿主机检查实例状态。</p>
        )}
        <ErrorNotice error={mutation.error} />
      </form>
    </Card>
  );
}
interface FileCheck {
  status: string;
  checkedAt?: number;
  files?: {
    name: string;
    kind?: string;
    path?: string;
    status?: string;
    sha256?: string | null;
    sameFile?: boolean | null;
    matchesRecorded?: boolean | null;
    present?: boolean;
    changedDuringCheck?: boolean;
  }[];
}
function FileChecks() {
  const client = useQueryClient();
  const state = useQuery({
    queryKey: ['file-check'],
    queryFn: ({ signal }) => api<FileCheck>('file-check', { signal }),
    refetchInterval: (query) => (query.state.data?.status === 'running' ? 1000 : false),
  });
  const start = useMutation({
    mutationFn: () => api('file-check', { body: { confirm: '/api/file-check' } }),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ['file-check'] });
    },
  });
  const labels: Record<string, string> = {
    idle: '尚未检查',
    running: '正在计算',
    done: '检查完成',
    failed: '检查未完成',
  };
  return (
    <Card
      title="文件内容检查"
      actions={
        <button
          disabled={start.isPending || state.data?.status === 'running'}
          onClick={() => start.mutate()}
        >
          检查当前文件
        </button>
      }
    >
      <p>
        {labels[state.data?.status || 'idle']}
        {state.data?.checkedAt && (
          <span className="muted"> · {new Date(state.data.checkedAt * 1000).toLocaleString()}</span>
        )}
      </p>
      {state.data?.files?.length ? (
        <Table head={['对象 / 文件', '文件身份', '内容与记录', 'SHA-256']}>
          {state.data.files.map((row, index) => (
            <tr key={index}>
              <td>
                <strong>{row.name === 'panel' ? 'Panel' : 'daemon'}</strong>
                <br />
                <span className="mono break-text">
                  {row.path ||
                    (row.status === 'invalid_record' ? '受管记录无法读取' : '文件路径无法读取')}
                </span>
              </td>
              <td>
                {row.sameFile === true ? '一致' : row.sameFile === false ? '已变化' : '无比较基线'}
              </td>
              <td>
                {row.matchesRecorded === true
                  ? '一致'
                  : row.matchesRecorded === false
                    ? '已变化'
                    : '无比较基线'}
              </td>
              <td>
                <span className="mono break-text">
                  {row.sha256 ||
                    (row.changedDuringCheck ? '检查期间文件发生变化，请重试' : '无法读取')}
                </span>
              </td>
            </tr>
          ))}
        </Table>
      ) : (
        <p className="fine muted">读取当前文件的 SHA-256，并与受管记录比较。</p>
      )}
      <ErrorNotice error={state.error || start.error} />
    </Card>
  );
}
