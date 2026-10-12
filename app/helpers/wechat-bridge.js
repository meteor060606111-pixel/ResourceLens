/* ResourceLens: observe the metadata of videos played in this WeChat page. */
(() => {
  if (!/(^|\.)(channels\.weixin\.qq\.com|weixin\.qq\.com)$/.test(location.hostname)) return;
  globalThis.__resourceLensWechatBridge = __RESOURCE_LENS_WECHAT_BRIDGE__;
  if (globalThis.__resourceLensWechatInstalled === 2) return;
  globalThis.__resourceLensWechatInstalled = 2;
  const originalFetch = globalThis.fetch?.bind(globalThis);
  const sent = new Map();
  const recent = new Map();
  let pendingHealth, activeEpoch = '';
  async function bridgeHealth() {
    if (!originalFetch) return null;
    if (pendingHealth) return pendingHealth;
    pendingHealth = (async () => {
      try {
        const response = await originalFetch(globalThis.__resourceLensWechatBridge, {method:'GET',credentials:'omit',cache:'no-store'});
        if (!response.ok || response.headers.get('X-ResourceLens-Bridge') !== 'wechat-v2') return null;
        const status = await response.json();
        if (!status.capture || typeof status.epoch !== 'string') return null;
        if (activeEpoch !== status.epoch) { activeEpoch = status.epoch; sent.clear(); }
        return status;
      } catch { return null; }
    })();
    try { return await pendingHealth; } finally { pendingHealth = null; }
  }
  async function send(key, body) {
    if (sent.has(key) && Date.now() - sent.get(key) < 30000) return;
    const epoch = activeEpoch;
    sent.set(key, Date.now());
    try {
      const response = await originalFetch(globalThis.__resourceLensWechatBridge, {method:'POST',body,credentials:'omit',cache:'no-store',headers:{'Content-Type':'application/json'}});
      if (!response.ok || response.headers.get('X-ResourceLens-Bridge') !== 'wechat-v2' || epoch !== activeEpoch) sent.delete(key);
    } catch { sent.delete(key); }
  }
  function post(desc) {
    if (!originalFetch || !desc || !Array.isArray(desc.media)) return;
    const video = desc.media.find(m => m && typeof m.url === 'string' && String(m.mediaType) !== '9');
    if (!video) return;
    if (typeof video.decodeKey === 'number' && !Number.isSafeInteger(video.decodeKey)) return;
    const key = video.url + String(video.urlToken ?? '') + '|' + String(video.decodeKey ?? '');
    // BigInt can appear in page objects; decimal strings preserve 64-bit seeds.
    let body;
    try { body = JSON.stringify({objectDesc: desc}, (_, v) => typeof v === 'bigint' ? String(v) : v); } catch { return; }
    if (body.length > 1048576) return;
    recent.delete(key); recent.set(key, body);
    if (recent.size > 24) { const first = recent.keys().next().value; recent.delete(first); sent.delete(first); }
    bridgeHealth().then(status => { if (status) return send(key, body); }).catch(() => {});
  }
  function collect(value) {
    const visited = new WeakSet(); let budget = 1200;
    function walk(v, depth) {
      if (!v || typeof v !== 'object' || depth > 18 || --budget < 0 || visited.has(v)) return;
      visited.add(v);
      if (Array.isArray(v.media)) post(v);
      if (Array.isArray(v)) { for (const item of v) walk(item, depth + 1); return; }
      for (const key of Object.keys(v)) { try { walk(v[key], depth + 1); } catch {} }
    }
    try { walk(value, 0); } catch {}
  }
  globalThis.__resourceLensWechatCollect = collect;
  if (originalFetch) {
    const timer = setInterval(async () => {
      if (!recent.size) return;
      const oldEpoch = activeEpoch;
      if (!await bridgeHealth()) return;
      for (const [key, body] of recent) {
        // Replay after restarting ResourceLens / capture; clearing the visible
        // resource list alone does not trigger a replay of every old video.
        if (oldEpoch !== activeEpoch || !sent.has(key)) await send(key, body);
      }
    }, 5000);
    timer?.unref?.();
  }
  const excluded = url => String(url).includes('/__resourcelens__/wechat/');
  if (originalFetch) globalThis.fetch = function (...args) {
    const promise = originalFetch(...args);
    if (!excluded(args[0]?.url || args[0])) promise.then(response => {
      const type = response.headers.get('content-type') || '';
      if (!/json/i.test(type)) return;
      response.clone().json().then(collect).catch(() => {});
    }).catch(() => {});
    return promise;
  };
  const originalSend = globalThis.XMLHttpRequest?.prototype.send;
  if (originalSend) globalThis.XMLHttpRequest.prototype.send = function (...args) {
    this.addEventListener('load', () => {
      if (excluded(this.responseURL)) return;
      try {
        if (this.responseType === 'json') collect(this.response);
        else if (!this.responseType || this.responseType === 'text') {
          const text = this.responseText;
          if (text && text.length < 8388608 && /^[\s]*[\[{]/.test(text)) collect(JSON.parse(text));
        }
      } catch {}
    }, {once: true});
    return originalSend.apply(this, args);
  };
})();
