<script setup>
import {ShieldCheck, ShieldAlert} from 'lucide-vue-next';
defineProps({capture: Boolean, certificateInstalled: Boolean});
const emit = defineEmits(['settings']);
</script>
<template>
  <div class="capture-status" :class="{capturing: capture}" role="status">
    <i class="capture-dot" aria-hidden="true"></i>
    <strong>{{capture ? '正在捕获' : '捕获已就绪'}}</strong>
    <span class="capture-status-description">{{capture ? '浏览网页或播放媒体，资源会自动出现' : '粘贴链接，或开启捕获后播放媒体'}}</span>
    <button @click="emit('settings')"><component :is="certificateInstalled ? ShieldCheck : ShieldAlert" :size="18"/>HTTPS 证书{{certificateInstalled ? '已安装' : '未安装'}}</button>
  </div>
</template>
