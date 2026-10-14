export class PhoneAPIError extends Error {
  constructor(message, kind = 'request', status = 0) {
    super(message);
    this.name = 'PhoneAPIError';
    this.kind = kind;
    this.status = status;
  }
}

export function createPhoneAPI({fetcher = globalThis.fetch, timeout = 30000, onUnauthorized = () => {}} = {}) {
  const requests = new Map();
  async function api(path, data) {
    const controller = new AbortController();
    requests.set(controller, data === undefined);
    let timedOut = false;
    const deadline = setTimeout(() => {timedOut = true; controller.abort();}, timeout);
    try {
      const response = await fetcher('/api/mobile/' + path, {
        signal: controller.signal, credentials: 'same-origin', cache: 'no-store',
        method: data === undefined ? 'GET' : 'POST',
        ...(data === undefined ? {} : {headers: {'Content-Type': 'application/json'}, body: JSON.stringify(data)}),
      });
      if (response.status === 401) {
        onUnauthorized();
        throw new PhoneAPIError('连接已过期，请重新输入 Mac 上的配对码', 'unauthorized', 401);
      }
      let json;
      try {json = await response.json();} catch { /* A stopped or changed gateway can return an HTML error. */ }
      if (!response.ok || !json?.ok) {
        const message = response.status === 403
          ? '连接地址已失效，请在 Mac 设置中重新扫描连接二维码'
          : json?.error || '操作未完成，请重试';
        throw new PhoneAPIError(message, response.status >= 500 || response.status === 403 ? 'connection' : 'request', response.status);
      }
      return json.data;
    } catch (error) {
      if (error instanceof PhoneAPIError) throw error;
      if (controller.signal.aborted && !timedOut) throw new PhoneAPIError('连接检查已取消', 'aborted');
      if (timedOut || error instanceof TypeError) {
        throw new PhoneAPIError('连接不到 Mac，请确认拾流已开启手机连接，并保持同一 Wi-Fi', 'connection');
      }
      throw error;
    } finally {
      clearTimeout(deadline);
      requests.delete(controller);
    }
  }
  api.abortReads = () => {for (const [controller, read] of requests) if (read) controller.abort();};
  api.close = () => {for (const controller of requests.keys()) controller.abort();};
  return api;
}
