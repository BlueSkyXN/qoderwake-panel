import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, query } from '../lib/api';
import { useIdentity } from '../context/session';
import { usePanelState, WakerPicker, Pager, activityStatus } from '../components/resources';
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
interface Session {
  session_id: string;
  title?: string;
  origin?: string;
  session_status?: string;
  connection_status?: string;
  unread: boolean;
  created_at?: string;
  updated_at?: string;
}
interface Artifact {
  id: string;
  title?: string;
  type?: string;
  description?: string;
  relativePath?: string;
  mimeType?: string;
  filesChanged?: unknown;
}
export function Sessions() {
  const identity = useIdentity();
  const state = usePanelState();
  const confirm = useConfirm();
  const toast = useToast();
  const client = useQueryClient();
  const [chosen, setChosen] = useState('');
  const [origin, setOrigin] = useState('');
  const [page, setPage] = useState(1);
  const [selected, setSelected] = useState('');
  const wid = chosen || state.data?.wakers[0]?.id || '';
  const sessions = useQuery({
    queryKey: ['sessions', wid, origin, page],
    queryFn: ({ signal }) =>
      api<{ items: Session[]; has_more: boolean }>(
        'sessions/query?' + query({ waker: wid, origin, offset: (page - 1) * 20, limit: 20 }),
        { signal },
      ),
    enabled: !!wid,
  });
  const action = useMutation({
    mutationFn: async ({ row, remove }: { row: Session; remove: boolean }) => {
      if (
        remove &&
        !(await confirm(
          '删除 ' + (row.title || row.session_id) + ' 及其事件和产物？运行中的会话需先停止。',
          '删除会话',
        ))
      )
        return;
      await api(remove ? 'session/delete' : 'session/read', {
        body: {
          sessionId: row.session_id,
          ...(remove ? { force: false, confirm: '/api/session/delete' } : {}),
        },
      });
      if (remove && selected === row.session_id) setSelected('');
      toast(remove ? '会话已删除' : '已标记为已读');
      await client.invalidateQueries({ queryKey: ['sessions'] });
      await client.invalidateQueries({ queryKey: ['audit'] });
    },
  });
  if (state.isPending) return <Loading />;
  if (state.error) return <ErrorNotice error={state.error} />;
  return (
    <>
      <Card title="筛选">
        <div className="form-grid">
          <WakerPicker
            items={state.data?.wakers || []}
            value={wid}
            onChange={(value) => {
              setChosen(value);
              setPage(1);
              setSelected('');
            }}
          />
          <Field label="来源">
            <select
              value={origin}
              onChange={(e) => {
                setOrigin(e.target.value);
                setPage(1);
                setSelected('');
              }}
            >
              <option value="">全部</option>
              <option value="chat">聊天</option>
              <option value="at_waker">@Waker</option>
            </select>
          </Field>
        </div>
      </Card>
      <Card title="会话">
        {!wid ? (
          <Empty>请选择已分配的机器人。</Empty>
        ) : sessions.isPending ? (
          <Loading />
        ) : sessions.error ? (
          <ErrorNotice error={sessions.error} retry={() => void sessions.refetch()} />
        ) : (
          <>
            {sessions.data?.items.length ? (
              <Table
                head={[
                  '标题 / 来源',
                  '状态',
                  '未读',
                  ...(identity.level === 'admin' ? ['操作'] : []),
                ]}
              >
                {sessions.data.items.map((row) => (
                  <tr key={row.session_id}>
                    <td>
                      <strong>{row.title || row.session_id}</strong>
                      <br />
                      <span className="mono muted">
                        {row.origin === 'chat' ? '聊天' : row.origin || '—'}
                      </span>
                    </td>
                    <td>{activityStatus(row.session_status || row.connection_status)}</td>
                    <td>{row.unread ? '是' : '否'}</td>
                    {identity.level === 'admin' && (
                      <td className="actions">
                        <button onClick={() => setSelected(row.session_id)}>产物</button>
                        <button
                          disabled={action.isPending}
                          onClick={() => action.mutate({ row, remove: false })}
                        >
                          标已读
                        </button>
                        <button
                          className="danger"
                          disabled={action.isPending}
                          onClick={() => action.mutate({ row, remove: true })}
                        >
                          删除
                        </button>
                      </td>
                    )}
                  </tr>
                ))}
              </Table>
            ) : (
              <Empty>{origin ? '此来源没有会话。' : '本页没有会话。'}</Empty>
            )}
            <Pager
              page={page}
              more={!!sessions.data?.has_more}
              busy={sessions.isFetching}
              onChange={(value) => {
                setPage(value);
                setSelected('');
              }}
            />
          </>
        )}
        <ErrorNotice error={action.error} />
      </Card>
      {selected && identity.level === 'admin' && <Artifacts key={selected} id={selected} />}
    </>
  );
}
function Artifacts({ id }: { id: string }) {
  const data = useQuery({
    queryKey: ['artifacts', id],
    queryFn: ({ signal }) =>
      api<{ artifacts: Artifact[] }>('session/artifacts?' + query({ session: id }), { signal }),
  });
  return (
    <Card title="会话产物">
      {data.isPending ? (
        <Loading />
      ) : data.error ? (
        <ErrorNotice error={data.error} retry={() => void data.refetch()} />
      ) : data.data?.artifacts.length ? (
        <Table head={['标题', '类型', '相对路径', '操作']}>
          {data.data.artifacts.map((a) => (
            <tr key={a.id}>
              <td>
                {a.title || a.id}
                {a.description && <p className="muted">{a.description}</p>}
              </td>
              <td>{a.type === 'file' ? '文件' : a.mimeType || '其他产物'}</td>
              <td className="mono">{a.relativePath || '—'}</td>
              <td>
                <a
                  download
                  href={'/api/session/artifact-file?' + query({ session: id, artifact: a.id })}
                >
                  下载
                </a>
              </td>
            </tr>
          ))}
        </Table>
      ) : (
        <Empty>此会话没有产物。</Empty>
      )}
      <p className="fine muted">单个文件最大 32 MiB。</p>
    </Card>
  );
}
