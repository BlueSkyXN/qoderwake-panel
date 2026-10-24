import React, { lazy } from 'react';
import ReactDOM from 'react-dom/client';
import { createHashRouter, Navigate, RouterProvider } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from './App';
import '../../panel/static/panel.css';
import '../../panel/static/workbench.css';
import './style.css';
const Overview = lazy(() => import('./pages/Overview').then((m) => ({ default: m.Overview })));
const Skills = lazy(() => import('./pages/Skills').then((m) => ({ default: m.Skills })));
const Wakers = lazy(() => import('./pages/Wakers').then((m) => ({ default: m.Wakers })));
const Users = lazy(() => import('./pages/Users').then((m) => ({ default: m.Users })));
const Account = lazy(() => import('./pages/Users').then((m) => ({ default: m.Account })));
const Audit = lazy(() => import('./pages/Audit').then((m) => ({ default: m.Audit })));
const Native = lazy(() => import('./pages/Native').then((m) => ({ default: m.Native })));
const Models = lazy(() => import('./pages/Models').then((m) => ({ default: m.Models })));
const Sessions = lazy(() => import('./pages/Sessions').then((m) => ({ default: m.Sessions })));
const Memory = lazy(() => import('./pages/Memory').then((m) => ({ default: m.Memory })));
const Chat = lazy(() => import('./pages/Chat').then((m) => ({ default: m.Chat })));
const Gateway = lazy(() => import('./pages/Chat').then((m) => ({ default: m.Gateway })));
const Callers = lazy(() => import('./pages/Callers').then((m) => ({ default: m.Callers })));
const Operations = lazy(() =>
  import('./pages/Operations').then((m) => ({ default: m.Operations })),
);
const Channels = lazy(() => import('./pages/Channels').then((m) => ({ default: m.Channels })));
const Plugins = lazy(() => import('./pages/Plugins').then((m) => ({ default: m.Plugins })));
const Usage = lazy(() => import('./pages/Usage').then((m) => ({ default: m.Usage })));
const Security = lazy(() => import('./pages/Security').then((m) => ({ default: m.Security })));
const Controls = lazy(() => import('./pages/Controls').then((m) => ({ default: m.Controls })));
const Network = lazy(() => import('./pages/Network').then((m) => ({ default: m.Network })));
const initialURL = new URL(location.href);
if (initialURL.searchParams.has('token')) {
  initialURL.searchParams.delete('token');
  history.replaceState(null, '', initialURL.pathname + initialURL.search + initialURL.hash);
}
const client = new QueryClient({
  defaultOptions: {
    queries: { retry: false, staleTime: 15000, refetchOnWindowFocus: false },
    mutations: { retry: false },
  },
});
const router = createHashRouter([
  {
    element: <App />,
    children: [
      { index: true, element: <Navigate to="/overview" replace /> },
      { path: 'overview', element: <Overview /> },
      { path: 'skills', element: <Skills /> },
      { path: 'wakers', element: <Wakers /> },
      { path: 'models', element: <Models /> },
      { path: 'sessions', element: <Sessions /> },
      { path: 'memory', element: <Memory /> },
      { path: 'chat', element: <Chat /> },
      { path: 'gateway', element: <Gateway /> },
      { path: 'callers', element: <Callers /> },
      { path: 'operations', element: <Operations /> },
      { path: 'channels', element: <Channels /> },
      { path: 'plugins', element: <Plugins /> },
      { path: 'usage', element: <Usage /> },
      { path: 'security', element: <Security /> },
      { path: 'controls', element: <Controls /> },
      { path: 'net', element: <Network /> },
      { path: 'users', element: <Users /> },
      { path: 'audit', element: <Audit /> },
      { path: 'native', element: <Native /> },
      { path: 'account', element: <Account /> },
      { path: '*', element: <Navigate to="/overview" replace /> },
    ],
  },
]);
ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </React.StrictMode>,
);
