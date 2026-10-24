import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../lib/api';
import { errors } from '../lib/messages';
import { useIdentity } from '../context/session';
import { useDirtyGuard } from '../context/drafts';
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
interface NetworkState {
  gw_alive: boolean;
  gw_port: number;
  cfg: { mode: string; token_policy: string; sink_post_prefixes: string[] };
  configRevision: string;
  active: {
    managed: boolean;
    healthy: boolean;
    mode?: string;
    matchesSaved?: boolean;
    error?: string;
    canStart?: boolean;
    recoveryRequired?: boolean;
  };
  fw: {
    supported: boolean;
    available: boolean;
    active_rules: number;
    error?: string;
    targets?: string[];
    requiredDirect?: string[];
    revision?: string;
  };
  total: number;
  up_bytes: number;
  logWindow: {
    available: boolean;
    bytesRead: number;
    maxBytes: number;
    maxLines: number;
    sources: number;
    truncated: boolean;
  };
  uplink: { path: string; n: number; bytes: number; last_status: number }[];
  poll: [string, number][];
  residual: [string, number][];
  egressAvailable: boolean;
  canConfigure: boolean;
}
const modes: Record<string, string> = {
  observe: '观察',
  enforce: '拦截匹配路径',
  strict: '严格规则',
  unconfigured: '未配置',
};
const tracking = '/algo/api/v1/tracking';
export function Network() {
  const identity = useIdentity();
  const state = useQuery({
    queryKey: ['network'],
    queryFn: ({ signal }) => api<NetworkState>('net/state', { signal }),
    enabled: identity.level === 'admin',
  });
  if (identity.level !== 'admin') return <Empty>此功能仅管理员可用。</Empty>;
  if (state.isPending) return <Loading />;
  if (state.error) return <ErrorNotice error={state.error} retry={() => void state.refetch()} />;
  const data = state.data!;
  return (
    <>
      <div className="grid">
        <div className="kpi">
          <span>网关状态</span>
          <strong>
            {data.active.healthy ? '运行正常' : data.active.managed ? '待核实' : '未登记实例'}
          </strong>
          <p className="fine muted">回环端口 {data.gw_port}</p>
        </div>
        <div className="kpi">
          <span>运行策略</span>
          <strong>
            {data.active.healthy ? modes[data.active.mode || ''] || '未读取' : '未读取'}
          </strong>
          <p className="fine muted">保存值：{modes[data.cfg.mode] || '未读取'}</p>
        </div>
        <div className="kpi">
          <span>观察窗口</span>
          <strong>{data.total.toLocaleString()} 条请求</strong>
          <p className="fine muted">请求体 {(data.up_bytes / 1024).toFixed(1)} KiB，含被拦截请求</p>
        </div>
      </div>
      {data.active.error && (
        <div className="notice">
          {errors[data.active.error] || '无法核实网关状态，请在宿主机检查运行记录。'}
        </div>
      )}
      {data.active.recoveryRequired && (
        <div className="notice">上次网关切换尚有未完成的事务，请在宿主机核对并恢复。</div>
      )}
      <NetworkEditor state={data} />
      <FirewallControl state={data.fw} />
      <Card title="写请求观察">
        <p className="fine muted">
          统计流经此网关的日志，读取尾部最多 {(data.logWindow.maxBytes / 1024 / 1024).toFixed(0)}{' '}
          MiB、{data.logWindow.maxLines.toLocaleString()} 行。
        </p>
        {!data.logWindow.available && (
          <div className="notice">部分日志无法读取，当前统计可能不完整。</div>
        )}
        {data.uplink.length ? (
          <Table head={['路径分组', '次数', '请求体量', '末次响应']}>
            {data.uplink.map((row) => (
              <tr key={row.path}>
                <td className="mono break-text">{row.path}</td>
                <td>{row.n}</td>
                <td>{(row.bytes / 1024).toFixed(1)} KiB</td>
                <td>{row.last_status}</td>
              </tr>
            ))}
          </Table>
        ) : (
          <Empty>
            {data.logWindow.sources ? '本次日志窗口内没有写请求。' : '尚无网关请求日志。'}
          </Empty>
        )}
      </Card>
      <Card title="拉取请求">
        {data.poll.length ? (
          <Table head={['GET / HEAD 路径分组', '次数']}>
            {data.poll.map(([path, n]) => (
              <tr key={path}>
                <td className="mono break-text">{path}</td>
                <td>{n}</td>
              </tr>
            ))}
          </Table>
        ) : (
          <Empty>本次日志窗口内没有拉取请求。</Empty>
        )}
      </Card>
      <Card title="daemon 当前连接">
        {!data.egressAvailable ? (
          <Empty>尚未核实 daemon 的受管身份，无法按进程统计连接。</Empty>
        ) : data.residual.length ? (
          <Table head={['目标 IP', '连接数']}>
            {data.residual.map(([ip, n]) => (
              <tr key={ip}>
                <td className="mono">{ip}</td>
                <td>{n}</td>
              </tr>
            ))}
          </Table>
        ) : (
          <Empty>本次快照未观测到外部 TCP 连接。</Empty>
        )}
      </Card>
      <Card title="QCS 兼容">
        <Link to="/controls">前往实例维护，查看补丁状态与还原入口</Link>
      </Card>
    </>
  );
}
function NetworkEditor({ state }: { state: NetworkState }) {
  const initial = {
    mode: state.cfg.mode === 'unconfigured' ? 'observe' : state.cfg.mode,
    sinks: state.cfg.sink_post_prefixes,
    revision: state.configRevision,
  };
  const [form, setForm] = useState(initial);
  const [saved, setSaved] = useState(initial);
  const dirty = JSON.stringify(form) !== JSON.stringify(saved);
  const signature = JSON.stringify(initial);
  useDirtyGuard(dirty);
  useEffect(() => {
    if (!dirty) {
      const next: typeof initial = JSON.parse(signature);
      setForm(next);
      setSaved(next);
    }
  }, [signature]);
  const client = useQueryClient();
  const confirm = useConfirm();
  const toast = useToast();
  const mutation = useMutation({
    mutationFn: async () => {
      if (
        !(await confirm(
          `应用“${modes[form.mode]}”策略？切换会重启网关，现有网关连接可能中断。`,
          '应用网关策略',
        ))
      )
        return;
      const result = await api<{ configHash: string }>('net/config', {
        body: {
          mode: form.mode,
          token_policy: 'balanced',
          sink_post_prefixes: form.sinks,
          baseRevision: form.revision,
          confirm: '/api/net/config',
        },
      });
      const next = { ...form, revision: result.configHash };
      setForm(next);
      setSaved(next);
      toast('网关策略已应用');
    },
    onSettled: async () => {
      await Promise.all(
        ['network', 'maintenance', 'state'].map((key) =>
          client.invalidateQueries({ queryKey: [key] }),
        ),
      );
    },
  });
  const strict = state.cfg.mode === 'strict';
  const allowed =
    state.canConfigure &&
    (state.active.healthy || state.active.canStart === true) &&
    !strict &&
    !state.active.recoveryRequired;
  return (
    <Card title="网关策略">
      <form
        onSubmit={(event) => {
          event.preventDefault();
          mutation.mutate();
        }}
      >
        <fieldset className="runtime-fields" disabled={mutation.isPending || strict}>
          <Field label="网关模式">
            <select
              value={form.mode}
              onChange={(event) => setForm({ ...form, mode: event.target.value })}
            >
              <option value="observe">观察（转发请求）</option>
              <option value="enforce">拦截匹配路径</option>
              {strict && <option value="strict">严格规则</option>}
            </select>
          </Field>
          <label className="check-row">
            <input
              type="checkbox"
              checked={form.sinks.includes(tracking)}
              onChange={(event) =>
                setForm({
                  ...form,
                  sinks: event.target.checked
                    ? [...form.sinks.filter((path) => path !== tracking), tracking]
                    : form.sinks.filter((path) => path !== tracking),
                })
              }
            />
            拦截 tracking 路径
          </label>
          <details>
            <summary>已选路径</summary>
            {form.sinks.length ? (
              <ul>
                {form.sinks.map((path) => (
                  <li className="mono break-text" key={path}>
                    {path}
                  </li>
                ))}
              </ul>
            ) : (
              <p>未选择拦截路径。</p>
            )}
          </details>
          <div className="form-actions">
            <button className="primary" disabled={!allowed}>
              {mutation.isPending ? '正在切换…' : state.active.managed ? '应用策略' : '启动并应用'}
            </button>
          </div>
        </fieldset>
        {strict ? (
          <p className="fine muted">当前严格规则请在宿主机维护。</p>
        ) : !allowed ? (
          <p className="fine muted">网关身份或端口尚未核实，请先检查宿主机上的运行实例。</p>
        ) : (
          state.active.matchesSaved && <p className="fine muted">运行策略与保存配置一致。</p>
        )}
        {form.revision !== state.configRevision && dirty && (
          <div className="notice">
            规则已在其他位置更新，请重新载入后再编辑。
            <button
              type="button"
              onClick={async () => {
                if (await confirm('放弃当前草稿并载入最新规则？')) {
                  setForm(initial);
                  setSaved(initial);
                }
              }}
            >
              重新载入
            </button>
          </div>
        )}
        <ErrorNotice error={mutation.error} />
      </form>
    </Card>
  );
}
function FirewallControl({ state }: { state: NetworkState['fw'] }) {
  const confirm = useConfirm();
  const client = useQueryClient();
  const toast = useToast();
  const mutation = useMutation({
    mutationFn: async (action: 'add' | 'remove') => {
      const message =
        action === 'add'
          ? '应用 lite 防火墙规则？将限制配置中的控制面地址并阻断 HTTPDNS，匹配规则的连接可能中断。'
          : '停用本项目的防火墙规则？此前受限制的连接将恢复。';
      if (!(await confirm(message, action === 'add' ? '应用防火墙规则' : '停用防火墙规则'))) return;
      await api('net/firewall', {
        body: { action, baseRevision: state.revision, confirm: '/api/net/firewall' },
      });
      toast('防火墙操作已完成');
    },
    onSettled: async () => {
      await client.invalidateQueries({ queryKey: ['network'] });
    },
  });
  return (
    <Card title="防火墙">
      {!state.supported ? (
        <Empty>此防火墙功能适用于 Linux。</Empty>
      ) : (
        <>
          <p>已检测规则：{state.active_rules < 0 ? '未读取' : state.active_rules}</p>
          {state.error && (
            <div className="notice">
              {errors[state.error] || '无法读取防火墙，请在宿主机检查权限。'}
            </div>
          )}
          {state.targets?.length ? (
            <details>
              <summary>控制面地址</summary>
              <ul>
                {state.targets.map((ip) => (
                  <li key={ip} className="mono">
                    {ip}
                    {state.requiredDirect?.includes(ip) ? ' · 保留直连' : ''}
                  </li>
                ))}
              </ul>
            </details>
          ) : (
            <p className="fine muted">请先在宿主机配置控制面地址清单。</p>
          )}
          <div className="form-actions">
            <button
              disabled={
                mutation.isPending || !state.available || !!state.error || !state.targets?.length
              }
              onClick={() => mutation.mutate('add')}
            >
              应用 lite 规则
            </button>
            <button
              disabled={
                mutation.isPending || !state.available || !!state.error || !state.targets?.length
              }
              onClick={() => mutation.mutate('remove')}
            >
              停用规则
            </button>
          </div>
        </>
      )}
      <ErrorNotice error={mutation.error} />
    </Card>
  );
}
