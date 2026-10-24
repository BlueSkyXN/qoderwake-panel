import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useSearchParams } from 'react-router-dom';
import { api, APIError, query } from '../lib/api';
import type { PanelState, Skill, SkillContent, SkillVersion } from '../lib/types';
import { can, useIdentity } from '../context/session';
import { useDirtyGuard, useDraftState } from '../context/drafts';
import {
  Card,
  Empty,
  ErrorNotice,
  Field,
  JSONDetails,
  Loading,
  useConfirm,
  useToast,
} from '../components/ui';
export function Skills() {
  const identity = useIdentity();
  const confirm = useConfirm();
  const { dirty } = useDraftState();
  const [params] = useSearchParams();
  const [waker, setWaker] = useState(params.get('waker') || '');
  const [selected, setSelected] = useState('');
  const [search, setSearch] = useState('');
  const state = useQuery({
    queryKey: ['state'],
    queryFn: ({ signal }) => api<PanelState>('state', { signal }),
  });
  const wid = state.data?.wakers.some((w) => w.id === waker)
    ? waker
    : state.data?.wakers[0]?.id || '';
  const list = useQuery({
    queryKey: ['skills', wid],
    queryFn: ({ signal }) => api<Skill[]>('skills?' + query({ waker: wid }), { signal }),
    enabled: !!wid,
  });
  const sid = list.data?.some((s) => s.skillId === selected)
    ? selected
    : list.data?.[0]?.skillId || '';
  async function change(kind: 'waker' | 'skill', value: string) {
    if (dirty && !(await confirm('技能正文有未保存的修改，放弃草稿并切换？'))) return;
    if (kind === 'waker') {
      setWaker(value);
      setSelected('');
    } else setSelected(value);
  }
  if (state.isPending) return <Loading />;
  if (state.error) return <ErrorNotice error={state.error} retry={() => void state.refetch()} />;
  return (
    <div className="skill-workspace">
      <aside className="skill-browser">
        <Field label="Waker">
          <select value={wid} onChange={(e) => void change('waker', e.target.value)}>
            {state.data?.wakers.map((w) => (
              <option key={w.id} value={w.id}>
                {w.name}
              </option>
            ))}
          </select>
        </Field>
        <Field label="查找技能">
          <input
            type="search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="名称或标识"
          />
        </Field>
        <div className="section-caption">技能 {list.data?.length ?? 0} 项</div>
        <ErrorNotice error={list.error} retry={() => void list.refetch()} />
        {list.isFetching && (
          <span role="status" className="muted">
            正在读取…
          </span>
        )}
        <div className="skill-items">
          {list.data
            ?.filter((s) =>
              [s.name, s.skillId].join(' ').toLowerCase().includes(search.toLowerCase()),
            )
            .map((s) => (
              <button
                className="skill-item"
                key={s.skillId}
                aria-current={sid === s.skillId}
                onClick={() => void change('skill', s.skillId)}
              >
                <strong>{s.name || s.skillId}</strong>
                <small>
                  {s.pinned || !s.mutableByAgent
                    ? '只读技能'
                    : can(identity, 'skill.write')
                      ? '可编辑'
                      : '可查看'}{' '}
                  · {s.currentVersionId}
                </small>
              </button>
            ))}
        </div>
      </aside>
      <section className="skill-stage" aria-label="技能工作区">
        {sid ? (
          <SkillLoader key={wid + '/' + sid} waker={wid} skill={sid} />
        ) : (
          <Empty>
            {state.data?.error
              ? '暂时无法读取技能。'
              : wid
                ? '当前 Waker 没有技能。'
                : identity.level === 'admin'
                  ? '请先在机器人页面创建 Waker。'
                  : '尚未分配 Waker，请联系管理员。'}
          </Empty>
        )}
      </section>
    </div>
  );
}
function SkillLoader({ waker, skill }: { waker: string; skill: string }) {
  const data = useQuery({
    queryKey: ['skill', waker, skill],
    queryFn: ({ signal }) =>
      api<SkillContent>('skill/content?' + query({ waker, skill }), { signal }),
  });
  if (data.isPending) return <Loading />;
  if (data.error) return <ErrorNotice error={data.error} retry={() => void data.refetch()} />;
  return <SkillEditor waker={waker} sid={skill} initial={data.data!} />;
}
function SkillEditor({
  waker,
  sid,
  initial,
}: {
  waker: string;
  sid: string;
  initial: SkillContent;
}) {
  const identity = useIdentity();
  const client = useQueryClient();
  const confirm = useConfirm();
  const toast = useToast();
  const [baseline, setBaseline] = useState(initial);
  const [draft, setDraft] = useState(initial.content);
  const [pane, setPane] = useState<'content' | 'history'>('content');
  const [conflict, setConflict] = useState<SkillContent | null>(null);
  const [version, setVersion] = useState('');
  const [reason, setReason] = useState('');
  const editable = can(identity, 'skill.write') && baseline.editable && baseline.contentAvailable;
  const dirty = editable && draft !== baseline.content;
  useDirtyGuard(dirty);
  const history = useQuery({
    queryKey: ['skill-versions', waker, sid],
    queryFn: ({ signal }) =>
      api<SkillVersion[]>('skill/versions?' + query({ waker, skill: sid }), { signal }),
    enabled: pane === 'history',
  });
  const diff = useQuery({
    queryKey: ['skill-diff', waker, sid, version],
    queryFn: ({ signal }) =>
      api<unknown>('skill/diff?' + query({ waker, skill: sid, version }), { signal }),
    enabled: !!version,
  });
  async function reload() {
    const current = await api<SkillContent>('skill/content?' + query({ waker, skill: sid }));
    setBaseline(current);
    setDraft(current.content);
    setConflict(null);
    client.setQueryData(['skill', waker, sid], current);
    await client.invalidateQueries({ queryKey: ['skill-versions', waker, sid] });
    await client.invalidateQueries({ queryKey: ['skills', waker] });
    await client.invalidateQueries({ queryKey: ['audit'] });
  }
  const save = useMutation({
    mutationFn: async (action: 'save' | 'rollback') => {
      const path = action === 'save' ? 'skill/update' : 'skill/rollback';
      if (
        !(await confirm(
          action === 'save' ? '保存当前技能正文？' : '恢复所选版本？当前技能正文将被替换。',
        ))
      )
        return false;
      await api(path, {
        body: {
          wakerId: waker,
          skillId: sid,
          baseVersionId: baseline.skill.currentVersionId,
          ...(action === 'save' ? { content: draft } : { versionId: version, reason }),
          confirm: '/api/' + path,
        },
      });
      return true;
    },
    onSuccess: async (saved) => {
      if (!saved) return;
      toast('已保存');
      try {
        await reload();
      } catch {
        toast('保存已完成，请重新读取最新版本。');
      }
    },
    onError: async (error) => {
      if (error instanceof APIError && error.code === 'version_conflict') {
        try {
          setConflict(await api<SkillContent>('skill/content?' + query({ waker, skill: sid })));
        } catch {}
      }
    },
  });
  useEffect(() => {
    const handler = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 's') {
        event.preventDefault();
        if (dirty && !save.isPending) save.mutate('save');
      }
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [dirty, save]);
  return (
    <>
      <header className="skill-editor-heading">
        <div>
          <h2>{baseline.skill.name || sid}</h2>
          <p className="muted">
            {waker} · {baseline.skill.currentVersionId}
          </p>
        </div>
        <div className="row">
          <span className="save-state">
            {editable ? (dirty ? '未保存修改' : '已保存') : '只读'}
          </span>
          <button
            disabled={!dirty || save.isPending}
            className="primary"
            onClick={() => save.mutate('save')}
          >
            保存修改
          </button>
        </div>
      </header>
      <ErrorNotice error={save.error} />
      <div className="segmented" aria-label="技能视图">
        <button aria-pressed={pane === 'content'} onClick={() => setPane('content')}>
          正文
        </button>
        <button aria-pressed={pane === 'history'} onClick={() => setPane('history')}>
          版本与差异
        </button>
        <button
          onClick={async () => {
            if (!dirty || (await confirm('重新读取将替换当前草稿，继续？'))) {
              try {
                await reload();
              } catch (error) {
                toast(error instanceof Error ? error.message : '读取未完成');
              }
            }
          }}
        >
          重新读取
        </button>
      </div>
      {pane === 'content' ? (
        <>
          <Field label="技能正文">
            <textarea
              className="skill-content"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              readOnly={!editable || save.isPending}
              spellCheck={false}
            />
          </Field>
          <div className="editor-meta">
            <span>Markdown · ⌘ S / Ctrl S 保存</span>
            <span>
              {draft.split('\n').length} 行 · {draft.length} 字符
            </span>
          </div>
        </>
      ) : (
        <div className="skill-history">
          <ErrorNotice error={history.error} />
          {history.isPending ? (
            <Loading />
          ) : (
            <>
              <Field label="版本">
                <select value={version} onChange={(e) => setVersion(e.target.value)}>
                  <option value="">选择版本</option>
                  {history.data?.map((v) => (
                    <option key={v.versionId} value={v.versionId}>
                      {v.versionId} · {v.reason || v.createdAt}
                    </option>
                  ))}
                </select>
              </Field>
              <ErrorNotice error={diff.error} />
              {diff.data !== undefined && <JSONDetails value={diff.data} label="版本差异" />}
              {version && editable && (
                <>
                  <Field label="恢复原因">
                    <textarea
                      value={reason}
                      onChange={(e) => setReason(e.target.value)}
                      maxLength={500}
                    />
                  </Field>
                  <button
                    disabled={!reason.trim() || save.isPending}
                    onClick={() => save.mutate('rollback')}
                  >
                    恢复此版本
                  </button>
                </>
              )}
            </>
          )}
        </div>
      )}
      {conflict && (
        <Card title="当前版本已变化">
          <p>你的修改保留在编辑框中。对照当前版本后重新读取，再合并需要保留的内容。</p>
          <JSONDetails label="读取时的正文" value={baseline.content} />
          <JSONDetails label="你的修改" value={draft} />
          <JSONDetails label="当前正文" value={conflict.content} />
        </Card>
      )}
    </>
  );
}
