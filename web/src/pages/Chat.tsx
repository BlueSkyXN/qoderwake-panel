import { useEffect, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { api, query } from '../lib/api';
import { useIdentity } from '../context/session';
import { useDirtyGuard } from '../context/drafts';
import { usePanelState, WakerPicker, activityStatus } from '../components/resources';
import { Card, Empty, ErrorNotice, Field, Loading, useConfirm } from '../components/ui';
interface ChatSession {
  id: string;
  title: string;
  status: string;
  startedAt: string;
}
interface Messages {
  messages: { role: string; text: string }[];
  status: string;
}
export function Chat() {
  const identity = useIdentity();
  const state = usePanelState();
  const client = useQueryClient();
  const confirm = useConfirm();
  const [chosen, setChosen] = useState('');
  const [selected, setSelected] = useState('');
  const [draft, setDraft] = useState('');
  const [pollUntil, setPollUntil] = useState(0);
  const [expired, setExpired] = useState(false);
  const area = useRef<HTMLDivElement>(null);
  const follow = useRef(true);
  const wid = chosen || state.data?.wakers[0]?.id || '';
  useDirtyGuard(!!draft);
  const sessions = useQuery({
    queryKey: ['chat-sessions', wid],
    queryFn: ({ signal }) =>
      api<ChatSession[]>('chat/sessions?' + query({ waker: wid }), { signal }),
    enabled: !!wid && identity.level === 'admin',
  });
  const messages = useQuery({
    queryKey: ['chat-messages', selected],
    queryFn: ({ signal }) =>
      api<Messages>('chat/messages?' + query({ session: selected }), { signal }),
    enabled: !!selected && identity.level === 'admin',
    staleTime: 0,
    refetchInterval: (q) =>
      q.state.data?.status === 'open' && Date.now() < pollUntil ? 2500 : false,
  });
  useEffect(() => {
    setExpired(false);
    if (!pollUntil) return;
    const timer = window.setTimeout(() => setExpired(true), Math.max(0, pollUntil - Date.now()));
    return () => clearTimeout(timer);
  }, [pollUntil]);
  useEffect(() => {
    if (follow.current && area.current) area.current.scrollTop = area.current.scrollHeight;
  }, [messages.data]);
  async function choose(id: string) {
    if (draft && !(await confirm('放弃未发送的消息并切换会话？'))) return;
    setDraft('');
    setSelected(id);
    setPollUntil(Date.now() + 120000);
    follow.current = true;
  }
  const create = useMutation({
    mutationFn: async () => {
      if (draft && !(await confirm('放弃未发送的消息并新建会话？'))) return;
      const value = await api<{ id: string }>('chat/new', {
        body: { wakerId: wid, title: '面板会话' },
      });
      setDraft('');
      setSelected(value.id);
      setPollUntil(Date.now() + 120000);
      await client.invalidateQueries({ queryKey: ['chat-sessions', wid] });
    },
  });
  const send = useMutation({
    mutationFn: async () => {
      let sid = selected;
      if (!sid) {
        const value = await api<{ id: string }>('chat/new', {
          body: { wakerId: wid, title: '面板会话' },
        });
        sid = value.id;
        setSelected(sid);
      }
      await api('chat/send', { body: { sessionId: sid, message: draft } });
      setDraft('');
      follow.current = true;
      setPollUntil(Date.now() + 120000);
      await client.invalidateQueries({ queryKey: ['chat-messages', sid] });
      await client.invalidateQueries({ queryKey: ['chat-sessions', wid] });
    },
  });
  if (identity.level !== 'admin') return <Empty>此功能仅管理员可用。</Empty>;
  if (state.isPending) return <Loading />;
  if (state.error) return <ErrorNotice error={state.error} />;
  const busy = send.isPending || create.isPending;
  return (
    <div className="chat-layout">
      <aside className="sessions">
        <WakerPicker
          items={state.data?.wakers || []}
          value={wid}
          disabled={busy}
          onChange={async (value) => {
            if (draft && !(await confirm('放弃未发送的消息并切换机器人？'))) return;
            setChosen(value);
            setSelected('');
            setDraft('');
            setPollUntil(0);
          }}
        />
        <button disabled={!wid || busy} onClick={() => create.mutate()}>
          新建会话
        </button>
        <ErrorNotice error={create.error} />
        {sessions.isFetching && !sessions.data ? (
          <Loading />
        ) : sessions.error ? (
          <ErrorNotice error={sessions.error} retry={() => void sessions.refetch()} />
        ) : sessions.data?.length ? (
          sessions.data.map((row) => (
            <button
              key={row.id}
              className={'session-item' + (selected === row.id ? ' selected' : '')}
              disabled={busy}
              onClick={() => void choose(row.id)}
            >
              {row.title}
              <small>
                {activityStatus(row.status)} · {row.startedAt}
              </small>
            </button>
          ))
        ) : (
          <Empty>暂无会话。</Empty>
        )}
      </aside>
      <section className="chat-main">
        <div
          className="messages"
          ref={area}
          onScroll={() => {
            const element = area.current;
            if (element)
              follow.current =
                element.scrollHeight - element.scrollTop - element.clientHeight < 100;
          }}
          aria-label="聊天记录"
        >
          {messages.error ? (
            <ErrorNotice error={messages.error} retry={() => void messages.refetch()} />
          ) : selected && messages.isPending ? (
            <Loading />
          ) : messages.data?.messages.length ? (
            messages.data.messages.map((m, index) => (
              <div key={index} className={'message ' + (m.role === 'user' ? 'user' : 'assistant')}>
                <small>
                  {m.role === 'user'
                    ? '你'
                    : state.data?.wakers.find((w) => w.id === wid)?.name || '机器人'}
                </small>
                <div className="message-text">{m.text}</div>
              </div>
            ))
          ) : (
            <Empty>{selected ? '会话已就绪。' : '选择会话或发送第一条消息。'}</Empty>
          )}
        </div>
        <div className="chat-status" role="status">
          {messages.data?.status && activityStatus(messages.data.status)}
          {expired && messages.data?.status === 'open' && ' · 等待超过 120 秒，请刷新核对结果。'}
          {selected && (
            <button
              onClick={() => {
                setPollUntil(Date.now() + 120000);
                void messages.refetch();
              }}
            >
              刷新回复
            </button>
          )}
        </div>
        <form
          className="composer"
          onSubmit={(e) => {
            e.preventDefault();
            if (wid && draft.trim() && !busy) send.mutate();
          }}
        >
          <Field label="消息">
            <textarea
              value={draft}
              disabled={busy || !wid}
              required
              maxLength={16000}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
                  e.preventDefault();
                  e.currentTarget.form?.requestSubmit();
                }
              }}
              placeholder="Enter 发送，Shift + Enter 换行"
            />
          </Field>
          <button className="primary" disabled={busy || !wid || !draft.trim()}>
            {send.isPending ? '正在发送…' : '发送'}
          </button>
        </form>
        <p className="fine muted chat-cost">发送消息会调用当前模型，可能产生费用。</p>
        <ErrorNotice error={send.error} />
      </section>
    </div>
  );
}
export function Gateway() {
  const identity = useIdentity();
  const state = usePanelState();
  const [chosen, setChosen] = useState('');
  const [message, setMessage] = useState('');
  const wid = chosen || state.data?.wakers[0]?.id || '';
  useDirtyGuard(!!message);
  const send = useMutation({
    mutationFn: () => api<{ message: string }>('gw', { body: { wakerId: wid, message } }),
    onSuccess: () => setMessage(''),
  });
  if (identity.level !== 'admin') return <Empty>此功能仅管理员可用。</Empty>;
  if (state.isPending) return <Loading />;
  if (state.error) return <ErrorNotice error={state.error} />;
  return (
    <>
      <Card title="程序调用">
        <p>在调用密钥页面创建凭据，并通过 Authorization 请求头传递。</p>
        <Link to="/callers">管理调用密钥</Link>
        <pre className="code">
          {'POST ' +
            location.origin +
            '/api/gw\nAuthorization: Bearer <CALLER_TOKEN>\nContent-Type: application/json\n\n{"wakerId":"<WAKER_ID>","message":"你好"}'}
        </pre>
        <p className="fine muted">
          按 IP 和角色各限 20 次 / 5 分钟，每进程最多 2 个模型调用同时执行。
        </p>
      </Card>
      <Card title="测试调用">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            send.mutate();
          }}
        >
          <fieldset disabled={send.isPending}>
            <WakerPicker items={state.data?.wakers || []} value={wid} onChange={setChosen} />
            <Field label="消息">
              <textarea
                value={message}
                required
                maxLength={16000}
                onChange={(e) => setMessage(e.target.value)}
              />
            </Field>
            <div className="form-actions">
              <span className="fine muted">调用所选机器人的模型，可能产生费用。</span>
              <button className="primary" disabled={!wid || !message.trim()}>
                {send.isPending ? '等待回复…' : '发送请求'}
              </button>
            </div>
          </fieldset>
        </form>
        <ErrorNotice error={send.error} />
        {send.data && <pre className="memory-text">{send.data.message}</pre>}
      </Card>
    </>
  );
}
