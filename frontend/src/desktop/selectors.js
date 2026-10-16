export const activeStatus = status => ['queued', 'resolving', 'downloading', 'retrying', 'merging', 'validating'].includes(status);

export function matchesQuery(item, query, fields) {
  const words = query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean);
  const text = fields.map(field => String(item[field] ?? '')).join(' ').toLocaleLowerCase();
  return words.every(word => text.includes(word));
}

export function sortResources(resources, order = 'recent') {
  // Unknown sizes stay after known sizes. Tie breakers prevent polling from moving rows.
  const dateValue = item => Date.parse(item.capturedAt) || 0;
  const byRecent = (a, b) => dateValue(b) - dateValue(a) || String(a.id).localeCompare(String(b.id));
  return resources.slice().sort((a, b) => {
    if (order === 'name') return String(a.name || '').localeCompare(String(b.name || ''), 'zh-CN', {numeric: true}) || byRecent(a, b);
    if (order === 'size') {
      const known = item => item.size != null && item.size >= 0;
      return Number(known(b)) - Number(known(a)) || Math.max(0, b.size || 0) - Math.max(0, a.size || 0) || byRecent(a, b);
    }
    return byRecent(a, b);
  });
}

export function filterJobs(jobs, status, query = '') {
  return jobs.filter(job => (status === 'all' || (status === 'active' ? activeStatus(job.status) : job.status === status))
    && matchesQuery(job, query, ['name', 'file', 'url', 'quality']));
}

export function normalizedSettings(config = {}) {
  return {
    downloadDir: config.downloadDir || '', proxyPort: Number(config.proxyPort ?? 18999),
    segments: Number(config.segments ?? 8), concurrent: Number(config.concurrent ?? 3),
    https: config.https ?? true, hostFilter: config.hostFilter || '',
    classifyBy: config.classifyBy || 'none', nameTemplate: config.nameTemplate || '{title}',
    duplicatePolicy: config.duplicatePolicy || 'ask', updateUrl: config.updateUrl || '',
    speedLimitKB: Number(config.speedLimitKB ?? 0), queuePaused: config.queuePaused ?? false,
    autoCheckUpdates: config.autoCheckUpdates ?? true,
    onboardingDone: config.onboardingDone ?? true, mobilePort: Number(config.mobilePort ?? 19090),
  };
}

export function shortcutAction(event, hasDialog = false) {
  if (hasDialog || event.defaultPrevented || event.repeat || event.isComposing) return '';
  if (!(event.metaKey || event.ctrlKey) || event.altKey || event.shiftKey) return '';
  const key = event.key.toLowerCase();
  if (key === 'n') return 'add';
  if (key === 'f') return 'search';
  // Do not consume input-editing chords or the macOS/Windows system shortcuts.
  return ({'1': 'capture', '2': 'downloads', '3': 'settings', '4': 'help', '5':'music'})[key] || '';
}

export function sortDownloadQueue(jobs, now = Date.now()) {
  const group = j => j.status === 'queued' ? (Date.parse(j.scheduledAt) > now ? 2 : 1) : activeStatus(j.status) ? 0 : 3;
  return jobs.slice().sort((a, b) => {
    const delta = group(a) - group(b);
    if (delta) return delta;
    if (a.status === 'queued' && b.status === 'queued') {
      if (group(a) === 2) return Date.parse(a.scheduledAt) - Date.parse(b.scheduledAt) || String(a.id).localeCompare(String(b.id));
      return (b.priority || 0) - (a.priority || 0) || Date.parse(a.createdAt) - Date.parse(b.createdAt) || String(a.id).localeCompare(String(b.id));
    }
    return Date.parse(b.createdAt) - Date.parse(a.createdAt) || String(a.id).localeCompare(String(b.id));
  });
}
