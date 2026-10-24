import { errors } from './messages';
let csrf = '';
export function setCSRF(value: string) {
  csrf = value;
}
export class APIError extends Error {
  code: string;
  status: number;
  detail: unknown;
  constructor(code: string, status: number, detail?: unknown) {
    super(errors[code] ?? '请求未完成，请查看错误详情。');
    this.code = code;
    this.status = status;
    this.detail = detail;
  }
}
export async function api<T>(
  path: string,
  options: {
    body?: unknown;
    signal?: AbortSignal;
    headers?: Record<string, string>;
    notifyUnauthorized?: boolean;
  } = {},
): Promise<T> {
  const { body, signal } = options;
  const response = await fetch('/api/' + path, {
    method: body === undefined ? 'GET' : 'POST',
    credentials: 'same-origin',
    signal,
    headers: {
      'Content-Type': 'application/json',
      ...(body === undefined ? {} : { 'X-CSRF-Token': csrf }),
      ...options.headers,
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let data: Record<string, unknown>;
  try {
    data = await response.json();
  } catch {
    throw new APIError('backend_unavailable', response.status);
  }
  if (
    !response.ok ||
    (data?.ok === false && !['pending', 'unknown'].includes(String(data?.status)))
  ) {
    if (response.status === 401 && options.notifyUnauthorized !== false)
      window.dispatchEvent(new Event('panel-session-expired'));
    const error = new APIError(
      typeof data?.error === 'string' ? data.error : 'backend_unavailable',
      response.status,
      data?.daemon,
    );
    if (typeof data?.message === 'string' && /[\u3400-\u9fff]/.test(data.message))
      error.message = data.message.slice(0, 400);
    throw error;
  }
  return data as T;
}
export const query = (values: Record<string, string | number>) =>
  new URLSearchParams(
    Object.entries(values).map(([key, value]) => [key, String(value)]),
  ).toString();
