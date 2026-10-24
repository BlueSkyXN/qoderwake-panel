import { useQuery } from '@tanstack/react-query';
import { api } from '../lib/api';
import { useIdentity } from '../context/session';
import { Card, Empty, ErrorNotice, Loading, Table } from '../components/ui';
interface UsageData {
  perModel: [string, number][];
  perDay: [string, number][];
  recent: { day: string; run: string; models: string[] }[];
  scan: {
    complete: boolean;
    bytesRead: number;
    pendingFiles: number;
    unavailableFiles: number;
    skippedLines: number;
    guardBytesRead: number;
    maxBytes: number;
  };
}
export function Usage() {
  const identity = useIdentity();
  const allowed = ['admin', 'viewer'].includes(identity.level);
  const data = useQuery({
    queryKey: ['usage'],
    queryFn: ({ signal }) => api<UsageData>('usage', { signal }),
    enabled: allowed,
    refetchInterval: (q) => (q.state.data?.scan.pendingFiles ? 5000 : 15000),
  });
  if (!allowed) return <Empty>当前账号没有此页面的访问权限。</Empty>;
  if (data.isPending) return <Loading />;
  if (data.error) return <ErrorNotice error={data.error} retry={() => void data.refetch()} />;
  const value = data.data!;
  const max = Math.max(1, ...value.perModel.map((row) => row[1]));
  return (
    <>
      <Card title="扫描状态">
        <p>按本机 worker 日志统计模型回合。</p>
        <dl className="facts">
          <dt>扫描进度</dt>
          <dd>{value.scan.complete ? '当前可读日志已扫描' : '统计尚不完整'}</dd>
          <dt>本次读取</dt>
          <dd>{(value.scan.bytesRead / 1024).toFixed(1)} KiB</dd>
          <dt>待继续的文件</dt>
          <dd>{value.scan.pendingFiles}</dd>
          <dt>不可读的文件</dt>
          <dd>{value.scan.unavailableFiles}</dd>
          <dt>跳过的超长行</dt>
          <dd>{value.scan.skippedLines}</dd>
        </dl>
      </Card>
      <Card title="按模型">
        {value.perModel.length ? (
          <Table head={['模型', '回合数']}>
            {value.perModel.map(([model, count]) => (
              <tr key={model}>
                <td className="mono">{model}</td>
                <td>
                  <div className="usage-metric">
                    <span>{count}</span>
                    <progress max={max} value={count} aria-label={model + ' 回合数'} />
                  </div>
                </td>
              </tr>
            ))}
          </Table>
        ) : (
          <Empty>当前日志中没有模型回合记录。</Empty>
        )}
      </Card>
      <Card title="近 30 天">
        {value.perDay.length ? (
          <Table head={['日期', '回合数']}>
            {value.perDay.map(([day, count]) => (
              <tr key={day}>
                <td>{day}</td>
                <td>{count}</td>
              </tr>
            ))}
          </Table>
        ) : (
          <Empty>近 30 天没有记录。</Empty>
        )}
      </Card>
      {value.recent.length > 0 && (
        <Card title="最近回合">
          <Table head={['时间', '运行标识', '模型']}>
            {value.recent.map((row, index) => (
              <tr key={index}>
                <td>{row.day}</td>
                <td className="mono">{row.run}</td>
                <td>{row.models.join('、')}</td>
              </tr>
            ))}
          </Table>
        </Card>
      )}
    </>
  );
}
