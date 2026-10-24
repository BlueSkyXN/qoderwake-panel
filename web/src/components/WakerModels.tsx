import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, APIError, query } from '../lib/api';
import type { PanelState } from '../lib/types';
import type { Preferences, SaveResult, WakerModels as Binding } from '../lib/model-settings';
import { preferencePayload, saveMessages } from '../lib/model-settings';
import { useIdentity } from '../context/session';
import { useDirtyGuard, useDraftState } from '../context/drafts';
import { Card, Empty, ErrorNotice, Field, Loading, Table, useConfirm, useToast } from './ui';

export function WakerModelBinding({ state, waker }: { state: PanelState; waker?: string }) {
  const [selected, setSelected] = useState(waker || state.wakers[0]?.id || '');
  const { dirty } = useDraftState();
  const confirm = useConfirm();
  const active = waker || selected;
  return (
    <Card title="Waker 模型绑定">
      {!waker && (
        <Field label="机器人">
          <select
            value={selected}
            onChange={async (event) => {
              const id = event.target.value;
              if (!dirty || (await confirm('放弃未保存的修改并切换 Waker？'))) setSelected(id);
            }}
          >
            <option value="">选择机器人</option>
            {state.wakers.map((w) => (
              <option key={w.id} value={w.id}>
                {w.name}
              </option>
            ))}
          </select>
        </Field>
      )}
      {active ? (
        <BindingLoader key={active} id={active} state={state} />
      ) : (
        <Empty>选择 Waker 查看模型偏好和会话模型。</Empty>
      )}
    </Card>
  );
}
function BindingLoader({ id, state }: { id: string; state: PanelState }) {
  const data = useQuery({
    queryKey: ['waker-models', id],
    queryFn: ({ signal }) => api<Binding>('waker/models?' + query({ waker: id }), { signal }),
    staleTime: 0,
  });
  if (data.isPending) return <Loading />;
  if (data.error) return <ErrorNotice error={data.error} retry={() => void data.refetch()} />;
  return <BindingEditor id={id} state={state} initial={data.data!} />;
}
function BindingEditor({
  id,
  state,
  initial,
}: {
  id: string;
  state: PanelState;
  initial: Binding;
}) {
  const identity = useIdentity();
  const editable = identity.level === 'admin';
  const [data, setData] = useState(initial);
  const [scope, setScope] = useState('');
  const [project, setProject] = useState('');
  const [model, setModel] = useState('');
  const [effort, setEffort] = useState('');
  const [context, setContext] = useState('');
  const [blocked, setBlocked] = useState(false);
  const [status, setStatus] = useState('');
  const [readError, setReadError] = useState<unknown>(null);
  const dirty = !!(model || effort || context);
  useDirtyGuard(dirty);
  const confirm = useConfirm();
  const toast = useToast();
  const client = useQueryClient();
  const prefs = data.preferences;
  const target = scope === 'new-project' ? project.trim() : scope;
  const current = target ? prefs.byProject[target] : prefs.noProject;
  function clearDraft() {
    setModel('');
    setEffort('');
    setContext('');
  }
  const save = useMutation({
    mutationFn: () =>
      api<SaveResult>('waker/models/update', {
        body: preferencePayload(
          id,
          target,
          model || current?.modelId || 'auto',
          prefs.revision,
          effort,
          context,
        ),
      }),
    onSuccess: async (result) => {
      setStatus(saveMessages[result.status]);
      if (result.status === 'verified' && result.preferences) {
        setData((old) => ({ ...old, preferences: result.preferences as Preferences }));
        clearDraft();
        setBlocked(false);
        toast('模型偏好已保存并回读');
        // Session metadata is a separate read; preferences do not establish a session switch.
        try {
          setData(await api<Binding>('waker/models?' + query({ waker: id })));
          setReadError(null);
        } catch (error) {
          setReadError(error);
        }
      } else setBlocked(true);
      await client.invalidateQueries({ queryKey: ['state'] });
      await client.invalidateQueries({ queryKey: ['audit'] });
    },
    onError: (error) => {
      if (error instanceof APIError && error.code === 'version_conflict') setBlocked(true);
    },
  });
  async function reload() {
    if (dirty && !(await confirm('放弃当前草稿，读取最新模型偏好和会话状态？'))) return;
    try {
      setData(await api<Binding>('waker/models?' + query({ waker: id })));
      clearDraft();
      setBlocked(false);
      setStatus('已读取最新状态。');
      setReadError(null);
      save.reset();
    } catch (error) {
      setReadError(error);
    }
  }
  const options = [...state.models];
  if (!options.some((m) => m.id === 'auto')) options.unshift({ id: 'auto', name: '自动选择' });
  if (current?.modelId && !options.some((m) => m.id === current.modelId))
    options.push({ id: current.modelId, name: current.modelId });
  const statuses: Record<string, string> = {
    stopped: '已停止',
    error: '失败',
    running: '运行中',
    idle: '空闲',
    completed: '已完成',
    active: '活动',
  };
  return (
    <>
      <Table head={['偏好范围', '已保存模型', '推理强度 / 上下文']}>
        {[['', prefs.noProject] as const, ...Object.entries(prefs.byProject)].map(
          ([key, entry]) => (
            <tr key={key}>
              <td>{key ? '项目：' + key : '无项目默认'}</td>
              <td className="mono">{entry.modelId || '未设置'}</td>
              <td>
                {entry.reasoningEffort || '—'} / {entry.contextWindow?.toLocaleString() || '—'}
              </td>
            </tr>
          ),
        )}
      </Table>
      {editable && (
        <form
          onSubmit={async (event) => {
            event.preventDefault();
            if (await confirm('保存此范围的模型偏好？已有会话的实际模型请在下方核对。'))
              save.mutate();
          }}
        >
          <fieldset disabled={save.isPending}>
            <div className="form-grid">
              <Field label="修改范围">
                <select
                  value={scope}
                  onChange={async (event) => {
                    const next = event.target.value;
                    if (!dirty || (await confirm('放弃草稿并切换偏好范围？'))) {
                      setScope(next);
                      clearDraft();
                    }
                  }}
                >
                  <option value="">无项目默认</option>
                  {Object.keys(prefs.byProject).map((key) => (
                    <option key={key} value={key}>
                      {key}
                    </option>
                  ))}
                  <option value="new-project">指定项目</option>
                </select>
              </Field>
              {scope === 'new-project' && (
                <Field label="项目 ID">
                  <input
                    required
                    maxLength={256}
                    value={project}
                    onChange={(e) => {
                      setProject(e.target.value);
                      clearDraft();
                    }}
                  />
                </Field>
              )}
              <Field label="模型">
                <select value={model} onChange={(e) => setModel(e.target.value)}>
                  <option value="">当前：{current?.modelId || '未设置'}</option>
                  {options.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.name || m.id} · {m.id}
                    </option>
                  ))}
                </select>
              </Field>
              <Field label="推理强度" hint="留空保留当前参数；可用值由模型提供方决定。">
                <input
                  value={effort}
                  maxLength={40}
                  placeholder={current?.reasoningEffort || '未设置'}
                  onChange={(e) => setEffort(e.target.value)}
                />
              </Field>
              <Field label="上下文窗口" hint="留空保留当前参数。">
                <input
                  type="number"
                  min={1}
                  step={1}
                  value={context}
                  placeholder={String(current?.contextWindow || '')}
                  onChange={(e) => setContext(e.target.value)}
                />
              </Field>
            </div>
            <div className="form-actions">
              <button
                className="primary"
                disabled={!dirty || blocked || (scope === 'new-project' && !project.trim())}
              >
                保存偏好
              </button>
            </div>
          </fieldset>
        </form>
      )}
      <button disabled={save.isPending} onClick={() => void reload()}>
        读取最新模型状态
      </button>
      {status && <p role="status">{status}</p>}
      <ErrorNotice error={save.error || readError} />
      {blocked && <p>请先读取最新状态，再继续修改。</p>}
      <h3>会话实际模型</h3>
      <p className="muted">
        以下为 Wake 记录的会话状态。保存偏好后，已有会话是否切换取决于运行时；“需要重新启动”由 Wake
        返回。
      </p>
      {data.sessionsError ? (
        <ErrorNotice error={new APIError(data.sessionsError, 502)} />
      ) : data.sessions.length ? (
        <Table head={['会话 / 最近活动', '模型', '运行参数', '状态']}>
          {data.sessions.map((session) => (
            <tr key={session.sessionId}>
              <td className="mono break-text">
                {session.sessionId}
                <br />
                <small>{session.lastActiveAt || '—'}</small>
              </td>
              <td className="mono">{session.currentModel || '未记录'}</td>
              <td>
                {session.runtimeConfig?.reasoningEffort || '—'} /{' '}
                {session.runtimeConfig?.contextWindow?.toLocaleString() || '—'}
                <br />
                <small>配置版本 {session.modelConfigRevision ?? '未记录'}</small>
              </td>
              <td>
                {statuses[session.status] || session.status}
                <br />
                {session.runtimeRestartRequired === true
                  ? '需要重新启动'
                  : session.runtimeRestartRequired === false
                    ? '无需重新启动'
                    : '重启状态未提供'}
              </td>
            </tr>
          ))}
        </Table>
      ) : (
        <Empty>暂无本机会话记录。</Empty>
      )}
    </>
  );
}
