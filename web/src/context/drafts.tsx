import { createContext, useCallback, useContext, useEffect, useId, useState } from 'react';
import type { ReactNode } from 'react';
import { useBlocker } from 'react-router-dom';
import { useConfirm } from '../components/ui';
const DraftContext = createContext({
  dirty: false,
  setDraft: (_id: string, _dirty: boolean) => {},
});
export function DraftProvider({ children }: { children: ReactNode }) {
  const [drafts, setDrafts] = useState<Record<string, boolean>>({});
  const dirty = Object.values(drafts).some(Boolean);
  const setDraft = useCallback(
    (id: string, value: boolean) =>
      setDrafts((old) => {
        if (!!old[id] === value) return old;
        const next = { ...old };
        if (value) next[id] = true;
        else delete next[id];
        return next;
      }),
    [],
  );
  const confirm = useConfirm();
  const blocker = useBlocker(dirty);
  useEffect(() => {
    if (!dirty) return;
    const handler = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', handler);
    return () => window.removeEventListener('beforeunload', handler);
  }, [dirty]);
  useEffect(() => {
    if (blocker.state !== 'blocked') return;
    let active = true;
    void confirm('有未保存的修改，放弃这份草稿并离开？').then((accepted) => {
      if (active) {
        if (accepted) blocker.proceed();
        else blocker.reset();
      }
    });
    return () => {
      active = false;
    };
  }, [blocker, confirm]);
  return <DraftContext.Provider value={{ dirty, setDraft }}>{children}</DraftContext.Provider>;
}
export const useDraftState = () => useContext(DraftContext);
export function useDirtyGuard(dirty: boolean) {
  const { setDraft } = useDraftState();
  const id = useId();
  useEffect(() => {
    setDraft(id, dirty);
    return () => setDraft(id, false);
  }, [id, dirty, setDraft]);
}
