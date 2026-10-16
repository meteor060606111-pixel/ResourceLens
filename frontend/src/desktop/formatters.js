import {activeStatus} from './selectors.js';

export function size(value) {
  if (value < 0 || value == null || !Number.isFinite(Number(value))) return '未知';
  const amount = Number(value);
  if (amount === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const index = Math.max(0, Math.min(Math.floor(Math.log(amount) / Math.log(1024)), 4));
  return (amount / 1024 ** index).toFixed(index === 0 ? 0 : 1) + ' ' + units[index];
}

export function percent(job) {
  if (job.status === 'completed') return 100;
  return Math.min(activeStatus(job.status) ? 99 : 100, Math.max(0, job.percent || (job.total > 0 ? job.done / job.total * 100 : 0)));
}

export function jobLabel(job) {
  if (job.status === 'queued' && Date.parse(job.scheduledAt) > Date.now()) return '等待预约';
  return {queued: '等待下载', resolving: '解析视频', downloading: '下载中', retrying: '重新连接', merging: '合并音视频', validating: '校验文件', paused: '已暂停', completed: '已完成', cancelled: '已取消', failed: '下载失败'}[job.status] || job.status;
}

export function duration(value) {
  if (!value) return '';
  const seconds = Math.round(value);
  return Math.floor(seconds / 60) + ':' + String(seconds % 60).padStart(2, '0');
}

export function eta(value) {
  if (!value || value < 0) return '正在估算';
  if (value < 60) return Math.ceil(value) + ' 秒';
  if (value < 3600) return Math.ceil(value / 60) + ' 分钟';
  return (value / 3600).toFixed(1) + ' 小时';
}
