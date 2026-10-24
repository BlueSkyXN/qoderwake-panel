import { createContext, useContext, useEffect, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import * as Dialog from '@radix-ui/react-dialog';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, APIError } from '../lib/api';
import { errors } from '../lib/messages';
import { requestID } from '../lib/ids';
import { useIdentity } from '../context/session';
import { ErrorNotice, Field, Loading, Table } from './ui';

type Target = { kind: 'waker' | 'provider'; target: string; baseRevision?: string };
type Pending = Target & { key: string };
type Request = Target & { onDeleted?: () => void };
interface Impact {
  id: string | null;
  expires: number | null;
  impactDigest: string;
  deletable: boolean;
  warningsRequired: string[];
  checks: {
    id: string;
    label: string;
    status: string;
    count: number;
    required: boolean;
    note: string;
    refs: string[];
  }[];
}
interface Result {
  status: string;
  error?: string;
  reconciled?: boolean;
}
const DeletionContext = createContext<(value: Request) => void>(() => {});
const same = (a: Target, b: Target) => a.kind === b.kind && a.target === b.target;
function restore(storage: string): Pending[] {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(storage) || '[]');
    if (Array.isArray(value))
      return value.filter(
        (v): v is Pending =>
          v &&
          ['waker', 'provider'].includes(v.kind) &&
          typeof v.target === 'string' &&
          typeof v.key === 'string',
      );
  } catch {}
  return [];
}
export function DeletionProvider({ children }: { children: ReactNode }) {
  const identity = useIdentity();
  const storage = 'panel-deletions:' + (identity.userId || identity.level);
  const [pending, setPending] = useState(() => restore(storage));
  const [active, setActive] = useState<(Request & { serial: number }) | null>(null);
  useEffect(() => {
    try {
      localStorage.setItem(storage, JSON.stringify(pending));
    } catch {}
  }, [storage, pending]);
  const open = (value: Request) => setActive({ ...value, serial: Date.now() });
  function remember(value: Pending) {
    setPending((old) => [...old.filter((v) => !same(v, value)), value]);
  }
  function forget(value: Target) {
    setPending((old) => old.filter((v) => !same(v, value)));
  }
  return (
    <DeletionContext.Provider value={open}>
      {pending.length > 0 && identity.level === 'admin' && (
        <div className="deletion-recovery notice" role="status">
          <strong>待核对的删除</strong>
          {pending.map((item) => (
            <button key={item.kind + item.target} onClick={() => open(item)}>
              核对 {item.target}
            </button>
          ))}
        </div>
      )}
      {children}
      {active && (
        <DeletionDialog
          key={active.serial}
          target={active}
          remembered={pending.find((v) => same(v, active))}
          remember={remember}
          forget={forget}
          close={() => setActive(null)}
        />
      )}
    </DeletionContext.Provider>
  );
}
export const useDeletion = () => useContext(DeletionContext);

function DeletionDialog({
  target,
  remembered,
  remember,
  forget,
  close,
}: {
  target: Request;
  remembered?: Pending;
  remember: (v: Pending) => void;
  forget: (v: Target) => void;
  close: () => void;
}) {
  const client = useQueryClient();
  const [recovery, setRecovery] = useState(!!remembered);
  const [result, setResult] = useState<Result | null>(null);
  const [acknowledged, setAcknowledged] = useState<string[]>([]);
  const [confirmation, setConfirmation] = useState('');
  const key = useRef(remembered?.key || requestID());
  const request = useRef(requestID());
  const preview = useQuery({
    queryKey: ['deletion-preview', request.current],
    queryFn: ({ signal }) =>
      api<Impact>('deletion/preview', {
        body: { kind: target.kind, target: target.target, baseRevision: target.baseRevision },
        signal,
      }),
    enabled: !recovery && !result,
    gcTime: 0,
  });
  const locked =
    recovery ||
    (preview.error instanceof APIError && preview.error.code === 'deletion_operation_unresolved');
  const persist = () =>
    remember({
      kind: target.kind,
      target: target.target,
      baseRevision: target.baseRevision,
      key: key.current,
    });
  async function accept(value: Result) {
    setResult(value);
    if (['pending', 'unknown'].includes(value.status)) {
      setRecovery(true);
      persist();
    } else {
      forget(target);
      setRecovery(false);
      if (value.status === 'succeeded') target.onDeleted?.();
      await client.invalidateQueries({
        predicate: (q) => ['state', 'audit', 'waker', 'skills'].includes(String(q.queryKey[0])),
      });
    }
  }
  const execute = useMutation({
    mutationFn: async () => {
      if (!preview.data?.id) throw new Error('请重新检查删除依赖');
      const route = target.kind + '/delete';
      persist();
      try {
        await accept(
          await api<Result>(route, {
            headers: { 'Idempotency-Key': key.current },
            body: {
              [target.kind === 'waker' ? 'id' : 'name']: target.target,
              baseRevision: target.baseRevision,
              preview: preview.data.id,
              impactDigest: preview.data.impactDigest,
              acknowledgedWarnings: acknowledged,
              confirm: '/api/' + route,
            },
          }),
        );
      } catch (error) {
        if (error instanceof APIError && error.status >= 400 && error.status < 500) {
          forget(target);
          key.current = requestID();
          setAcknowledged([]);
          void preview.refetch();
        } else setRecovery(true);
        throw error;
      }
    },
  });
  const reconcile = useMutation({
    mutationFn: async () => {
      if (remembered) {
        try {
          const current = await api<Result>('deletion/status', {
            body: { idempotencyKey: key.current },
          });
          if (!['pending', 'unknown'].includes(current.status)) {
            await accept(current);
            return;
          }
        } catch (error) {
          if (!(error instanceof APIError && error.code === 'deletion_operation_not_found'))
            throw error;
        }
      }
      await accept(
        await api<Result>('deletion/reconcile', {
          body: { kind: target.kind, target: target.target },
        }),
      );
    },
  });
  const release = useMutation({
    mutationFn: async () => {
      await api('deletion/release', {
        body: {
          kind: target.kind,
          target: target.target,
          decision: 'failed',
          targetConfirmation: confirmation,
          confirm: '/api/deletion/release',
        },
      });
      await accept({ status: 'failed' });
    },
  });
  const busy = execute.isPending || reconcile.isPending || release.isPending;
  const done = result && ['succeeded', 'failed'].includes(result.status);
  const allChecked = preview.data?.warningsRequired.every((id) => acknowledged.includes(id));
  return (
    <Dialog.Root
      open
      onOpenChange={(open) => {
        if (!open && !busy) close();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content
          className="dialog-content deletion-dialog"
          onEscapeKeyDown={(e) => {
            if (busy) e.preventDefault();
          }}
          onPointerDownOutside={(e) => {
            if (busy) e.preventDefault();
          }}
        >
          <Dialog.Title>
            {done
              ? result.status === 'succeeded'
                ? '删除已完成'
                : '删除已结束'
              : '删除' + (target.kind === 'waker' ? '机器人' : 'Provider')}
          </Dialog.Title>
          <Dialog.Description>
            <code>{target.target}</code>
          </Dialog.Description>
          <div className="dialog-scroll">
            {done ? (
              <p>
                {result.status === 'succeeded'
                  ? '目标已删除。'
                  : '当前删除操作已标记失败。如需重试，请重新检查依赖。'}
              </p>
            ) : locked ? (
              <>
                <p>删除结果待核对。此目标保持锁定，核对完成后可继续修改配置。</p>
                {result?.error && (
                  <p className="notice">{errors[result.error] || '请核对目标状态。'}</p>
                )}
                <button disabled={busy} onClick={() => reconcile.mutate()}>
                  核对目标状态
                </button>
                <details>
                  <summary>手动结束此操作</summary>
                  <p>在宿主机确认删除进程已结束、目标仍存在后，将此操作标记失败并解除锁定。</p>
                  <Field label={'输入目标 ID：' + target.target}>
                    <input
                      value={confirmation}
                      onChange={(e) => setConfirmation(e.target.value)}
                      autoComplete="off"
                    />
                  </Field>
                  <button
                    className="danger"
                    disabled={busy || confirmation !== target.target}
                    onClick={() => release.mutate()}
                  >
                    标记失败并解除锁定
                  </button>
                </details>
              </>
            ) : (
              <>
                {preview.isPending && <Loading />}
                {preview.data && (
                  <>
                    <Table head={['依赖检查', '结果']}>
                      {preview.data.checks.map((check) => (
                        <tr key={check.id}>
                          <td>
                            {check.label}
                            {check.count > 0 && <span className="muted"> · {check.count}</span>}
                            {['blocked', 'unknown'].includes(check.status) && check.note && (
                              <p className="fine muted">{check.note}</p>
                            )}
                            {check.refs.length > 0 && (
                              <details>
                                <summary>查看引用</summary>
                                <ul>
                                  {check.refs.map((ref) => (
                                    <li key={ref}>
                                      <code>{ref}</code>
                                    </li>
                                  ))}
                                </ul>
                              </details>
                            )}
                          </td>
                          <td>
                            {(
                              {
                                clear: '可继续',
                                blocked: '存在引用',
                                unknown: '未能核实',
                                warning: '需确认',
                              } as Record<string, string>
                            )[check.status] || '未能核实'}
                          </td>
                        </tr>
                      ))}
                    </Table>
                    {!preview.data.deletable && (
                      <p className="notice">请先处理引用关系，或恢复未能读取的接口，再重新检查。</p>
                    )}
                    {preview.data.warningsRequired.map((id) => {
                      const check = preview.data!.checks.find((v) => v.id === id);
                      return (
                        <label className="check-row" key={id}>
                          <input
                            type="checkbox"
                            checked={acknowledged.includes(id)}
                            onChange={(e) =>
                              setAcknowledged((old) =>
                                e.target.checked ? [...old, id] : old.filter((v) => v !== id),
                              )
                            }
                          />
                          <span>{check?.note || check?.label || id}</span>
                        </label>
                      );
                    })}
                    <p>删除后无法从此面板恢复。</p>
                  </>
                )}
                <ErrorNotice error={preview.error} />
              </>
            )}
            <ErrorNotice error={execute.error || reconcile.error || release.error} />
          </div>
          <div className="dialog-actions">
            <button disabled={busy} onClick={close}>
              {done ? '关闭' : '取消'}
            </button>
            {!locked && !done && (
              <>
                <button
                  disabled={busy || preview.isFetching}
                  onClick={() => {
                    setAcknowledged([]);
                    execute.reset();
                    void preview.refetch();
                  }}
                >
                  重新检查
                </button>
                <button
                  className="danger"
                  disabled={busy || !preview.data?.deletable || !allChecked || preview.isFetching}
                  onClick={() => execute.mutate()}
                >
                  {execute.isPending ? '正在删除…' : '确认删除'}
                </button>
              </>
            )}
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
