import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { api } from '../lib/api';
import { useIdentity } from '../context/session';
import { ErrorNotice, useConfirm, useToast } from './ui';

export type Policy = {
  hotDeploy: boolean;
  embeddingDisabled: boolean;
  sdkByok?: boolean;
  sessionProjectionUplink?: boolean | null;
  remoteExecutionUplink?: boolean | null;
};
export interface RuntimeState {
  desired: Policy;
  observed: Partial<Policy>[];
  observedUplink: Partial<Policy>;
  effectiveOnPanelRestart: Partial<Policy>;
  pendingRestart: boolean | null;
  sdkByokPending: boolean;
  restartAllowed: boolean;
  restartBlocker: string | null;
}
export interface Maintenance {
  mode: 'normal' | 'draining' | 'maintenance' | 'restarting';
  reason?: string;
  activeMutations?: number;
  activeCalls?: number;
  expires: number | null;
}
export const maintenanceLabels = {
  normal: '正常运行',
  draining: '正在排空请求',
  maintenance: '维护中',
  restarting: '正在重启',
};
export function useRuntime() {
  const identity = useIdentity();
  return useQuery({
    queryKey: ['runtime'],
    queryFn: ({ signal }) => api<RuntimeState>('runtime', { signal }),
    enabled: identity.level === 'admin',
  });
}
export function useMaintenance() {
  return useQuery({
    queryKey: ['maintenance'],
    queryFn: ({ signal }) => api<Maintenance>('maintenance', { signal }),
    refetchInterval: 5000,
  });
}
export function MaintenanceBanner() {
  const identity = useIdentity();
  const state = useMaintenance();
  const mode = state.data?.mode;
  if (!mode || mode === 'normal') return null;
  return (
    <div className="notice maintenance-banner" role="status">
      <strong>{maintenanceLabels[mode]}</strong> · 新的业务写请求已暂停。
      {identity.level === 'admin' && <Link to="/controls">管理维护状态</Link>}
    </div>
  );
}
export function ApplyRuntimeButton() {
  const runtime = useRuntime();
  const client = useQueryClient();
  const confirm = useConfirm();
  const toast = useToast();
  const mutation = useMutation({
    mutationFn: async () => {
      if (
        !(await confirm(
          '应用已保存的配置并重启 daemon？面板会先排空请求并等待 daemon 空闲。',
          '应用配置',
        ))
      )
        return;
      await api('apply', { body: { confirm: '/api/apply' } });
      toast('daemon 已重启');
      await Promise.all(
        ['runtime', 'state', 'maintenance'].map((key) =>
          client.invalidateQueries({ queryKey: [key] }),
        ),
      );
    },
  });
  return (
    <div>
      <button
        disabled={mutation.isPending || !runtime.data?.restartAllowed}
        onClick={() => mutation.mutate()}
      >
        {mutation.isPending ? '正在应用…' : '应用配置并重启'}
      </button>
      {runtime.data?.restartAllowed === false && (
        <p className="fine muted">无法核实受管进程，请在宿主机检查实例状态。</p>
      )}
      <ErrorNotice error={mutation.error || runtime.error} />
    </div>
  );
}
