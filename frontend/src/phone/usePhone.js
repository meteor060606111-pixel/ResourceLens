import {ref, onMounted, onUnmounted} from 'vue';
import {createPhoneAPI} from './client';
import {pollDelay} from './helpers';

export function usePhone() {
  const paired = ref(false), loading = ref(true), offline = ref(''), busy = ref(false);
  const notice = ref(''), noticeError = ref(false), lastUpdated = ref(null);
  const state = ref({resources: [], jobs: [], batches: [], version: ''});
  const query = {resourcePage: 1, resourceQuery: '', resourceType: 'all', jobPage: 1, jobQuery: '', jobFilter: 'all'};
  let timer, noticeTimer, refreshPromise, reconnectPromise, disposed = false, generation = 0, failures = 0;
  function clearSession() {
    generation++;
    Object.assign(query,{resourcePage:1,resourceQuery:'',resourceType:'all',jobPage:1,jobQuery:'',jobFilter:'all'});
    paired.value = false;
    offline.value = '';
    lastUpdated.value = null;
    state.value = {resources: [], jobs: [], batches: [], version: state.value.version};
    clearTimeout(timer);
    api.abortReads();
    refreshPromise = null;
  }
  const api = createPhoneAPI({onUnauthorized() {
    if (!disposed) {clearSession(); toast('连接已过期，请重新输入电脑上的配对码', true);}
  }});
  function toast(text, error = false) {
    if (disposed) return;
    notice.value = text;
    noticeError.value = error;
    clearTimeout(noticeTimer);
    noticeTimer = setTimeout(() => notice.value = '', 4500);
  }
  function schedule() {
    clearTimeout(timer);
    if (disposed || !paired.value || document.visibilityState !== 'visible') return;
    timer = setTimeout(async () => {await refresh(); schedule();}, pollDelay(state.value, failures));
  }
  function refresh() {
    if (disposed || !paired.value) return Promise.resolve(false);
    if (refreshPromise) return refreshPromise;
    const epoch = generation;
    const pending = (async () => {
      try {
        const snapshot = await api('state?' + new URLSearchParams(query));
        if (disposed || epoch !== generation || !paired.value) return false;
        state.value = snapshot;
        lastUpdated.value = Date.now();
        offline.value = '';
        failures = 0;
        return true;
      } catch (error) {
        if (!disposed && epoch === generation && paired.value && error.kind !== 'aborted') {
          offline.value = error.message;
          failures++;
        }
        return false;
      } finally {if (refreshPromise === pending) refreshPromise = null;}
    })();
    refreshPromise = pending;
    return pending;
  }
  function reconnect() {
    if (disposed) return Promise.resolve();
    if (reconnectPromise) return reconnectPromise;
    const epoch = generation;
    loading.value = true;
    const pending = (async () => {
      try {
        const bootstrap = await api('bootstrap');
        if (disposed || epoch !== generation) return;
        state.value.version = bootstrap.version;
        if (!bootstrap.paired) {clearSession(); return;}
        paired.value = true;
        await refresh();
      } catch (error) {
        if (!disposed && epoch === generation && error.kind !== 'aborted') {offline.value = error.message; failures++;}
      } finally {
        if (reconnectPromise === pending) reconnectPromise = null;
        if (!disposed) {loading.value = false; schedule();}
      }
    })();
    reconnectPromise = pending;
    return pending;
  }
  async function run(fn) {
    if (busy.value || disposed) return;
    busy.value = true;
    try {await fn(); await refresh();}
    catch (error) {
      if (error.kind === 'connection') {offline.value = error.message; failures++;}
      if (error.kind !== 'aborted') toast(error.message, true);
    } finally {busy.value = false; schedule();}
  }
  async function pair(code) {
    await run(async () => {
      await api('pair', {code});
      generation++;
      api.abortReads();
      refreshPromise = null;
      paired.value = true;
      offline.value = '';
      failures = 0;
      toast('已连接电脑');
    });
  }
  async function disconnect() {
    await run(async () => {await api('disconnect', {}); clearSession(); toast('已断开连接');});
  }
  async function setQuery(section, values) {
    const prefix = section === 'resources' ? 'resource' : 'job';
    query[prefix + 'Page'] = values.page || 1;
    if (values.query !== undefined) query[prefix + 'Query'] = values.query;
    if (values.type !== undefined) query.resourceType = values.type;
    if (values.filter !== undefined) query.jobFilter = values.filter;
    generation++;
    api.abortReads();
    refreshPromise = null;
    loading.value = true;
    try {await refresh();} finally {loading.value = false; schedule();}
  }
  function visible() {
    if (document.visibilityState === 'visible') void reconnect();
    else {clearTimeout(timer); api.abortReads();}
  }
  onMounted(() => {
    void reconnect();
    document.addEventListener('visibilitychange', visible);
    window.addEventListener('online', visible);
  });
  onUnmounted(() => {
    disposed = true;
    generation++;
    clearTimeout(timer);
    clearTimeout(noticeTimer);
    api.close();
    document.removeEventListener('visibilitychange', visible);
    window.removeEventListener('online', visible);
  });
  return {paired, loading, offline, busy, notice, noticeError, lastUpdated, state, api, toast, refresh, reconnect, run, pair, disconnect, setQuery};
}
