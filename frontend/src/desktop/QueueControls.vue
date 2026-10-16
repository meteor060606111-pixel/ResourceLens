<script setup>
import {Pause, Play, Clock3, Gauge} from 'lucide-vue-next';
defineProps({paused: Boolean, queued: Number, scheduled: Number, limit: Number, concurrent: Number, busy: Boolean});
const emit = defineEmits(['toggle', 'settings']);
</script>
<template>
  <div class="queue-controls" aria-label="下载队列控制">
    <div class="queue-overview"><strong>{{paused ? '队列已暂停' : '自动下载队列'}}</strong><span>{{queued}} 个等待 · 同时 {{concurrent}} 个任务</span><span v-if="scheduled"><Clock3 :size="14"/>{{scheduled}} 个预约</span></div>
    <button class="compact" @click="emit('settings')"><Gauge :size="16"/>{{limit ? limit + ' KiB/s / 任务' : '不限速'}}</button>
    <button class="compact" :class="{primary:paused}" @click="emit('toggle', !paused)" :disabled="busy"><Play v-if="paused" :size="16"/><Pause v-else :size="16"/>{{paused ? '继续队列' : '暂停队列'}}</button>
    <p v-if="paused" class="queue-explanation">进行中的任务继续传输，新任务等待你恢复队列。预约任务需要 Mac 和拾流保持运行。</p>
  </div>
</template>
