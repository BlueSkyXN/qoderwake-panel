// getRandomValues 在通过可信局域网 HTTP 打开的浏览器中也可用。
export function requestID(
  source: Pick<Crypto, 'getRandomValues'> & Partial<Pick<Crypto, 'randomUUID'>> = crypto,
): string {
  if (typeof source.randomUUID === 'function') return source.randomUUID();
  return Array.from(source.getRandomValues(new Uint8Array(16)), (byte) =>
    byte.toString(16).padStart(2, '0'),
  ).join('');
}
