import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { api, query } from '../lib/api';
import { useIdentity } from '../context/session';
import { usePanelState, Pager, WakerPicker } from '../components/resources';
import type { PanelState } from '../lib/types';
import {
  Card,
  Empty,
  ErrorNotice,
  Field,
  JSONDetails,
  Loading,
  Table,
  useConfirm,
  useToast,
} from '../components/ui';
interface Plugin {
  id?: string;
  pluginId?: string;
  name?: string;
  description?: string;
  version?: string;
  latestVersion?: string;
  enabled?: boolean;
}
interface Catalog {
  items?: Plugin[];
  plugins?: Plugin[];
  total?: number;
}
const pluginID = (row: Plugin) => row.pluginId || row.id || '';
const entries = (value: Catalog | undefined) => value?.items || value?.plugins || [];
export function Plugins() {
  const identity = useIdentity();
  const state = usePanelState();
  const admin = identity.level === 'admin';
  const viewer = identity.level === 'viewer';
  const [term, setTerm] = useState('');
  const [keyword, setKeyword] = useState('');
  const [page, setPage] = useState(1);
  const [selected, setSelected] = useState<Plugin | null>(null);
  const catalog = useQuery({
    queryKey: ['plugin-catalog', keyword, page],
    queryFn: ({ signal }) =>
      api<Catalog>('plugins/catalog?' + query({ keyword, page }), { signal }),
    enabled: admin,
  });
  const installed = useQuery({
    queryKey: ['plugins-installed'],
    queryFn: ({ signal }) => api<Catalog>('plugins/installed', { signal }),
    enabled: admin,
  });
  const summary = useQuery({
    queryKey: ['plugin-summary'],
    queryFn: ({ signal }) => api<{ featured: Catalog; installed: Catalog }>('plugins', { signal }),
    enabled: viewer,
  });
  if (!admin && !viewer) return <Empty>当前账号没有此页面的访问权限。</Empty>;
  if (viewer)
    return (
      <>
        <Card title="精选插件">
          {summary.isPending ? (
            <Loading />
          ) : summary.error ? (
            <ErrorNotice error={summary.error} />
          ) : (
            <PluginTable rows={entries(summary.data?.featured)} />
          )}
        </Card>
        <Card title="已安装">
          <PluginTable rows={entries(summary.data?.installed)} />
        </Card>
      </>
    );
  return (
    <>
      <Card title="已安装插件">
        {installed.isPending ? (
          <Loading />
        ) : installed.error ? (
          <ErrorNotice error={installed.error} retry={() => void installed.refetch()} />
        ) : (
          <PluginTable rows={entries(installed.data)} />
        )}
      </Card>
      <Card title="官方插件市场">
        <form
          className="row"
          onSubmit={(e) => {
            e.preventDefault();
            setKeyword(term);
            setPage(1);
            setSelected(null);
          }}
        >
          <Field label="关键词">
            <input
              type="search"
              value={term}
              maxLength={120}
              onChange={(e) => setTerm(e.target.value)}
            />
          </Field>
          <button>搜索</button>
        </form>
        {catalog.isPending ? (
          <Loading />
        ) : catalog.error ? (
          <ErrorNotice error={catalog.error} retry={() => void catalog.refetch()} />
        ) : entries(catalog.data).length ? (
          <Table head={['名称', '说明', '操作']}>
            {entries(catalog.data).map((row) => (
              <tr key={pluginID(row)}>
                <td>{row.name || pluginID(row)}</td>
                <td>{row.description || '—'}</td>
                <td>
                  <button onClick={() => setSelected(row)}>
                    {state.data?.pluginWritesEnabled ? '详情与安装' : '查看详情'}
                  </button>
                </td>
              </tr>
            ))}
          </Table>
        ) : (
          <Empty>{keyword ? '没有匹配的插件，请更换关键词。' : '暂无插件。'}</Empty>
        )}
        <Pager
          page={page}
          more={
            typeof catalog.data?.total === 'number'
              ? page * 20 < catalog.data.total
              : entries(catalog.data).length === 20
          }
          busy={catalog.isFetching}
          onChange={(value) => {
            setPage(value);
            setSelected(null);
          }}
        />
      </Card>
      {selected && state.data && (
        <PluginDetail key={pluginID(selected)} row={selected} state={state.data} />
      )}
      <ErrorNotice error={state.error} />
    </>
  );
}
function PluginTable({ rows }: { rows: Plugin[] }) {
  return rows.length ? (
    <Table head={['名称', '版本', '状态']}>
      {rows.map((row) => (
        <tr key={pluginID(row)}>
          <td>{row.name || pluginID(row)}</td>
          <td>{row.version || row.latestVersion || '未提供'}</td>
          <td>{row.enabled === false ? '已停用' : row.enabled === true ? '已启用' : '未提供'}</td>
        </tr>
      ))}
    </Table>
  ) : (
    <Empty>暂无插件。</Empty>
  );
}
function PluginDetail({ row, state }: { row: Plugin; state: PanelState }) {
  const id = pluginID(row);
  const client = useQueryClient();
  const confirm = useConfirm();
  const toast = useToast();
  const [chosen, setChosen] = useState('');
  const wid = chosen || state.wakers[0]?.id || '';
  const detail = useQuery({
    queryKey: ['plugin-detail', id],
    queryFn: ({ signal }) =>
      api<Plugin & { plugin?: Plugin }>('plugins/detail?' + query({ id }), { signal }),
  });
  const installs = useQuery({
    queryKey: ['plugin-installations', id],
    queryFn: ({ signal }) => api<unknown>('plugins/installations?' + query({ id }), { signal }),
  });
  const info = detail.data?.plugin || detail.data || row;
  const version = info.version || info.latestVersion || row.version || row.latestVersion;
  const action = useMutation({
    mutationFn: async ({
      kind,
      enabled,
    }: {
      kind: 'install' | 'remove' | 'toggle';
      enabled?: boolean;
    }) => {
      const label =
        kind === 'install' ? '安装' : kind === 'remove' ? '卸载' : enabled ? '启用' : '停用';
      const target = state.wakers.find((w) => w.id === wid)?.name || wid;
      if (
        !(await confirm(
          label +
            ' ' +
            (info.name || id) +
            ' 到 ' +
            target +
            '？' +
            (kind === 'install'
              ? '安装会下载并运行第三方代码，并可能向官方报告安装状态。'
              : kind === 'remove'
                ? '相关能力将从此机器人中移除。'
                : ''),
        ))
      )
        return;
      await api('plugins/' + kind, {
        body: {
          pluginId: id,
          wakerId: wid,
          expectedVersion: version,
          enabled,
          confirm: '/api/plugins/' + kind,
        },
      });
      toast('插件已更新');
      await client.invalidateQueries({ queryKey: ['plugins-installed'] });
      await client.invalidateQueries({ queryKey: ['plugin-installations', id] });
      await client.invalidateQueries({ queryKey: ['audit'] });
    },
  });
  if (detail.isPending) return <Loading />;
  if (detail.error) return <ErrorNotice error={detail.error} retry={() => void detail.refetch()} />;
  return (
    <Card title={info.name || id}>
      <p>{info.description || row.description}</p>
      <p>版本：{version || '未提供'}</p>
      <WakerPicker
        items={state.wakers}
        value={wid}
        onChange={setChosen}
        disabled={action.isPending}
      />
      <div className="row">
        <button
          className="primary"
          disabled={
            !state.pluginWritesEnabled ||
            !wid ||
            typeof version !== 'string' ||
            !version ||
            action.isPending
          }
          onClick={() => action.mutate({ kind: 'install' })}
        >
          安装此版本
        </button>
        <button
          disabled={!state.pluginWritesEnabled || !wid || action.isPending}
          onClick={() => action.mutate({ kind: 'remove' })}
        >
          卸载
        </button>
        <button
          disabled={!state.pluginWritesEnabled || !wid || action.isPending}
          onClick={() => action.mutate({ kind: 'toggle', enabled: true })}
        >
          启用
        </button>
        <button
          disabled={!state.pluginWritesEnabled || !wid || action.isPending}
          onClick={() => action.mutate({ kind: 'toggle', enabled: false })}
        >
          停用
        </button>
      </div>
      {!state.pluginWritesEnabled && (
        <p className="fine muted">
          此页面的插件修改未开放，可在<Link to="/native">官方操作</Link>中管理。
        </p>
      )}
      <ErrorNotice error={action.error || installs.error} />
      {installs.data != null && <JSONDetails value={installs.data} label="安装状态" />}
    </Card>
  );
}
