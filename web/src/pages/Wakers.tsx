import { WakerModelBinding } from '../components/WakerModels';
import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { api, APIError, query } from '../lib/api';
import type { PanelState, WakerDetail } from '../lib/types';
import { can, useIdentity } from '../context/session';
import { useDirtyGuard, useDraftState } from '../context/drafts';
import { useDeletion } from '../components/Deletion';
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
export function Wakers() {
  const remove = useDeletion();
  const identity = useIdentity();
  const [selected, setSelected] = useState('');
  const confirm = useConfirm();
  const { dirty } = useDraftState();
  const data = useQuery({
    queryKey: ['state'],
    queryFn: ({ signal }) => api<PanelState>('state', { signal }),
  });
  if (data.isPending) return <Loading />;
  if (data.error) return <ErrorNotice error={data.error} retry={() => void data.refetch()} />;
  return (
    <>
      <Card title="机器人">
        {data.data?.wakers.length ? (
          <Table head={['名称', '默认模型', ...(can(identity, 'waker.read') ? ['操作'] : [])]}>
            {data.data.wakers.map((w) => (
              <tr key={w.id}>
                <td>
                  <strong>{w.name}</strong>
                  <br />
                  <span className="mono muted">{w.id}</span>
                </td>
                <td>{w.preference}</td>
                {can(identity, 'waker.read') && (
                  <td className="actions">
                    <button
                      onClick={async () => {
                        if (!dirty || (await confirm('当前配置有未保存的修改，放弃并切换？')))
                          setSelected(w.id);
                      }}
                    >
                      {can(identity, 'waker.write') ? '编辑' : '查看'}
                    </button>
                    <Link className="text-link" to={'/skills?waker=' + encodeURIComponent(w.id)}>
                      技能
                    </Link>
                    {identity.level === 'admin' && (
                      <button
                        className="danger"
                        onClick={async () => {
                          if (!dirty || (await confirm('有未保存的配置，放弃后检查删除？'))) {
                            setSelected('');
                            remove({ kind: 'waker', target: w.id });
                          }
                        }}
                      >
                        删除
                      </button>
                    )}
                  </td>
                )}
              </tr>
            ))}
          </Table>
        ) : (
          <Empty>
            {identity.level === 'admin' || identity.level === 'viewer'
              ? '尚未创建 Waker。'
              : '尚未分配 Waker，请联系管理员。'}
          </Empty>
        )}
      </Card>
      {selected && (
        <>
          <WakerLoader key={selected} id={selected} />
          <WakerModelBinding key={selected + '-models'} state={data.data!} waker={selected} />
        </>
      )}
      {identity.level === 'admin' && data.data && (
        <>
          <CreateWaker state={data.data} />
        </>
      )}
    </>
  );
}
function CreateWaker({ state }: { state: PanelState }) {
  const [name, setName] = useState('');
  const [model, setModel] = useState('');
  useDirtyGuard(!!name || !!model);
  const toast = useToast();
  const client = useQueryClient();
  const create = useMutation({
    mutationFn: () =>
      api<{ id: string; preferenceApplied?: boolean }>('waker', { body: { name, model } }),
    onSuccess: async (value) => {
      setName('');
      setModel('');
      toast(
        value.preferenceApplied === false
          ? '机器人已创建，默认模型未应用，请在默认模型设置中重试。'
          : '机器人已创建',
      );
      await client.invalidateQueries({ queryKey: ['state'] });
      await client.invalidateQueries({ queryKey: ['audit'] });
    },
  });
  return (
    <Card title="创建机器人">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <fieldset disabled={create.isPending}>
          <div className="form-grid">
            <Field label="名称">
              <input
                value={name}
                required
                maxLength={100}
                onChange={(e) => setName(e.target.value)}
              />
            </Field>
            <Field label="默认模型">
              <select value={model} onChange={(e) => setModel(e.target.value)}>
                <option value="">沿用默认</option>
                {state.models.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name || m.id}
                  </option>
                ))}
              </select>
            </Field>
          </div>
          <div className="form-actions">
            <button className="primary">创建</button>
          </div>
        </fieldset>
      </form>
      <ErrorNotice error={create.error} />
    </Card>
  );
}
function WakerLoader({ id }: { id: string }) {
  const data = useQuery({
    queryKey: ['waker', id],
    queryFn: ({ signal }) => api<WakerDetail>('waker/detail?' + query({ id }), { signal }),
  });
  if (data.isPending) return <Loading />;
  if (data.error) return <ErrorNotice error={data.error} retry={() => void data.refetch()} />;
  return <WakerEditor id={id} initial={data.data!} />;
}
function WakerEditor({ id, initial }: { id: string; initial: WakerDetail }) {
  const identity = useIdentity();
  const client = useQueryClient();
  const toast = useToast();
  const [baseline, setBaseline] = useState(initial);
  const [draft, setDraft] = useState(initial);
  const [conflict, setConflict] = useState<WakerDetail | null>(null);
  const editable = can(identity, 'waker.write');
  const dirty = editable && JSON.stringify(draft) !== JSON.stringify(baseline);
  useDirtyGuard(dirty);
  const save = useMutation({
    mutationFn: () =>
      api('waker/update', {
        body: {
          id,
          name: draft.name,
          description: draft.description,
          skills: draft.skills.map((s) => ({ skillId: s.skillId, enabled: s.enabled })),
          baseRevision: baseline.configRevision,
        },
      }),
    onSuccess: async () => {
      toast('配置已保存');
      try {
        const current = await api<WakerDetail>('waker/detail?' + query({ id }));
        setBaseline(current);
        setDraft(current);
        setConflict(null);
        await client.invalidateQueries({ queryKey: ['state'] });
        await client.invalidateQueries({ queryKey: ['audit'] });
      } catch {
        toast('保存已完成，请重新读取最新配置。');
      }
    },
    onError: async (error) => {
      if (error instanceof APIError && error.code === 'version_conflict') {
        try {
          setConflict(await api<WakerDetail>('waker/detail?' + query({ id })));
        } catch {}
      }
    },
  });
  return (
    <Card title={initial.name}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        <fieldset disabled={!editable || save.isPending}>
          <div className="form-grid">
            <Field label="名称">
              <input
                value={draft.name}
                onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                required
                maxLength={100}
              />
            </Field>
            <Field label="描述">
              <textarea
                value={draft.description || ''}
                onChange={(e) => setDraft({ ...draft, description: e.target.value })}
                maxLength={4000}
              />
            </Field>
          </div>
          <h3>技能开关</h3>
          {draft.skills.map((s, index) => (
            <label key={s.skillId} className="check-row">
              <input
                type="checkbox"
                checked={s.enabled}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    skills: draft.skills.map((v, i) =>
                      i === index ? { ...v, enabled: e.target.checked } : v,
                    ),
                  })
                }
              />
              <span>{s.name || s.skillId}</span>
            </label>
          ))}
          <div className="form-actions">
            <span className="muted">{editable ? (dirty ? '未保存修改' : '已保存') : '只读'}</span>
            <button className="primary" disabled={!dirty}>
              保存配置
            </button>
          </div>
        </fieldset>
      </form>
      <ErrorNotice error={save.error} />
      {conflict && (
        <section className="notice">
          <h3>当前版本已变化</h3>
          <JSONDetails value={baseline} label="读取时的配置" />
          <JSONDetails value={draft} label="你的修改" />
          <JSONDetails value={conflict} label="当前配置" />
          <button
            onClick={() => {
              setBaseline(conflict);
              setDraft(conflict);
              setConflict(null);
            }}
          >
            读取当前版本
          </button>
        </section>
      )}
    </Card>
  );
}
