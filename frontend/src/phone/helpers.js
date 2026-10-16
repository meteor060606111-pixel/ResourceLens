export const activeStatus=s=>['queued','resolving','downloading','retrying','merging','validating'].includes(s);
export function size(v){if(!v||v<0)return '大小待确认';const units=['B','KB','MB','GB','TB'];const i=Math.min(Math.floor(Math.log(v)/Math.log(1024)),4);return (v/1024**i).toFixed(i?1:0)+' '+units[i]}
export function percent(j){if(j.status==='completed')return 100;return Math.min(activeStatus(j.status)?99:100,Math.max(0,j.percent||(j.total>0?j.done/j.total*100:0)))}
export function jobLabel(s){return {queued:'等待电脑下载',resolving:'电脑正在解析',downloading:'电脑正在下载',retrying:'正在重新连接',merging:'电脑正在合并',validating:'电脑正在校验',paused:'已暂停',completed:'已完成',cancelled:'已取消',failed:'下载失败'}[s]||s}
export function duration(v){if(!v)return '';const seconds=Math.round(v);return Math.floor(seconds/60)+':'+String(seconds%60).padStart(2,'0')}
export function inputLinks(text){return [...new Set((text.match(/https?:\/\/[^\s<>"'，。；、（）【】]+/gi)||[]).map(s=>s.replace(/[，。；、！!"'）)\]}]+$/g,'')))]}
export function pollDelay(state, failures = 0) {
  if (failures) return Math.min(1500 * 2 ** Math.min(failures, 4), 15000);
  return (state.activity ?? (state.jobs.some(j => activeStatus(j.status)) || state.batches.some(b => b.status === 'parsing'))) ? 1500 : 10000;
}
export function matchesSearch(item, text) {
  const words = text.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean);
  const value = [item.name, item.site, item.author].filter(Boolean).join(' ').toLocaleLowerCase();
  return words.every(word => value.includes(word));
}
export function remaining(seconds) {
  if (!Number.isFinite(seconds) || seconds <= 0) return '';
  if (seconds < 60) return '约 ' + Math.ceil(seconds) + ' 秒';
  if (seconds < 3600) return '约 ' + Math.ceil(seconds / 60) + ' 分钟';
  return '约 ' + (seconds / 3600).toFixed(1) + ' 小时';
}

export function plannedLabel(job, queuePaused = false) {
  if (job.status !== 'queued') return jobLabel(job.status);
  const scheduled = Date.parse(job.scheduledAt);
  if (scheduled > Date.now()) return '预约 ' + new Date(scheduled).toLocaleString('zh-CN', {month:'numeric', day:'numeric', hour:'2-digit', minute:'2-digit'});
  return queuePaused ? '等待队列继续' : '等待电脑下载';
}
