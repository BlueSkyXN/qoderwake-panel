import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api, APIError, query } from '../lib/api';
import { useIdentity } from '../context/session';
import { usePanelState, WakerPicker } from '../components/resources';
import { Card, Empty, ErrorNotice, Field, Loading } from '../components/ui';
interface MemoryDoc {
  path: string;
  content: string;
  truncated?: boolean;
}
interface MemoryRecord {
  memoryId: string;
  title: string;
  content: string;
  updatedAt?: string;
  securityLevel?: string;
  truncated?: boolean;
}
interface MemoryData {
  kind: string;
  status: string;
  docs: MemoryDoc[];
  memories?: MemoryRecord[];
  filesStatus?: string;
  error?: string;
  filesError?: string;
  truncated: boolean;
}
export function Memory() {
  const identity = useIdentity();
  const state = usePanelState();
  const [chosen, setChosen] = useState('');
  const [search, setSearch] = useState('');
  const wid = chosen || state.data?.wakers[0]?.id || '';
  const memory = useQuery({
    queryKey: ['memory', wid],
    queryFn: ({ signal }) => api<MemoryData>('memory?' + query({ waker: wid }), { signal }),
    enabled: !!wid && identity.level === 'admin',
  });
  if (identity.level !== 'admin') return <Empty>此功能仅管理员可用。</Empty>;
  if (state.isPending) return <Loading />;
  if (state.error) return <ErrorNotice error={state.error} />;
  const data = memory.data;
  const term = search.toLowerCase();
  const docs =
    data?.docs.filter((d) => [d.path, d.content].join(' ').toLowerCase().includes(term)) || [];
  const memories =
    data?.memories?.filter((d) => [d.title, d.content].join(' ').toLowerCase().includes(term)) ||
    [];
  return (
    <>
      <Card title="记忆">
        <div className="form-grid">
          <WakerPicker
            items={state.data?.wakers || []}
            value={wid}
            onChange={(value) => {
              setChosen(value);
              setSearch('');
            }}
          />
          <Field label="查找记忆">
            <input
              type="search"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="文件名、标题或正文"
            />
          </Field>
        </div>
      </Card>
      {!wid ? (
        <Empty>请先创建机器人。</Empty>
      ) : memory.isPending ? (
        <Loading />
      ) : memory.error ? (
        <ErrorNotice error={memory.error} retry={() => void memory.refetch()} />
      ) : (
        data && (
          <>
            {data.kind === 'personal_avatar' && (
              <Card title="分身记忆">
                {data.status === 'unavailable' && <MemoryError code={data.error} />}
                {memories.map((m) => (
                  <details className="memory-item" key={m.memoryId}>
                    <summary>{m.title || m.memoryId}</summary>
                    {m.updatedAt && <p className="fine muted">{m.updatedAt}</p>}
                    <pre className="memory-text">{m.content}</pre>
                    {m.truncated && <p className="fine muted">显示前 200 KiB。</p>}
                  </details>
                ))}
                {data.status === 'ready' && !memories.length && (
                  <Empty>{search ? '没有匹配的记忆。' : '尚无分身记忆。'}</Empty>
                )}
              </Card>
            )}
            <Card title={data.kind === 'personal_avatar' ? '工作区记忆文件' : '记忆文件'}>
              {data.filesStatus === 'unavailable' && <MemoryError code={data.filesError} />}
              {docs.map((doc, index) => (
                <details
                  className="memory-item"
                  key={doc.path}
                  open={index === 0 && docs.length < 4}
                >
                  <summary className="mono">{doc.path}</summary>
                  <pre className="memory-text">{doc.content}</pre>
                  {doc.truncated && <p className="fine muted">显示前 200 KiB。</p>}
                </details>
              ))}
              {data.filesStatus !== 'unavailable' && !docs.length && (
                <Empty>{search ? '没有匹配的文件。' : '暂无记忆文件。'}</Empty>
              )}
            </Card>
            {data.truncated && (
              <p className="notice">部分内容超过预览上限，完整记忆可在官方工具中查看。</p>
            )}
          </>
        )
      )}
    </>
  );
}
function MemoryError({ code }: { code?: string }) {
  const messages: Record<string, string> = {
    PERSONAL_AVATAR_DWS_LOGIN_REQUIRED:
      '请在官方控制台完成分身记忆设置：使用文件模式，或登录钉钉。',
    PERSONAL_AVATAR_OWNER_REQUIRED: '请在官方控制台重新登录。',
    PERSONAL_AVATAR_DWS_UNAVAILABLE: '请在官方控制台检查钉钉连接组件。',
    PERSONAL_AVATAR_ACCOUNT_CHANGED: '分身身份已变化，请在官方控制台重新确认记忆设置。',
    daemon_response_too_large: '记忆内容超过单次读取上限，请到官方控制台查看。',
  };
  const error = new APIError(code || 'memory_unavailable', 502);
  if (code && messages[code]) error.message = messages[code];
  return <ErrorNotice error={error} />;
}
