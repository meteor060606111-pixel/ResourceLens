// Run a public Douyin page normally in a private temporary browser profile.
// Read the page's own response; never import a profile, cookies or account state.
import fs from 'node:fs/promises';
import path from 'node:path';
import {spawn} from 'node:child_process';

const args = new Map();
for (let i = 2; i < process.argv.length; i += 2) args.set(process.argv[i], process.argv[i + 1]);
const executable = args.get('--browser');
const profile = args.get('--profile');
const start = new URL(args.get('--url'));
const timeout = Math.min(60000, Math.max(5000, Number(args.get('--timeout') || 45000)));
const site = hostname => hostname === 'douyin.com' || hostname.endsWith('.douyin.com') || hostname === 'iesdouyin.com' || hostname.endsWith('.iesdouyin.com');
if (!executable || !profile || !path.isAbsolute(profile) || !['https:', 'http:'].includes(start.protocol) || !site(start.hostname) || start.username || start.password) throw new Error('无效的抖音浏览器请求');

function videoId(url) {
  try {
    const u = new URL(url);
    if (!site(u.hostname)) return '';
    return u.pathname.match(/^\/(?:share\/)?video\/(\d{8,24})(?:\/|$)/)?.[1] || ['modal_id', 'vid', 'aweme_id', 'item_id'].map(k => u.searchParams.get(k)).find(v => /^\d{8,24}$/.test(v || '')) || '';
  } catch { return ''; }
}
const wait = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds));

class CDP {
  constructor(socket) {
    this.socket = socket;
    this.sequence = 0;
    this.pending = new Map();
    this.listeners = new Set();
    socket.addEventListener('message', event => {
      let message;
      try { message = JSON.parse(event.data); } catch { return; }
      if (message.id) {
        const p = this.pending.get(message.id);
        if (!p) return;
        clearTimeout(p.timer);
        this.pending.delete(message.id);
        if (message.error) p.reject(new Error(message.error.message));
        else p.resolve(message.result || {});
      } else for (const listener of this.listeners) listener(message);
    });
    socket.addEventListener('close', () => {
      for (const p of this.pending.values()) { clearTimeout(p.timer); p.reject(new Error('浏览器连接已关闭')); }
      this.pending.clear();
    });
  }
  send(method, params = {}, sessionId) {
    const id = ++this.sequence;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.pending.delete(id); reject(new Error('浏览器响应超时')); }, 12000);
      this.pending.set(id, {resolve, reject, timer});
      this.socket.send(JSON.stringify({id, method, params, ...(sessionId ? {sessionId} : {})}));
    });
  }
}

function requestedItem(value, id, depth = 0) {
  if (!value || typeof value !== 'object' || depth > 35) return null;
  if (value.aweme_id === id && value.video && typeof value.video === 'object') return value;
  for (const nested of Object.values(value)) {
    const found = requestedItem(nested, id, depth + 1);
    if (found) return found;
  }
  return null;
}
function publicItem(item) {
  const keys = ['width', 'height', 'duration', 'format', 'is_h265', 'is_bytevc1', 'is_bytevc2', 'is_drm', 'drm_type', 'play_addr', 'play_addr_h264', 'play_addr_265', 'play_addr_bytevc1', 'download_addr', 'bit_rate'];
  const video = {};
  for (const key of keys) if (item.video[key] !== undefined) video[key] = item.video[key];
  return {aweme_id: item.aweme_id, desc: item.desc || item.title || '', video, status: {is_private: item.status?.is_private, is_prohibited: item.status?.is_prohibited}, is_drm: item.is_drm};
}

let browser, cdp, socket, failure;
let complete = false;
const started = Date.now();
try {
  await fs.mkdir(profile, {recursive: true});
  browser = spawn(executable, ['--headless=new', '--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1', '--user-data-dir=' + profile, '--no-first-run', '--no-default-browser-check', '--disable-sync', '--disable-extensions', '--disable-background-networking', '--no-proxy-server', '--autoplay-policy=no-user-gesture-required', '--mute-audio', '--window-size=1365,900', 'about:blank'], {stdio: 'ignore', windowsHide: true});
  browser.on('error', error => { failure = error; });
  let endpoint;
  while (Date.now() - started < Math.min(timeout, 18000)) {
    if (failure) throw failure;
    if (browser.exitCode !== null) throw new Error('浏览器启动失败');
    try {
      const [port, resource] = (await fs.readFile(path.join(profile, 'DevToolsActivePort'), 'utf8')).trim().split(/\r?\n/);
      if (/^\d+$/.test(port) && resource.startsWith('/devtools/browser/')) { endpoint = `ws://127.0.0.1:${port}${resource}`; break; }
    } catch {}
    await wait(80);
  }
  if (!endpoint) throw new Error('未能连接本机浏览器');
  socket = new WebSocket(endpoint);
  await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, {once: true}); socket.addEventListener('error', () => reject(new Error('未能连接浏览器调试端口')), {once: true}); });
  cdp = new CDP(socket);
  const version = await cdp.send('Browser.getVersion');
  const {targetId} = await cdp.send('Target.createTarget', {url: 'about:blank'});
  const {sessionId} = await cdp.send('Target.attachToTarget', {targetId, flatten: true});
  let pageUrl = start.href;
  let expectedId = videoId(pageUrl);
  let result;
  let blocked = '';
  const metadata = new Set();
  const accept = item => {
    if (result || !item || item.aweme_id !== expectedId) return;
    if (item.status?.is_private || item.status?.is_prohibited || item.is_drm || item.video?.is_drm || item.video?.drm_type > 0) {
      blocked = '该抖音作品要求权限或受 DRM 保护';
      return;
    }
    result = {item: publicItem(item), page_url: pageUrl, user_agent: version.userAgent, method: 'normal-browser-response'};
  };
  cdp.listeners.add(message => {
    if (message.sessionId !== sessionId) return;
    const p = message.params || {};
    if (message.method === 'Page.frameNavigated' && !p.frame?.parentId) {
      pageUrl = p.frame.url;
      expectedId ||= videoId(pageUrl);
    } else if (message.method === 'Network.responseReceived') {
      try {
        const u = new URL(p.response.url);
        if (site(u.hostname) && p.response.status === 200 && /\/(?:aweme\/detail|aweme\/iteminfo)\//.test(u.pathname)) metadata.add(p.requestId);
      } catch {}
    } else if (message.method === 'Network.loadingFinished' && metadata.delete(p.requestId)) {
      cdp.send('Network.getResponseBody', {requestId: p.requestId}, sessionId).then(({body, base64Encoded}) => {
        if (result || !expectedId || body.length > (8 << 20)) return;
        try { accept(requestedItem(JSON.parse(base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body), expectedId)); } catch {}
      }).catch(() => {});
    }
  });
  await Promise.all([cdp.send('Page.enable', {}, sessionId), cdp.send('Runtime.enable', {}, sessionId), cdp.send('Network.enable', {maxTotalBufferSize: 32 << 20, maxResourceBufferSize: 8 << 20}, sessionId)]);
  await cdp.send('Page.navigate', {url: start.href}, sessionId);
  let inspected = 0;
  while (!result && !blocked && Date.now() - started < timeout) {
    if (failure) throw failure;
    if (Date.now() - inspected > 2200) {
      inspected = Date.now();
      // Read existing hydration data as a backup. No cookies, storage or API calls.
      const read = await cdp.send('Runtime.evaluate', {expression: `JSON.stringify({url:location.href,router:window._ROUTER_DATA,render:document.querySelector('script#RENDER_DATA')?.textContent,caption:document.body?.innerText?.slice(0,1200)})`, returnByValue: true}, sessionId).catch(() => null);
      if (read?.result?.value) {
        try {
          const value = JSON.parse(read.result.value);
          pageUrl = value.url || pageUrl;
          expectedId ||= videoId(pageUrl);
          if (expectedId) {
            accept(requestedItem(value.router, expectedId));
            if (value.render) accept(requestedItem(JSON.parse(decodeURIComponent(value.render)), expectedId));
          }
          if (/(请完成验证|请通过验证|请拖动滑块|请按住滑块|访问过于频繁)/.test(value.caption || '')) blocked = '抖音要求用户在浏览器中完成验证';
        } catch {}
      }
    }
    await wait(100);
  }
  if (blocked) throw new Error(blocked);
  if (!result) throw new Error('公开页面没有返回该作品的播放地址；可能要求验证、登录，或作品已不可用');
  process.stdout.write(JSON.stringify(result));
  complete = true;
} catch (error) {
  process.stderr.write(String(error.message || error) + '\n');
} finally {
  if (cdp) await cdp.send('Browser.close').catch(() => {});
  if (socket) socket.close();
  if (browser && browser.exitCode === null) {
    await Promise.race([new Promise(resolve => browser.once('exit', resolve)), wait(1800)]);
    if (browser.exitCode === null) browser.kill();
  }
  // The Go owner closes the process Job and removes this exact temporary profile.
  process.exitCode = complete ? 0 : 1;
}
