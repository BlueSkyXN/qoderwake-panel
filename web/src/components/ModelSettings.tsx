import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, APIError, query } from '../lib/api';
import type { ProviderConfig, SaveResult } from '../lib/model-settings';
import { saveMessages } from '../lib/model-settings';
import { useDirtyGuard } from '../context/drafts';
import { Card, ErrorNotice, Field, JSONDetails, Loading, useConfirm, useToast } from './ui';

export function NativeBYOK() {
  const state = useQuery({
    queryKey: ['native-byok'],
    queryFn: ({ signal }) =>
      api<{ status: string; code?: string; overview?: unknown }>('models/native-byok', { signal }),
  });
  const labels: Record<string, string> = {
    available: '已读取原生配置',
    local_only: '仅允许在 Wake 本机界面管理',
    unavailable: '暂时无法读取原生配置',
    unsupported: '当前运行时未提供原生 BYOK 接口',
  };
  return (
    <Card
      title="Wake 原生 BYOK"
      actions={
        <button disabled={state.isFetching} onClick={() => void state.refetch()}>
          重新检查
        </button>
      }
    >
      {state.isPending ? (
        <Loading />
      ) : (
        state.data && (
          <>
            <p>
              <strong>{labels[state.data.status] || '状态未知'}</strong>
            </p>
            <p>请在运行 Wake 的电脑上打开官方客户端，在模型设置中管理原生 BYOK。</p>
            {state.data.code && (
              <details>
                <summary>接口状态</summary>
                <code>{state.data.code}</code>
              </details>
            )}
            {state.data.overview !== undefined && (
              <JSONDetails value={state.data.overview} label="原生配置概览" />
            )}
          </>
        )
      )}
      <ErrorNotice error={state.error} />
    </Card>
  );
}

export function ProviderJSONLoader({ name, close }: { name: string; close: () => void }) {
  const data = useQuery({
    queryKey: ['provider-config', name],
    queryFn: ({ signal }) => api<ProviderConfig>('provider/config?' + query({ name }), { signal }),
    staleTime: 0,
  });
  if (data.isPending) return <Loading />;
  if (data.error) return <ErrorNotice error={data.error} retry={() => void data.refetch()} />;
  return <ProviderJSONEditor initial={data.data!} close={close} />;
}

function ProviderJSONEditor({ initial, close }: { initial: ProviderConfig; close: () => void }) {
  const [baseline, setBaseline] = useState(initial);
  const [text, setText] = useState(initial.configJSON);
  const [keyAction, setKeyAction] = useState('keep');
  const [key, setKey] = useState('');
  const [showKey, setShowKey] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [status, setStatus] = useState('');
  const [history, setHistory] = useState(false);
  const [loadError, setLoadError] = useState<unknown>(null);
  const dirty = text !== baseline.configJSON || keyAction !== 'keep';
  useDirtyGuard(dirty);
  const confirm = useConfirm();
  const toast = useToast();
  const client = useQueryClient();
  function adopt(value: ProviderConfig) {
    setBaseline(value);
    setText(value.configJSON);
    setKey('');
    setKeyAction('keep');
    setShowKey(false);
    setBlocked(false);
    setHistory(false);
  }
  const save = useMutation({
    mutationFn: async () => {
      try {
        JSON.parse(text);
      } catch {
        throw new Error('JSON 格式有误，请修正后保存。');
      }
      return api<SaveResult>('provider/config', {
        body: {
          name: initial.name,
          configJSON: text,
          keyAction,
          apiKey: keyAction === 'replace' ? key : '',
          baseRevision: baseline.baseRevision,
          acknowledgeHistory: history,
          confirm: '/api/provider/config',
        },
      });
    },
    onSuccess: async (result) => {
      setKey('');
      setShowKey(false);
      setStatus(saveMessages[result.status]);
      if (result.status === 'verified' && result.current) {
        adopt(result.current);
        toast('明文配置已保存并回读');
      } else setBlocked(true);
      await client.invalidateQueries({ queryKey: ['state'] });
      await client.invalidateQueries({ queryKey: ['audit'] });
    },
    onError: (error) => {
      if (error instanceof APIError && error.code === 'version_conflict') setBlocked(true);
    },
  });
  async function reload() {
    if (dirty && !(await confirm('放弃当前草稿，重新读取已保存的明文配置？'))) return;
    try {
      adopt(await api<ProviderConfig>('provider/config?' + query({ name: initial.name })));
      setLoadError(null);
      save.reset();
      setStatus('已读取最新配置。');
    } catch (error) {
      setLoadError(error);
    }
  }
  return (
    <Card title={'编辑明文配置 · ' + initial.name}>
      <p className="muted">
        保存到 qodercli/settings.json。API Key 通过下方选项保留、更换或删除；JSON
        中的密钥占位符请保持原样。
      </p>
      <form
        onSubmit={async (event) => {
          event.preventDefault();
          if (
            await confirm(
              keyAction === 'delete'
                ? '保存此配置并删除 API Key？使用此连接的模型可能无法继续调用。'
                : '将此 Provider 的 JSON 修改保存到明文配置文件？',
            )
          )
            save.mutate();
        }}
      >
        <fieldset disabled={save.isPending}>
          <Field label="Provider JSON">
            <textarea
              className="model-json"
              spellCheck={false}
              value={text}
              onChange={(e) => setText(e.target.value)}
            />
          </Field>
          <div className="form-grid">
            <Field label="API Key">
              <select
                value={keyAction}
                onChange={(e) => {
                  setKeyAction(e.target.value);
                  setKey('');
                  setShowKey(false);
                }}
              >
                <option value="keep">保留当前值</option>
                <option value="replace">更换</option>
                <option value="delete">删除</option>
              </select>
            </Field>
            {keyAction === 'replace' && (
              <Field label="新 API Key">
                <input
                  type={showKey ? 'text' : 'password'}
                  autoComplete="new-password"
                  value={key}
                  required
                  onChange={(e) => setKey(e.target.value)}
                />
                <button type="button" aria-pressed={showKey} onClick={() => setShowKey((x) => !x)}>
                  {showKey ? '隐藏输入' : '显示输入'}
                </button>
              </Field>
            )}
          </div>
          {save.error instanceof APIError &&
            save.error.code === 'provider_model_history_confirmation' && (
              <label className="checkbox-row">
                <input
                  type="checkbox"
                  checked={history}
                  onChange={(e) => setHistory(e.target.checked)}
                />
                确认移除这些模型；历史会话的模型记录会保留
              </label>
            )}
          <div className="form-actions">
            <button className="primary" disabled={!dirty || blocked}>
              保存明文配置
            </button>
            <button type="button" onClick={() => void reload()}>
              读取最新配置
            </button>
            <button
              type="button"
              onClick={async () => {
                if (!dirty || (await confirm('放弃未保存的配置修改？'))) close();
              }}
            >
              关闭
            </button>
          </div>
        </fieldset>
      </form>
      {status && <p role="status">{status}</p>}
      <ErrorNotice error={save.error || loadError} />
      {blocked && <p>请先读取最新配置，再决定是否继续编辑。</p>}
    </Card>
  );
}
