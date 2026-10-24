import { createContext, useContext, useEffect, useRef } from 'react';
import type { ReactNode } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, APIError, setCSRF } from '../lib/api';
import type { Identity } from '../lib/types';
const SessionContext = createContext<Identity | null>(null);
export const useIdentity = () => {
  const value = useContext(SessionContext);
  if (!value) throw new Error('Session required');
  return value;
};
export const can = (identity: Identity, capability: string) =>
  identity.level === 'admin' || identity.capabilities.includes(capability);
export function SessionProvider({
  identity,
  children,
}: {
  identity: Identity;
  children: ReactNode;
}) {
  setCSRF(identity.csrf);
  return <SessionContext.Provider value={identity}>{children}</SessionContext.Provider>;
}
export function useSession() {
  const client = useQueryClient();
  const migrationTried = useRef(false);
  useEffect(() => {
    const expire = () => {
      setCSRF('');
      void client.cancelQueries().then(() => {
        client.removeQueries({ predicate: (q) => q.queryKey[0] !== 'identity' });
        client.setQueryData(['identity'], null);
      });
    };
    window.addEventListener('panel-session-expired', expire);
    return () => window.removeEventListener('panel-session-expired', expire);
  }, [client]);
  return useQuery({
    queryKey: ['identity'],
    queryFn: async ({ signal }) => {
      try {
        return await api<Identity>('whoami', { signal, notifyUnauthorized: false });
      } catch (error) {
        if (!(error instanceof APIError) || error.status !== 401) throw error;
        if (client.getQueryData(['identity'])) {
          window.dispatchEvent(new Event('panel-session-expired'));
          throw error;
        }
        if (migrationTried.current) throw error;
        migrationTried.current = true;
        const mode = await api<{ mode: string }>('auth/mode', { signal });
        if (mode.mode !== 'legacy') throw error;
        await api('migrate', { body: {}, signal, notifyUnauthorized: false });
        return api<Identity>('whoami', { signal, notifyUnauthorized: false });
      }
    },
    retry: false,
    staleTime: 60000,
  });
}
