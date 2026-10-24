import {
  cloneElement,
  createContext,
  isValidElement,
  useCallback,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
} from 'react';
import type { ReactElement, ReactNode } from 'react';
import * as Dialog from '@radix-ui/react-dialog';
import { APIError } from '../lib/api';
export function Card({
  title,
  children,
  actions,
}: {
  title?: string;
  children: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <section className="card">
      {title && (
        <div className="section-heading">
          <h2>{title}</h2>
          {actions}
        </div>
      )}
      {children}
    </section>
  );
}
export function Empty({ children = '暂无记录' }: { children?: ReactNode }) {
  return <div className="empty">{children}</div>;
}
export function ErrorNotice({ error, retry }: { error: unknown; retry?: () => void }) {
  if (!error) return null;
  return (
    <div className="notice error-notice" role="alert">
      <p>
        {error instanceof Error && /[\u3400-\u9fff]/.test(error.message)
          ? error.message
          : '操作未完成，请查看错误详情。'}
      </p>
      {error instanceof APIError && (
        <details>
          <summary>错误详情</summary>
          <code>{error.code}</code>
        </details>
      )}
      {retry && <button onClick={retry}>重试</button>}
    </div>
  );
}
export function Loading() {
  return (
    <div className="empty" role="status">
      正在读取…
    </div>
  );
}
export function Table({ head, children }: { head: string[]; children: ReactNode }) {
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            {head.map((h) => (
              <th key={h}>{h}</th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}
export function JSONDetails({ value, label = '查看详情' }: { value: unknown; label?: string }) {
  return (
    <details>
      <summary>{label}</summary>
      <pre className="code">{JSON.stringify(value, null, 2)}</pre>
    </details>
  );
}
export function Field({
  label,
  children,
  hint,
}: {
  label: string;
  children: ReactNode;
  hint?: string;
}) {
  const id = useId();
  let control = children;
  if (isValidElement(children)) {
    const element = children as ReactElement<Record<string, unknown>>;
    control = cloneElement(element, {
      'aria-labelledby': element.props['aria-labelledby'] || id + '-label',
      'aria-describedby':
        [element.props['aria-describedby'], hint ? id + '-hint' : null].filter(Boolean).join(' ') ||
        undefined,
    });
  }
  return (
    <label className="field">
      <span id={id + '-label'}>{label}</span>
      {control}
      {hint && (
        <small id={id + '-hint'} className="muted">
          {hint}
        </small>
      )}
    </label>
  );
}
type Confirm = (message: string, title?: string) => Promise<boolean>;
const ConfirmContext = createContext<Confirm>(() => Promise.resolve(false));
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [prompt, setPrompt] = useState<{ title: string; message: string } | null>(null);
  const pending = useRef<((value: boolean) => void) | null>(null);
  const finish = useCallback((value: boolean) => {
    pending.current?.(value);
    pending.current = null;
    setPrompt(null);
  }, []);
  const confirm = useCallback<Confirm>((message, title = '确认操作') => {
    if (pending.current) return Promise.resolve(false);
    return new Promise((resolve) => {
      pending.current = resolve;
      setPrompt({ message, title });
    });
  }, []);
  useEffect(() => () => pending.current?.(false), []);
  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      <Dialog.Root
        open={!!prompt}
        onOpenChange={(open) => {
          if (!open) finish(false);
        }}
      >
        <Dialog.Portal>
          <Dialog.Overlay className="dialog-overlay" />
          <Dialog.Content className="dialog-content">
            <Dialog.Title>{prompt?.title}</Dialog.Title>
            <Dialog.Description>{prompt?.message}</Dialog.Description>
            <div className="dialog-actions">
              <button onClick={() => finish(false)}>取消</button>
              <button className="primary" onClick={() => finish(true)}>
                确认继续
              </button>
            </div>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    </ConfirmContext.Provider>
  );
}
export const useConfirm = () => useContext(ConfirmContext);
const ToastContext = createContext<(message: string) => void>(() => {});
export function ToastProvider({ children }: { children: ReactNode }) {
  const [message, setMessage] = useState('');
  useEffect(() => {
    if (!message) return;
    const timer = setTimeout(() => setMessage(''), 6000);
    return () => clearTimeout(timer);
  }, [message]);
  return (
    <ToastContext.Provider value={setMessage}>
      {children}
      {message && (
        <div className="toast" role="status">
          {message}
        </div>
      )}
    </ToastContext.Provider>
  );
}
export const useToast = () => useContext(ToastContext);
