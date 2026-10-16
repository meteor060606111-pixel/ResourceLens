<script setup>
import {Download, CheckCircle2, AlertCircle, Pause, Play, X, FolderOpen, RefreshCw, Eye} from 'lucide-vue-next';
import {activeStatus} from './selectors.js';
import {size, percent, jobLabel, duration, eta} from './formatters.js';
defineProps({job: Object, resource: Object, poster: String, checked: Boolean, busy: Boolean});
const emit = defineEmits(['select', 'poster-error', 'pause', 'cancel', 'resume', 'preview', 'folder', 'plan']);
</script>
<template>
  <article :data-job-id="job.id" class="download-row" :class="'job-' + job.status">
    <input type="checkbox" :checked="checked" :disabled="activeStatus(job.status) || job.status === 'paused'" :aria-label="'选择任务 ' + job.name" @change="emit('select',$event.target.checked)"/>
    <div class="job-icon" :class="[job.status, {'has-poster': poster}]">
      <img v-if="poster" :src="poster" alt="" @error="emit('poster-error')"/>
      <template v-else><CheckCircle2 v-if="job.status === 'completed'" :size="25"/><AlertCircle v-else-if="job.status === 'failed'" :size="25"/><Pause v-else-if="job.status === 'paused'" :size="25"/><Download v-else :size="25"/></template>
      <span v-if="resource?.duration" class="poster-duration">{{duration(resource.duration)}}</span>
    </div>
    <div class="job-description"><h2 :title="job.name">{{job.name}}</h2><p v-if="job.quality" class="job-quality">{{job.quality}}</p><p v-if="job.priority || Date.parse(job.scheduledAt)>Date.now()" class="job-queue-meta"><span v-if="job.priority">{{job.priority>0?'优先':'空闲时'}}</span><span v-if="Date.parse(job.scheduledAt)>Date.now()">{{new Date(job.scheduledAt).toLocaleString('zh-CN',{month:'numeric',day:'numeric',hour:'2-digit',minute:'2-digit'})}} 开始</span></p><p class="job-path" :title="job.file || job.url">{{job.file || job.url}}</p></div>
    <div class="job-transfer">
      <div class="job-progress-line"><div class="progress-track" :class="{unknown: job.total <= 0 && activeStatus(job.status)}" role="progressbar" :aria-label="job.name + ' 下载进度'" :aria-valuenow="job.total > 0 ? Math.round(percent(job)) : undefined" aria-valuemin="0" aria-valuemax="100" :aria-valuetext="jobLabel(job)"><div :style="{width: percent(job) + '%'}" :class="job.status"></div></div><span v-if="job.total > 0" class="job-percent">{{percent(job).toFixed(0)}}%</span></div>
      <div class="job-meta"><span>{{size(job.done)}} / {{size(job.total)}}</span></div>
      <p v-if="job.error" class="job-error"><AlertCircle :size="14"/>{{job.error}}</p><p v-else-if="job.notice" class="job-notice">{{job.notice}}</p>
    </div>
    <div class="job-status-block"><span :class="'job-status ' + job.status">{{jobLabel(job)}}</span><p v-if="job.status === 'downloading'" class="job-speed">{{size(job.speed)}}/s · 剩余 {{eta(job.eta)}}</p><p v-if="job.status === 'paused'" class="job-notice">{{job.resumeAvailable ? '进度已保留' : '恢复后检查进度'}}</p><button v-if="['queued','paused'].includes(job.status)" class="text-button job-plan-button" @click="emit('plan')" :disabled="busy">调整计划</button></div>
    <div class="job-actions">
      <template v-if="activeStatus(job.status)"><button class="icon-button" @click="emit('pause')" :disabled="busy || ['merging','validating'].includes(job.status)" aria-label="暂停下载" :title="['merging','validating'].includes(job.status) ? '文件处理完成后可操作' : '暂停并保留下载进度'"><Pause :size="18"/></button><button class="icon-button cancel-task" @click="emit('cancel')" :disabled="busy" aria-label="取消下载" title="取消下载"><X :size="18"/></button></template>
      <template v-else-if="job.status === 'completed'"><button class="icon-button" @click="emit('preview')" aria-label="播放已下载文件" title="预览已下载文件"><Eye v-if="resource?.type === 'image'" :size="18"/><Play v-else :size="18"/></button><button class="icon-button" @click="emit('folder')" aria-label="定位下载文件" title="打开文件夹并选中此文件"><FolderOpen :size="19"/></button></template>
      <template v-else><button class="icon-button retry-task" @click="emit('resume')" :disabled="busy || (!job.resumeAvailable && job.status !== 'paused' && !resource)" :aria-label="job.status === 'paused' || job.resumeAvailable ? '继续下载' : '重试下载'" :title="job.status === 'paused' || job.resumeAvailable ? '继续已有下载进度' : '重新下载'"><Play v-if="job.status === 'paused'" :size="18"/><RefreshCw v-else :size="18"/></button><button v-if="job.status === 'paused'" class="icon-button cancel-task" @click="emit('cancel')" :disabled="busy" aria-label="取消暂停任务"><X :size="18"/></button></template>
    </div>
  </article>
</template>
