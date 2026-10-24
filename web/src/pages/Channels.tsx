import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { api, query } from '../lib/api';
import { useIdentity } from '../context/session';
import { useDirtyGuard, useDraftState } from '../context/drafts';
import { usePanelState, activityStatus, WakerPicker } from '../components/resources';
import type { PanelState } from '../lib/types';
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
interface Channel {
  id?: string;
  channelId?: string;
  name?: string;
  botName?: string;
  type: string;
  status?: string;
  enabled: boolean;
  appId?: string;
  agentId?: string;
  bindingTarget?: string | { targetId: string };
  accessPolicy: 'paired' | 'open';
  artifactDeliveryEnabled?: boolean;
}
const channelID = (row: Channel) => row.channelId || row.id || '';
function targetID(row: Channel) {
  return (
    row.agentId ||
    (typeof row.bindingTarget === 'string' ? row.bindingTarget : row.bindingTarget?.targetId) ||
    ''
  );
}
export function Channels() {
  const identity = useIdentity();
  const state = usePanelState();
  const client = useQueryClient();
  const confirm = useConfirm();
  const toast = useToast();
  const { dirty } = useDraftState();
  const [selected, setSelected] = useState<string | null>(null);
  const channels = useQuery({
    queryKey: ['channels'],
    queryFn: ({ signal }) => api<Channel[]>('channels', { signal }),
    enabled: identity.level === 'admin',
  });
  const action = useMutation({
    mutationFn: async ({
      row,
      action,
    }: {
      row: Channel;
      action: 'start' | 'stop' | 'restart' | 'delete';
    }) => {
      const label = { start: '启动', stop: '停止', restart: '重启', delete: '删除' }[action];
      const name = row.botName || row.name || row.type;
      const effect =
        action === 'stop'
          ? '此通道将停止接收消息。'
          : action === 'delete'
            ? '删除通道配置，平台应用保留。'
            : '平台消息可能触发模型调用并产生费用。';
      if (!(await confirm(label + ' ' + name + '？' + effect))) return;
      const path = action === 'delete' ? 'channels/delete' : 'channels/action';
      await api(path, { body: { id: channelID(row), action, confirm: '/api/' + path } });
      toast(label + '操作已完成');
      await client.invalidateQueries({ queryKey: ['channels'] });
      await client.invalidateQueries({ queryKey: ['audit'] });
      if (action === 'delete' && selected === channelID(row)) setSelected(null);
    },
  });
  async function edit(id: string) {
    if (dirty && !(await confirm('放弃当前通道草稿并切换？'))) return;
    setSelected(id);
  }
  if (identity.level !== 'admin') return <Empty>此功能仅管理员可用。</Empty>;
  if (state.isPending) return <Loading />;
  if (state.error) return <ErrorNotice error={state.error} />;
  return (
    <>
      <Card
        title="消息通道"
        actions={
          <button className="primary" onClick={() => void edit('')}>
            新增飞书通道
          </button>
        }
      >
        {channels.isPending ? (
          <Loading />
        ) : channels.error ? (
          <ErrorNotice error={channels.error} retry={() => void channels.refetch()} />
        ) : channels.data?.length ? (
          <Table head={['名称 / 平台', '状态', '访问策略 / 目标', '操作']}>
            {channels.data.map((row) => (
              <tr key={channelID(row)}>
                <td>
                  <strong>{row.name || row.botName || row.type}</strong>
                  <p className="fine muted">{row.type === 'feishu' ? '飞书' : row.type}</p>
                </td>
                <td>
                  {row.status ? activityStatus(row.status) : row.enabled ? '已启用' : '已停用'}
                </td>
                <td>
                  {row.accessPolicy === 'open' ? '开放到指定机器人' : '配对后访问'}
                  <p className="muted">
                    {state.data?.wakers.find((w) => w.id === targetID(row))?.name ||
                      targetID(row) ||
                      '配对时决定'}
                  </p>
                </td>
                <td className="actions">
                  {row.type === 'feishu' ? (
                    <button onClick={() => void edit(channelID(row))}>编辑</button>
                  ) : (
                    <Link to="/native">官方操作</Link>
                  )}
                  {(['start', 'stop', 'restart', 'delete'] as const).map((kind) => (
                    <button
                      key={kind}
                      className={kind === 'delete' ? 'danger' : ''}
                      disabled={action.isPending}
                      onClick={() => action.mutate({ row, action: kind })}
                    >
                      {{ start: '启动', stop: '停止', restart: '重启', delete: '删除' }[kind]}
                    </button>
                  ))}
                </td>
              </tr>
            ))}
          </Table>
        ) : (
          <Empty>尚未配置通道。</Empty>
        )}
        <ErrorNotice error={action.error} />
      </Card>
      {selected !== null && state.data && (
        <ChannelLoader
          key={selected}
          id={selected}
          state={state.data}
          close={() => setSelected(null)}
        />
      )}
    </>
  );
}
function ChannelLoader({ id, state, close }: { id: string; state: PanelState; close: () => void }) {
  const data = useQuery({
    queryKey: ['channel', id],
    queryFn: ({ signal }) => api<Channel>('channels/detail?' + query({ id }), { signal }),
    enabled: !!id,
  });
  if (id && data.isPending) return <Loading />;
  if (data.error) return <ErrorNotice error={data.error} retry={() => void data.refetch()} />;
  return <ChannelEditor id={id} initial={data.data} state={state} close={close} />;
}
function ChannelEditor({
  id,
  initial,
  state,
  close,
}: {
  id: string;
  initial?: Channel;
  state: PanelState;
  close: () => void;
}) {
  const client = useQueryClient();
  const confirm = useConfirm();
  const toast = useToast();
  const baseline = {
    botName: initial?.botName || initial?.name || '',
    appId: initial?.appId || '',
    appSecret: '',
    accessPolicy: initial?.accessPolicy || 'paired',
    bindingTarget: initial ? targetID(initial) : '',
    artifactDeliveryEnabled: !!initial?.artifactDeliveryEnabled,
  };
  const [draft, setDraft] = useState(baseline);
  const dirty = JSON.stringify(draft) !== JSON.stringify(baseline);
  useDirtyGuard(dirty);
  const save = useMutation({
    mutationFn: async () => {
      if (
        !(await confirm(
          initial?.enabled
            ? '保存通道配置？当前通道已启用，官方服务可能立即重启连接。'
            : '保存飞书通道配置？保存后可单独启动连接。',
        ))
      )
        return;
      await api('channels/config', {
        body: {
          ...draft,
          id,
          type: 'feishu',
          enabled: initial?.enabled === true,
          confirm: '/api/channels/config',
        },
      });
      toast('通道配置已保存');
      setDraft((v) => ({ ...v, appSecret: '' }));
      close();
      await client.invalidateQueries({ queryKey: ['channels'] });
      await client.invalidateQueries({ queryKey: ['channel', id] });
    },
  });
  return (
    <Card title={id ? '编辑飞书通道' : '新增飞书通道'}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        <fieldset disabled={save.isPending}>
          <div className="form-grid">
            <Field label="机器人显示名称">
              <input
                value={draft.botName}
                maxLength={100}
                onChange={(e) => setDraft({ ...draft, botName: e.target.value })}
              />
            </Field>
            <Field label="App ID">
              <input
                value={draft.appId}
                required
                maxLength={300}
                onChange={(e) => setDraft({ ...draft, appId: e.target.value })}
              />
            </Field>
            <Field label="App Secret" hint={id ? '留空沿用已保存的凭据。' : undefined}>
              <input
                type="password"
                value={draft.appSecret}
                required={!id}
                maxLength={2000}
                autoComplete="off"
                onChange={(e) => setDraft({ ...draft, appSecret: e.target.value })}
              />
            </Field>
            <Field label="访问策略">
              <select
                value={draft.accessPolicy}
                onChange={(e) =>
                  setDraft({ ...draft, accessPolicy: e.target.value as 'paired' | 'open' })
                }
              >
                <option value="paired">配对后访问</option>
                <option value="open">开放到指定机器人</option>
              </select>
            </Field>
            {draft.accessPolicy === 'open' && (
              <WakerPicker
                label="绑定机器人"
                items={state.wakers}
                value={draft.bindingTarget}
                onChange={(value) => setDraft({ ...draft, bindingTarget: value })}
              />
            )}
          </div>
          <label className="check-row">
            <input
              type="checkbox"
              checked={draft.artifactDeliveryEnabled}
              onChange={(e) => setDraft({ ...draft, artifactDeliveryEnabled: e.target.checked })}
            />
            <span>允许发送生成的文件</span>
          </label>
          <p className="fine muted">请在飞书开放平台完成应用权限和事件订阅配置。</p>
          <div className="form-actions">
            <button
              type="button"
              onClick={async () => {
                if (!dirty || (await confirm('放弃尚未保存的通道配置？'))) close();
              }}
            >
              取消
            </button>
            <button
              className="primary"
              disabled={!dirty || (draft.accessPolicy === 'open' && !draft.bindingTarget)}
            >
              保存配置
            </button>
          </div>
        </fieldset>
      </form>
      <ErrorNotice error={save.error} />
    </Card>
  );
}
