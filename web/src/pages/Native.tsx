import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { api, query } from '../lib/api';
import { errors } from '../lib/messages';
import { requestID } from '../lib/ids';
import { nativeContextLabels, nativeLabels } from '../lib/native-labels';
import {
  isFile,
  fileParameter,
  matches,
  optionActive,
  parameters,
  section,
  statusLabel,
  title,
} from '../lib/native';
import type { FieldValue, FileSlot, Job, JobOutput, Operation, Option, Spec } from '../lib/native';
import type { PanelState, SkillContent, WakerDetail } from '../lib/types';
import { useIdentity } from '../context/session';
import { useDirtyGuard, useDraftState } from '../context/drafts';
import { useDeletion } from '../components/Deletion';
import {
  Card,
  Empty,
  ErrorNotice,
  Field,
  Loading,
  Table,
  useConfirm,
  useToast,
} from '../components/ui';
export function Native() {
  const [search, setSearch] = useState('');
  const [group, setGroup] = useState('');
  const [selected, setSelected] = useState('');
  const [job, setJob] = useState('');
  const confirm = useConfirm();
  const { dirty } = useDraftState();
  const catalog = useQuery({
    queryKey: ['native-catalog'],
    queryFn: ({ signal }) =>
      api<{ referenceVersion: string; items: Operation[] }>('native/catalog', { signal }),
  });
  const jobs = useQuery({
    queryKey: ['native-jobs'],
    queryFn: ({ signal }) => api<{ items: Job[] }>('native/jobs', { signal }),
  });
  const rows =
    catalog.data?.items.filter((s) => (!group || s.group === group) && matches(s, search)) || [];
  const groups = [...new Map(catalog.data?.items.map((s) => [s.group, s.groupName])).entries()];
  if (catalog.isPending) return <Loading />;
  if (catalog.error)
    return <ErrorNotice error={catalog.error} retry={() => void catalog.refetch()} />;
  return (
    <>
      <div className="native-workspace">
        <aside className="native-catalog">
          <Field label="查找官方操作">
            <input
              type="search"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="例如：新增 MCP、mcp add"
            />
          </Field>
          <Field label="功能分组">
            <select value={group} onChange={(e) => setGroup(e.target.value)}>
              <option value="">全部功能</option>
              {groups.map(([id, name]) => (
                <option key={id} value={id}>
                  {name}
                </option>
              ))}
            </select>
          </Field>
          <p className="fine muted">
            {rows.length} 项匹配 · 共 {catalog.data?.items.length} 项
          </p>
          <div className="native-list">
            {rows.map((spec) => (
              <button
                key={spec.id}
                className="native-operation"
                aria-current={selected === spec.id}
                onClick={async () => {
                  if (selected === spec.id) return;
                  if (dirty && !(await confirm('当前表单有尚未执行的参数，放弃并切换操作？')))
                    return;
                  setSelected(spec.id);
                }}
              >
                <span className="native-operation-title">
                  <strong>{title(spec)}</strong>
                  <span
                    className={
                      'operation-kind' +
                      (['delete', 'remove', 'reset', 'clear', 'uninstall'].includes(
                        spec.path.at(-1) || '',
                      )
                        ? ' danger'
                        : '')
                    }
                  >
                    {spec.effect === 'read' ? '读取' : '修改'}
                  </span>
                </span>
                <code>{spec.path.join(' ')}</code>
              </button>
            ))}
            {!rows.length && <Empty>没有匹配的操作。</Empty>}
          </div>
        </aside>
        <section className="native-stage">
          {selected ? (
            <OperationLoader key={selected} id={selected} onJob={setJob} />
          ) : (
            <Empty>选择一项操作，查看参数与官方帮助。</Empty>
          )}
        </section>
      </div>
      {job && <JobView key={job} id={job} />}
      <details className="native-history">
        <summary>我的操作记录 · {jobs.data?.items.length || 0}</summary>
        <ErrorNotice error={jobs.error} />
        {jobs.data?.items.length ? (
          <Table head={['操作', '时间', '结果', '']}>
            {jobs.data.items.map((item) => (
              <tr key={item.id}>
                <td className="mono">{item.operation}</td>
                <td>{new Date(item.created * 1000).toLocaleString()}</td>
                <td>{statusLabel(item.status)}</td>
                <td>
                  <button onClick={() => setJob(item.id)}>查看结果</button>
                </td>
              </tr>
            ))}
          </Table>
        ) : (
          <Empty>尚未执行操作。</Empty>
        )}
      </details>
    </>
  );
}
function OperationLoader({ id, onJob }: { id: string; onJob: (id: string) => void }) {
  const spec = useQuery({
    queryKey: ['native-spec', id],
    queryFn: ({ signal }) => api<Spec>('native/operation?' + query({ id }), { signal }),
  });
  if (spec.isPending) return <Loading />;
  if (spec.error) return <ErrorNotice error={spec.error} retry={() => void spec.refetch()} />;
  return <OperationForm spec={spec.data!} onJob={onJob} />;
}
function OperationForm({ spec, onJob }: { spec: Spec; onJob: (id: string) => void }) {
  const deleteResource = useDeletion();
  const identity = useIdentity();
  const client = useQueryClient();
  const confirm = useConfirm();
  const toast = useToast();
  const state = useQuery({
    queryKey: ['state'],
    queryFn: ({ signal }) => api<PanelState>('state', { signal }),
  });
  const [args, setArgs] = useState<Record<string, string>>({});
  const [fields, setFields] = useState<Record<string, FieldValue>>({});
  const [mode, setMode] = useState('fields');
  const [transport, setTransport] = useState('');
  const [dirty, setDirty] = useState(false);
  const [guard, setGuard] = useState<Record<string, string>>({});
  const [versionError, setVersionError] = useState<unknown>(null);
  const [timeout, setTimeout] = useState(300);
  const [interactive, setInteractive] = useState(false);
  const [stdin, setStdin] = useState('');
  const [context, setContext] = useState<Record<string, string>>({});
  const request = useRef<{ body: string; key: string } | null>(null);
  useDirtyGuard(dirty);
  useEffect(() => {
    if (identity.level !== 'admin' && state.data?.wakers.length) {
      setFields((old) =>
        old['--waker-id']
          ? old
          : { ...old, '--waker-id': { value: state.data!.wakers[0].id, enabled: true } },
      );
    }
  }, [identity.level, state.data]);
  function update(name: string, value: FieldValue) {
    setFields((old) => ({ ...old, [name]: value }));
    setDirty(true);
    setGuard({});
    if (name === '--transport') setTransport(value.value);
  }
  function values() {
    return parameters(spec, args, fields, mode, transport);
  }
  async function readVersion() {
    setVersionError(null);
    try {
      const options = values().options;
      if (spec.id === 'skill.manage' && options['--conversation-id']) {
        if (typeof options['--base-version-id'] !== 'string' || !options['--base-version-id'])
          throw new Error('群技能请填写读取时的版本');
        setGuard({ baseVersionId: options['--base-version-id'] });
        return;
      }
      const wid = String(options['--waker-id'] || '');
      if (!wid) throw new Error('先选择 Waker');
      if (spec.id.startsWith('waker.update.')) {
        const current = await api<WakerDetail>('waker/detail?' + query({ id: wid }));
        setGuard({ baseRevision: current.configRevision });
      } else {
        const sid = String(options['--skill-id'] || '');
        if (!sid) throw new Error('先填写技能 ID；创建新技能可直接提交');
        const current = await api<SkillContent>(
          'skill/content?' + query({ waker: wid, skill: sid }),
        );
        const version = current.skill.currentVersionId;
        if (!version) throw new Error('未取得当前版本');
        setGuard({ baseVersionId: version });
        if (spec.options.some((o) => o.name === '--base-version-id'))
          setFields((old) => ({ ...old, '--base-version-id': { enabled: true, value: version } }));
      }
    } catch (error) {
      setVersionError(error);
    }
  }
  const execute = useMutation({
    mutationFn: async () => {
      const params = values();
      if (spec.id.startsWith('waker.update.') && !guard.baseRevision)
        throw new Error('请先读取当前版本');
      const creating = spec.id === 'skill.manage' && params.options['--action'] === 'create';
      const groupVersion =
        spec.id === 'skill.manage' &&
        params.options['--conversation-id'] &&
        params.options['--base-version-id'];
      if (
        ['skill.manage', 'skill.delete', 'skill.self-evolution', 'skill.rollback'].includes(
          spec.id,
        ) &&
        params.options['--skill-id'] &&
        !creating &&
        !guard.baseVersionId &&
        !groupVersion
      )
        throw new Error('请先读取技能当前版本');
      if (spec.id === 'waker.delete') {
        const target = String(params.options['--waker-id'] || '');
        if (!target) throw new Error('先选择 Waker');
        deleteResource({ kind: 'waker', target, onDeleted: () => setDirty(false) });
        return;
      }
      if (spec.id === 'skill.rollback') {
        if (!(await confirm('将所选技能恢复到指定版本？'))) return;
        await api('skill/rollback', {
          body: {
            wakerId: params.options['--waker-id'],
            skillId: params.options['--skill-id'],
            versionId: params.options['--to'],
            reason: params.options['--reason'] || '从官方操作页恢复',
            ...guard,
            confirm: '/api/skill/rollback',
          },
        });
        setDirty(false);
        setGuard({});
        toast('技能版本已恢复');
        return;
      }
      const body = {
        operation: spec.id,
        revision: spec.revision,
        parameters: params,
        ...guard,
        timeout,
        interactive,
        stdin,
        context,
        ...(spec.effect === 'read' ? {} : { confirm: spec.id }),
      };
      if (
        spec.effect !== 'read' &&
        !(await confirm(
          '执行 ' +
            spec.path.join(' ') +
            '？请核对目标与参数。此操作可能修改数据、运行程序或访问外部服务。',
        ))
      )
        return;
      const encoded = JSON.stringify(body);
      if (!request.current || request.current.body !== encoded)
        request.current = { body: encoded, key: requestID() };
      const job = await api<Job>('native/execute', {
        body,
        headers: { 'Idempotency-Key': request.current.key },
      });
      request.current = null;
      setDirty(false);
      setGuard({});
      setStdin('');
      setContext((old) =>
        Object.fromEntries(Object.entries(old).filter(([key]) => !key.includes('TOKEN'))),
      );
      setFields((old) =>
        Object.fromEntries(
          Object.entries(old).map(([key, value]) => [
            key,
            spec.options.find((o) => o.name === key)?.secret
              ? { ...value, value: '', enabled: false }
              : value,
          ]),
        ),
      );
      onJob(job.id);
      await client.invalidateQueries({ queryKey: ['native-jobs'] });
    },
  });
  const guarded =
    spec.id.startsWith('waker.update.') ||
    ['skill.manage', 'skill.delete', 'skill.self-evolution', 'skill.rollback'].includes(spec.id);
  const renderOption = (field: Option) => (
    <OptionField
      key={field.name}
      field={
        spec.id === 'mcp.add' && field.name === '--transport'
          ? { ...field, choices: ['http', 'sse', 'stdio'] }
          : field
      }
      value={fields[field.name] || { value: '', enabled: false }}
      onChange={(value) => update(field.name, value)}
      wakers={state.data?.wakers || []}
      scoped={identity.level !== 'admin'}
      admin={identity.level === 'admin'}
    />
  );
  return (
    <>
      <header className="native-editor-heading">
        <h2>{title(spec)}</h2>
        <code>qoderwake {spec.path.join(' ')}</code>
        {spec.panelPage && (
          <Link className="text-link" to={'/' + spec.panelPage}>
            常用管理页面
          </Link>
        )}
        {!spec.available && (
          <p className="error-text">
            {errors[spec.error || ''] || '当前 CLI 无法执行此操作，请查看当前版本的帮助。'}
          </p>
        )}
      </header>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          execute.mutate();
        }}
      >
        <fieldset disabled={execute.isPending}>
          <div className="native-form-body">
            {spec.id === 'mcp.add' && (
              <div className="native-mode">
                <Field label="添加方式">
                  <select
                    value={mode}
                    onChange={(e) => {
                      setMode(e.target.value);
                      setDirty(true);
                    }}
                  >
                    <option value="fields">填写配置</option>
                    <option value="json">导入 JSON</option>
                    <option value="file">导入 JSON 文件</option>
                    <option value="all">全部参数</option>
                  </select>
                </Field>
                <p>参数右侧的勾选框控制是否传递，包括空值。</p>
              </div>
            )}
            {spec.arguments.length > 0 && (
              <section className="native-fields">
                <h3>位置参数</h3>
                {spec.arguments.map((field) => (
                  <div key={field.name}>
                    <Field
                      key={field.name}
                      label={field.name + (field.required ? ' *' : '')}
                      hint={field.description}
                    >
                      <textarea
                        value={args[field.name] || ''}
                        required={field.required}
                        rows={field.variadic ? 3 : 1}
                        onChange={(e) => {
                          setArgs((old) => ({ ...old, [field.name]: e.target.value }));
                          setDirty(true);
                          setGuard({});
                        }}
                      />
                    </Field>
                    {identity.level === 'admin' && isFile(field) && (
                      <FileControls
                        value={args[field.name] || ''}
                        field={field}
                        onChange={(value) => {
                          setArgs((old) => ({ ...old, [field.name]: value }));
                          setDirty(true);
                          setGuard({});
                        }}
                      />
                    )}
                  </div>
                ))}
              </section>
            )}
            {['basic', 'connection', 'import', 'auth', 'version', 'advanced'].map((group) => {
              const options = spec.options.filter(
                (field) =>
                  section(field, spec) === group &&
                  (spec.id !== 'mcp.add' || optionActive(field.name, mode, transport)),
              );
              if (!options.length) return null;
              const label = (
                {
                  basic: '基本参数',
                  connection: '连接设置',
                  import: '导入配置',
                  auth: '请求头与认证',
                  version: '版本与幂等',
                  advanced: '高级参数',
                } as Record<string, string>
              )[group];
              return ['auth', 'version', 'advanced'].includes(group) ? (
                <details key={group} className="native-option-group">
                  <summary>{label}</summary>
                  <div className="form-grid">{options.map(renderOption)}</div>
                </details>
              ) : (
                <section key={group} className="native-option-group">
                  <h3>{label}</h3>
                  <div className="form-grid">{options.map(renderOption)}</div>
                </section>
              );
            })}
            {guarded && (
              <div className="row">
                <button type="button" onClick={() => void readVersion()}>
                  读取当前版本
                </button>
                <span className="mono muted">
                  {guard.baseRevision || guard.baseVersionId || '填写 Waker / 技能后读取'}
                </span>
              </div>
            )}
            <ErrorNotice error={versionError} />
            {!spec.panelOnly && (
              <details className="native-execution">
                <summary>执行设置</summary>
                <Field label="最长等待时间（秒）">
                  <input
                    type="number"
                    min={10}
                    max={86400}
                    value={timeout}
                    onChange={(e) => {
                      setTimeout(Number(e.target.value));
                      setDirty(true);
                    }}
                  />
                </Field>
                {identity.level === 'admin' && (
                  <>
                    <label className="check-row">
                      <input
                        type="checkbox"
                        checked={interactive}
                        onChange={(e) => {
                          setInteractive(e.target.checked);
                          setDirty(true);
                        }}
                      />
                      使用交互终端
                    </label>
                    <Field label="标准输入">
                      <textarea
                        value={stdin}
                        disabled={interactive}
                        onChange={(e) => {
                          setStdin(e.target.value);
                          setDirty(true);
                        }}
                        autoComplete="off"
                      />
                    </Field>
                    <details>
                      <summary>当前 Run 上下文</summary>
                      <p>命令要求现有运行身份时，填写该 Run 的标识与凭据。</p>
                      {spec.contextKeys.map((key) => (
                        <Field key={key} label={nativeContextLabels[key] || key}>
                          <input
                            value={context[key] || ''}
                            type={key.includes('TOKEN') ? 'password' : 'text'}
                            autoComplete="off"
                            onChange={(e) => {
                              setContext((old) => ({ ...old, [key]: e.target.value }));
                              setDirty(true);
                            }}
                          />
                        </Field>
                      ))}
                    </details>
                  </>
                )}
              </details>
            )}
            <ErrorNotice error={execute.error} />
          </div>
          <div className="native-submit-bar">
            <span>{spec.effect === 'read' ? '读取当前资源' : '执行前将确认目标与参数'}</span>
            <button className="primary" disabled={!spec.available}>
              {execute.isPending ? '正在提交…' : spec.effect === 'read' ? '读取' : '检查并执行'}
            </button>
          </div>
        </fieldset>
      </form>
      <details className="native-reference">
        <summary>官方参数参考</summary>
        <pre className="code">{spec.help}</pre>
        {spec.currentHelp && (
          <>
            <h3>当前版本的帮助</h3>
            <pre className="code">{spec.currentHelp}</pre>
          </>
        )}
      </details>
      {identity.level === 'admin' && <FilesList />}
    </>
  );
}
function OptionField({
  field,
  value,
  onChange,
  wakers,
  scoped,
  admin,
}: {
  field: Option;
  value: FieldValue;
  onChange: (v: FieldValue) => void;
  wakers: PanelState['wakers'];
  scoped: boolean;
  admin: boolean;
}) {
  const label = nativeLabels[field.name] || field.name;
  if (!field.value)
    return (
      <label className="check-row">
        <input
          type="checkbox"
          checked={value.enabled}
          onChange={(e) => onChange({ ...value, enabled: e.target.checked })}
        />
        <span>
          {label}
          {label !== field.name && <small className="mono muted"> {field.name}</small>}
        </span>
      </label>
    );
  return (
    <div className="native-field">
      <div className="native-field-label">
        <label>
          {label}
          {label !== field.name && <code>{field.name}</code>}
        </label>
        <label className="inline-check">
          <input
            type="checkbox"
            aria-label={'使用 ' + label + ' 参数'}
            checked={value.enabled}
            disabled={scoped && field.name === '--waker-id'}
            onChange={(e) => onChange({ ...value, enabled: e.target.checked })}
          />
          使用
        </label>
      </div>
      {field.name === '--waker-id' ? (
        <select
          aria-label={label}
          value={value.value}
          required={scoped}
          onChange={(e) => onChange({ value: e.target.value, enabled: true })}
        >
          <option value="">选择 Waker</option>
          {wakers.map((w) => (
            <option key={w.id} value={w.id}>
              {w.name}
            </option>
          ))}
        </select>
      ) : field.choices.length ? (
        <select
          aria-label={label}
          value={value.value}
          onChange={(e) => onChange({ value: e.target.value, enabled: true })}
        >
          <option value="">选择参数值</option>
          {field.choices.map((v) => (
            <option key={v} value={v}>
              {field.name === '--transport'
                ? (
                    { http: 'HTTP', sse: 'SSE', stdio: 'stdio · 本地命令' } as Record<
                      string,
                      string
                    >
                  )[v] || v
                : v}
            </option>
          ))}
        </select>
      ) : field.secret ? (
        <input
          aria-label={label}
          type="password"
          autoComplete="off"
          value={value.value}
          onChange={(e) => onChange({ value: e.target.value, enabled: true })}
        />
      ) : field.variadic ||
        field.repeatable ||
        ['--json', '--content', '--headers', '--env'].includes(field.name) ? (
        <textarea
          aria-label={label}
          rows={
            field.variadic ||
            field.repeatable ||
            ['--json', '--content', '--headers', '--env'].includes(field.name)
              ? 3
              : 1
          }
          value={value.value}
          onChange={(e) => onChange({ value: e.target.value, enabled: true })}
          placeholder={field.placeholder}
          maxLength={60000}
        />
      ) : (
        <input
          aria-label={label}
          value={value.value}
          maxLength={60000}
          placeholder={field.placeholder}
          onChange={(e) => onChange({ value: e.target.value, enabled: true })}
        />
      )}
      {field.description && (
        <details className="native-field-help">
          <summary aria-label={label + '参数说明'}>参数说明</summary>
          <p>{field.description}</p>
        </details>
      )}
      {field.optionalValue && (
        <label className="inline-check">
          <input
            type="checkbox"
            checked={!!value.bare}
            onChange={(e) => onChange({ ...value, enabled: true, bare: e.target.checked })}
          />
          仅传开关
        </label>
      )}
      {(field.variadic || field.repeatable) && (
        <p className="fine muted">多值参数可使用 JSON 字符串数组。</p>
      )}
      {admin && isFile(field) && (
        <FileControls
          value={value.value}
          field={field}
          onChange={(value) => onChange({ enabled: true, value })}
        />
      )}
    </div>
  );
}
function FileControls({
  value,
  field,
  onChange,
}: {
  value: string;
  field: { name: string; variadic: boolean; repeatable?: boolean };
  onChange: (value: string) => void;
}) {
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [name, setName] = useState('result.json');
  const client = useQueryClient();
  async function create(file?: File) {
    setError(null);
    setBusy(true);
    let uploadID = '';
    try {
      if (file && file.size > 67108864) throw new Error('上传文件最大 64 MiB');
      const slot = await api<FileSlot>('native/file/create', {
        body: { name: file?.name || name, output: !file },
      });
      if (file) {
        uploadID = slot.id;
        for (let offset = 0; offset < file.size; offset += 32768) {
          const bytes = new Uint8Array(await file.slice(offset, offset + 32768).arrayBuffer());
          await api('native/file/append', {
            body: { id: slot.id, offset, data: btoa(String.fromCharCode(...bytes)) },
          });
        }
      }
      onChange(fileParameter(field, value, slot.reference));
      uploadID = '';
      await client.invalidateQueries({ queryKey: ['native-files'] });
    } catch (error) {
      setError(error);
      if (uploadID)
        try {
          await api('native/file/delete', { body: { id: uploadID } });
        } catch {}
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="file-controls">
      <label className="file-button">
        {busy ? '正在处理…' : '上传文件'}
        <input
          type="file"
          className="sr-only"
          aria-label={field.name + ' 上传文件'}
          disabled={busy}
          onChange={(e) => {
            const file = e.target.files?.[0];
            if (file) void create(file);
            e.target.value = '';
          }}
        />
      </label>
      <details>
        <summary>保存命令输出</summary>
        <Field label="下载文件名">
          <input value={name} onChange={(e) => setName(e.target.value)} maxLength={160} />
        </Field>
        <button type="button" disabled={busy || !name.trim()} onClick={() => void create()}>
          使用此输出文件
        </button>
      </details>
      <ErrorNotice error={error} />
    </div>
  );
}
function FilesList() {
  const client = useQueryClient();
  const confirm = useConfirm();
  const files = useQuery({
    queryKey: ['native-files'],
    queryFn: ({ signal }) => api<{ items: FileSlot[] }>('native/files', { signal }),
  });
  const cleanup = useMutation({
    mutationFn: (id: string) => api('native/file/delete', { body: { id } }),
    onSuccess: () => client.invalidateQueries({ queryKey: ['native-files'] }),
  });
  return (
    <details className="native-files">
      <summary>临时文件 · {files.data?.items.length || 0}</summary>
      <ErrorNotice error={files.error || cleanup.error} />
      {files.data?.items.map((file) => (
        <div className="row" key={file.id}>
          <span>{file.name}</span>
          <code>{file.reference}</code>
          {file.ready && (
            <a download href={'/api/native/file?' + query({ id: file.id })}>
              下载
            </a>
          )}
          <button
            onClick={async () => {
              if (await confirm('清理此临时文件？')) cleanup.mutate(file.id);
            }}
          >
            清理
          </button>
        </div>
      ))}
    </details>
  );
}
function JobView({ id }: { id: string }) {
  const client = useQueryClient();
  const [state, setState] = useState<JobOutput | null>(null);
  const [output, setOutput] = useState('');
  const [error, setError] = useState<unknown>(null);
  const [revision, setRevision] = useState(0);
  const toast = useToast();
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    let active = true;
    let offset = 0;
    let accumulated = '';
    let timer: ReturnType<typeof setTimeout>;
    const controller = new AbortController();
    async function poll() {
      try {
        const job = await api<JobOutput>('native/job?' + query({ id, offset }), {
          signal: controller.signal,
        });
        if (!active) return;
        offset = job.nextOffset;
        accumulated += job.output;
        const bytes = new TextEncoder().encode(accumulated);
        if (bytes.length > 2097152) accumulated = new TextDecoder().decode(bytes.slice(-2097152));
        setState(job);
        setOutput(accumulated);
        setError(null);
        if (['queued', 'running'].includes(job.status) || Array.from(job.output).length === 65536)
          timer = setTimeout(() => void poll(), 700);
        else {
          void client.invalidateQueries({ queryKey: ['native-jobs'] });
          void client.invalidateQueries({ queryKey: ['native-files'] });
          void client.invalidateQueries({ queryKey: ['audit'] });
        }
      } catch (error) {
        if (active) setError(error);
      }
    }
    void poll();
    return () => {
      active = false;
      controller.abort();
      clearTimeout(timer);
    };
  }, [id, revision, client]);
  const action = useMutation({
    mutationFn: ({ path, value }: { path: string; value?: string }) =>
      api(path, { body: { id, ...(value === undefined ? {} : { input: value }) } }),
    onSuccess: () => {
      if (input.current) input.current.value = '';
    },
  });
  async function send(e: FormEvent) {
    e.preventDefault();
    action.mutate({ path: 'native/input', value: (input.current?.value || '') + '\n' });
  }
  let note = '';
  if (state) {
    if (!state.outputAvailable) note = '当前输出不可用，可根据操作记录核对资源状态。';
    else if (state.error) note = errors[state.error] || '命令未完成，请查看错误详情。';
    else if (state.truncated) note = '当前保留最近 2 MiB 输出。';
    else if (state.status === 'queued') note = '离开页面后仍会执行。';
    else if (state.status === 'running') note = '离开页面后命令继续运行。';
    else if (state.status === 'failed') note = '请查看输出中的失败原因。';
    else if (state.status === 'unknown') note = '执行结果尚未确认，请先核对目标资源状态。';
  }
  return (
    <Card title="执行结果">
      <ErrorNotice error={error || action.error} retry={() => setRevision((v) => v + 1)} />
      <p>
        {state?.operation} · {state ? statusLabel(state.status) : '正在读取…'}
        {state?.exit_code !== null && state?.exit_code !== undefined
          ? ' · 退出码 ' + state.exit_code
          : ''}
      </p>
      <pre className="code native-output" tabIndex={0}>
        {output}
      </pre>
      {note && <p className="muted">{note}</p>}
      {state?.error && (
        <details>
          <summary>错误详情</summary>
          <code>{state.error}</code>
        </details>
      )}
      <div className="row">
        <button
          disabled={!state || !['queued', 'running'].includes(state.status) || action.isPending}
          onClick={() => action.mutate({ path: 'native/cancel' })}
        >
          停止本地命令
        </button>
        <button
          onClick={() =>
            void navigator.clipboard
              .writeText(output)
              .catch(() => toast('浏览器未允许复制，请选择输出后手动复制'))
          }
        >
          复制当前输出
        </button>
      </div>
      {state?.canInput && (
        <form onSubmit={send}>
          <Field label="终端输入">
            <input ref={input} type="password" autoComplete="off" />
          </Field>
          <div className="row">
            <button disabled={action.isPending}>发送并换行</button>
            <button
              type="button"
              disabled={action.isPending}
              onClick={() => action.mutate({ path: 'native/input', value: '\x04' })}
            >
              结束输入
            </button>
          </div>
        </form>
      )}
    </Card>
  );
}
