import { useQuery } from '@tanstack/react-query';
import { api } from '../lib/api';
import type { PanelState, Waker } from '../lib/types';
import { Field } from './ui';
export const usePanelState = () =>
  useQuery({ queryKey: ['state'], queryFn: ({ signal }) => api<PanelState>('state', { signal }) });
export function WakerPicker({
  items,
  value,
  onChange,
  label = '机器人',
  disabled = false,
}: {
  items: Waker[];
  value: string;
  onChange: (id: string) => void;
  label?: string;
  disabled?: boolean;
}) {
  return (
    <Field label={label}>
      <select value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)}>
        <option value="">选择机器人</option>
        {items.map((w) => (
          <option key={w.id} value={w.id}>
            {w.name || w.id}
          </option>
        ))}
      </select>
    </Field>
  );
}
export function Pager({
  page,
  more,
  busy,
  onChange,
}: {
  page: number;
  more: boolean;
  busy?: boolean;
  onChange: (page: number) => void;
}) {
  return (
    <div className="row pagination">
      <button disabled={busy || page <= 1} onClick={() => onChange(page - 1)}>
        上一页
      </button>
      <span>第 {page} 页</span>
      <button disabled={busy || !more} onClick={() => onChange(page + 1)}>
        下一页
      </button>
    </div>
  );
}
export function activityStatus(value: unknown): string {
  return (
    (
      {
        success: '已完成',
        succeeded: '已完成',
        done: '已结束',
        failed: '失败',
        error: '失败',
        running: '运行中',
        open: '处理中',
        queued: '排队中',
        pending: '等待中',
        idle: '待命',
        cancelled: '已取消',
        canceled: '已取消',
        stopped: '已停止',
        paused: '已暂停',
        closed: '已结束',
        connected: '已连接',
        disconnected: '未连接',
        connecting: '连接中',
      } as Record<string, string>
    )[String(value)] || (value == null ? '未提供' : '未知')
  );
}
export interface PageResult<T> {
  items: T[];
  pagination?: { total?: number; pageSize?: number };
}
export function hasNext<T>(data: PageResult<T> | undefined, page: number, size = 20) {
  return typeof data?.pagination?.total === 'number'
    ? page * (data.pagination.pageSize || size) < data.pagination.total
    : (data?.items.length || 0) === size;
}
