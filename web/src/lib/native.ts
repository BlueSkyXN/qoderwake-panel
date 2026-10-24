import { nativeVerbs } from './native-labels.ts';
export interface Argument {
  name: string;
  required: boolean;
  variadic: boolean;
  description: string;
}
export interface Option {
  name: string;
  aliases: string[];
  description: string;
  value: boolean;
  optionalValue: boolean;
  variadic: boolean;
  repeatable: boolean;
  placeholder: string;
  choices: string[];
  secret: boolean;
}
export interface Operation {
  id: string;
  path: string[];
  group: string;
  groupName: string;
  description: string;
  revision: string;
  effect: 'read' | 'write';
  scope: 'admin' | 'waker';
  panelPage: string | null;
  panelOnly: boolean;
}
export interface Spec extends Operation {
  arguments: Argument[];
  options: Option[];
  help: string;
  available: boolean;
  error?: string;
  currentHelp?: string;
  contextKeys: string[];
}
export interface Job {
  id: string;
  operation: string;
  waker: string;
  status: string;
  created: number;
  updated: number;
  exit_code: number | null;
  error: string;
}
export interface JobOutput extends Job {
  output: string;
  nextOffset: number;
  outputAvailable: boolean;
  interactive: boolean;
  canInput: boolean;
  truncated: boolean;
}
export interface FileSlot {
  id: string;
  name: string;
  reference: string;
  output: boolean;
  ready?: boolean;
}
export type Parameter = string | boolean | string[];
export interface Parameters {
  arguments: Record<string, Parameter>;
  options: Record<string, Parameter>;
}
export interface FieldValue {
  value: string;
  enabled: boolean;
  bare?: boolean;
}
export function title(spec: Operation) {
  if (spec.id === 'mcp.auth.start') return '授权 MCP 服务器';
  if (spec.path.length === 1) return spec.groupName;
  const leaf = spec.path.at(-1) || '';
  return spec.groupName + ' · ' + (nativeVerbs[leaf] || leaf);
}
export function matches(spec: Operation, search: string) {
  const text = [spec.id, spec.path.join(' '), spec.description, spec.groupName, title(spec)]
    .join(' ')
    .toLowerCase();
  return search
    .toLowerCase()
    .trim()
    .split(/\s+/)
    .every((word) => text.includes(word));
}
export function optionActive(name: string, mode: string, transport: string) {
  if (mode === 'all' || ['--waker-id', '--expected-revision', '--idempotency-key'].includes(name))
    return true;
  if (mode === 'json') return name === '--json';
  if (mode === 'file') return name === '--json-file';
  if (['--json', '--json-file'].includes(name)) return false;
  if (['--command', '--args', '--env'].includes(name)) return transport === 'stdio';
  if (
    ['--http-url', '--headers', '--header', '--auto-inject-at-waker-sender-id'].includes(name) ||
    name.startsWith('--oauth-')
  )
    return ['http', 'sse'].includes(transport);
  return true;
}
export function parameterValue(
  field: { variadic: boolean; repeatable?: boolean },
  value: string,
): string | string[] {
  if (!field.variadic && !field.repeatable) return value;
  let result: unknown;
  try {
    result = JSON.parse(value);
  } catch {
    result = value.split('\n').filter(Boolean);
  }
  if (!Array.isArray(result) || !result.length || result.some((v) => typeof v !== 'string'))
    throw new Error('多值参数请逐行填写，或使用 JSON 字符串数组');
  return result;
}
export function parameters(
  spec: Spec,
  args: Record<string, string>,
  options: Record<string, FieldValue>,
  mode: string,
  transport: string,
): Parameters {
  const result: Parameters = { arguments: {}, options: {} };
  for (const field of spec.arguments) {
    const value = args[field.name] || '';
    if (value !== '' || field.required) result.arguments[field.name] = parameterValue(field, value);
  }
  for (const field of spec.options) {
    if (spec.id === 'mcp.add' && !optionActive(field.name, mode, transport)) continue;
    const item = options[field.name];
    if (!item?.enabled) continue;
    result.options[field.name] =
      !field.value || item.bare ? true : parameterValue(field, item.value);
  }
  return result;
}
export function section(field: Option, spec: Operation) {
  const name = field.name;
  if (['--expected-revision', '--base-version-id', '--idempotency-key'].includes(name))
    return 'version';
  if (
    name.startsWith('--oauth-') ||
    ['--headers', '--header', '--auto-inject-at-waker-sender-id'].includes(name)
  )
    return 'auth';
  if (spec.id === 'mcp.add' && ['--json', '--json-file'].includes(name)) return 'import';
  if (spec.group === 'mcp' && ['--command', '--args', '--env', '--http-url'].includes(name))
    return 'connection';
  if (
    ['--format', '--force', '--yes', '--dry-run', '--apply', '--no-open'].includes(name) ||
    (name === '--json' && !field.value)
  )
    return 'advanced';
  return 'basic';
}
export const statusLabel = (status: string) =>
  (
    ({
      queued: '等待执行',
      running: '执行中',
      succeeded: '命令成功退出',
      failed: '命令退出失败',
      unknown: '结果待核对',
      cancelled: '已停止命令',
    }) as Record<string, string>
  )[status] || '状态未知';
export const isFile = (field: Option | Argument) =>
  (!('value' in field) || field.value) &&
  (/file|path|zip|archive|output|out$/i.test(field.name) ||
    ('placeholder' in field && /file|path|zip/i.test(field.placeholder)));
export function fileParameter(
  field: { variadic: boolean; repeatable?: boolean },
  value: string,
  reference: string,
) {
  if (!field.variadic && !field.repeatable) return reference;
  const current = value.trim() ? parameterValue(field, value) : [];
  return JSON.stringify([...(Array.isArray(current) ? current : [current]), reference]);
}
