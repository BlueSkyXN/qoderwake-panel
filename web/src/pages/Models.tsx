import { NativeBYOK, ProviderJSONLoader } from '../components/ModelSettings';
import { WakerModelBinding } from '../components/WakerModels';
import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, APIError } from '../lib/api';
import type { PanelState, Provider } from '../lib/types';
import { useIdentity } from '../context/session';
import { useDirtyGuard, useDraftState } from '../context/drafts';
import { useDeletion } from '../components/Deletion';
import { ApplyRuntimeButton } from '../components/Runtime';
import {
  Card,
  Empty,
  ErrorNotice,
  Field,
  JSONDetails,
  Loading,
  Table,
  useConfirm,
  useToast,
} from '../components/ui';

export function Models() {
  const identity = useIdentity();
  const confirm = useConfirm();
  const { dirty } = useDraftState();
  const remove = useDeletion();
  const [editor, setEditor] = useState<{ item?: Provider; revision: string } | null>(null);
  const [search, setSearch] = useState('');
  const state = useQuery({
    queryKey: ['state'],
    queryFn: ({ signal }) => api<PanelState>('state', { signal }),
  });
  if (!['admin', 'viewer'].includes(identity.level))
    return <Empty>当前账号没有此页面的访问权限。</Empty>;
  if (state.isPending) return <Loading />;
  if (state.error) return <ErrorNotice error={state.error} retry={() => void state.refetch()} />;
  const data = state.data!;
  const visibleModels = data.models.filter((m) =>
    [m.id, m.name, m.provider].join(' ').toLowerCase().includes(search.toLowerCase()),
  );
  async function edit(item?: Provider) {
    if (dirty && !(await confirm('当前 Provider 有未保存的修改，放弃并切换？'))) return;
    if (data.providerRevision) setEditor({ item, revision: data.providerRevision });
  }
  return (
    <>
      {identity.level === 'admin' && (
        <>
          <Card
            title="明文 BYOK · Provider"
            actions={
              <button
                className="primary"
                disabled={!data.providerRevision}
                onClick={() => void edit()}
              >
                新增 Provider
              </button>
            }
          >
            {data.providers.length ? (
              <Table head={['名称 / 地址', '模型', '凭据', '操作']}>
                {data.providers.map((item) => (
                  <tr key={item.name}>
                    <td>
                      <strong>{item.name}</strong>
                      <br />
                      <span className="mono muted break-text">{item.baseUrl}</span>
                    </td>
                    <td>{item.models.join('、') || '—'}</td>
                    <td>{item.keyConfigured ? '已配置' : '未配置'}</td>
                    <td className="actions">
                      <button onClick={() => void edit(item)}>编辑 JSON</button>
                      {item.panelManaged && (
                        <>
                          <ProbeButton
                            data={{
                              mode: 'saved',
                              name: item.name,
                              baseRevision: data.providerRevision,
                            }}
                          />
                          <button
                            className="danger"
                            onClick={async () => {
                              if (
                                !dirty ||
                                (await confirm('有未保存的 Provider 修改，放弃后检查删除？'))
                              ) {
                                setEditor(null);
                                remove({
                                  kind: 'provider',
                                  target: item.name,
                                  baseRevision: data.providerRevision || undefined,
                                });
                              }
                            }}
                          >
                            删除
                          </button>
                        </>
                      )}
                    </td>
                  </tr>
                ))}
              </Table>
            ) : (
              <Empty>尚未配置 Provider，可添加兼容 OpenAI 的模型服务。</Empty>
            )}
          </Card>
          {editor &&
            (editor.item ? (
              <ProviderJSONLoader
                key={editor.item.name}
                name={editor.item.name}
                close={() => setEditor(null)}
              />
            ) : (
              <ProviderEditor key="new" revision={editor.revision} close={() => setEditor(null)} />
            ))}
          <NativeBYOK />
          <DefaultModel state={data} />
          <Card title="应用已保存的配置">
            <ApplyRuntimeButton />
          </Card>
        </>
      )}
      <Card title="可用模型">
        <Field label="查找模型">
          <input
            type="search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="名称或模型 ID"
          />
        </Field>
        <Table head={['模型', '模型 ID', 'Provider']}>
          {visibleModels.map((m) => (
            <tr key={m.id}>
              <td>{m.name || m.id}</td>
              <td className="mono">{m.id}</td>
              <td>{m.provider || '—'}</td>
            </tr>
          ))}
        </Table>
        {data.error ? (
          <ErrorNotice error={new APIError(data.error, 503)} retry={() => void state.refetch()} />
        ) : (
          !visibleModels.length && <Empty>{search ? '没有匹配的模型。' : '暂无可用模型。'}</Empty>
        )}
      </Card>
    </>
  );
}
type Draft = { name: string; baseUrl: string; model: string; display: string; apiKey: string };
function draftFor(item?: Provider): Draft {
  return {
    name: item?.name || '',
    baseUrl: item?.baseUrl || '',
    model: item?.models[0] || '',
    display: item?.displayNames[0] || '',
    apiKey: '',
  };
}
function ProviderEditor({
  item,
  revision,
  close,
}: {
  item?: Provider;
  revision: string;
  close: () => void;
}) {
  const confirm = useConfirm();
  const toast = useToast();
  const client = useQueryClient();
  const [baseline, setBaseline] = useState(() => draftFor(item));
  const [draft, setDraft] = useState(baseline);
  const [baseRevision, setBaseRevision] = useState(revision);
  const [conflict, setConflict] = useState<PanelState | null>(null);
  const dirty = JSON.stringify(draft) !== JSON.stringify(baseline);
  useDirtyGuard(dirty);
  const save = useMutation({
    mutationFn: () => api('provider', { body: { ...draft, baseRevision } }),
    onSuccess: async () => {
      setDraft((v) => ({ ...v, apiKey: '' }));
      toast('Provider 已保存');
      close();
      await client.invalidateQueries({ queryKey: ['state'] });
      await client.invalidateQueries({ queryKey: ['audit'] });
    },
    onError: async (error) => {
      if (error instanceof APIError && error.code === 'version_conflict') {
        try {
          setConflict(await api<PanelState>('state'));
        } catch {}
      }
    },
  });
  const update = (field: keyof Draft, value: string) =>
    setDraft((old) => ({ ...old, [field]: value }));
  return (
    <Card title={item ? '编辑 · ' + item.name : '新增 Provider'}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        <fieldset disabled={save.isPending}>
          <div className="form-grid">
            <Field label="Provider 标识">
              <input
                value={draft.name}
                readOnly={!!item}
                required
                pattern="[A-Za-z0-9_.\-]+"
                maxLength={120}
                onChange={(e) => update('name', e.target.value)}
              />
            </Field>
            <Field label="服务地址">
              <input
                type="url"
                value={draft.baseUrl}
                required
                maxLength={2048}
                placeholder="https://api.example.com/v1"
                onChange={(e) => update('baseUrl', e.target.value)}
              />
            </Field>
            <Field label="模型 ID">
              <input
                value={draft.model}
                required
                maxLength={200}
                onChange={(e) => update('model', e.target.value)}
              />
            </Field>
            <Field label="显示名称">
              <input
                value={draft.display}
                maxLength={200}
                onChange={(e) => update('display', e.target.value)}
              />
            </Field>
            <Field label="API Key" hint={item ? '留空沿用已保存的密钥。' : undefined}>
              <input
                type="password"
                value={draft.apiKey}
                required={!item}
                autoComplete="off"
                maxLength={2048}
                onChange={(e) => update('apiKey', e.target.value)}
              />
            </Field>
          </div>
          <div className="form-actions">
            <button
              type="button"
              onClick={async () => {
                if (!dirty || (await confirm('放弃尚未保存的 Provider 修改？'))) close();
              }}
            >
              取消
            </button>
            <ProbeButton
              data={{ mode: 'adhoc', baseUrl: draft.baseUrl, apiKey: draft.apiKey }}
              disabled={!draft.apiKey || !draft.baseUrl}
              label="测试当前填写的连接"
            />
            <button className="primary" disabled={!dirty}>
              保存 Provider
            </button>
          </div>
        </fieldset>
      </form>
      <ErrorNotice error={save.error} />
      {conflict && (
        <section className="notice">
          <h3>配置已变化</h3>
          <JSONDetails value={{ ...baseline, apiKey: undefined }} label="读取时的配置" />
          <JSONDetails
            value={conflict.providers.find((v) => v.name === draft.name) || null}
            label="当前配置"
          />
          <button
            onClick={async () => {
              if (await confirm('放弃当前草稿并读取最新配置？')) {
                const value = draftFor(conflict.providers.find((v) => v.name === draft.name));
                setDraft(value);
                setBaseline(value);
                setBaseRevision(conflict.providerRevision || '');
                setConflict(null);
                save.reset();
              }
            }}
          >
            读取最新配置
          </button>
        </section>
      )}
    </Card>
  );
}
function ProbeButton({
  data,
  disabled,
  label = '测试连接',
}: {
  data: Record<string, unknown>;
  disabled?: boolean;
  label?: string;
}) {
  const confirm = useConfirm();
  const toast = useToast();
  const test = useMutation({
    mutationFn: async () => {
      const payload = { ...data };
      const plan = await api<{ id: string; target: string }>('provider/probe-plan', {
        body: payload,
      });
      if (
        !(await confirm('将此连接的 API Key 发送到 ' + plan.target + ' 读取模型目录？', '测试连接'))
      )
        return;
      const path = payload.mode === 'saved' ? 'test/saved' : 'test';
      const result = await api<{ message: string }>(path, {
        body: { ...payload, plan: plan.id, confirm: '/api/' + path },
      });
      toast(result.message);
    },
  });
  return (
    <span className="inline-action">
      <button type="button" disabled={disabled || test.isPending} onClick={() => test.mutate()}>
        {test.isPending ? '正在测试…' : label}
      </button>
      <ErrorNotice error={test.error} />
    </span>
  );
}
export function DefaultModel({ state, waker }: { state: PanelState; waker?: string }) {
  return <WakerModelBinding state={state} waker={waker} />;
}
