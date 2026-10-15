<script setup>
import {Search, Download, Settings, BookOpen, Network, Copy, ChevronDown, Music} from 'lucide-vue-next';
defineProps({page: String, activeJobs: Number, loaded: Boolean, error: String, capture: Boolean, proxy: String, version: String, platform: String});
const emit = defineEmits(['navigate', 'copy']);
const navigation = [{id: 'capture', label: '资源捕获', icon: Search, shortcut:1}, {id:'music', label:'音乐搜索', icon:Music, shortcut:5}, {id: 'downloads', label: '下载任务', icon: Download, shortcut:2}, {id: 'settings', label: '设置', icon: Settings, shortcut:3}, {id: 'help', label: '使用帮助', icon: BookOpen, shortcut:4}];
</script>

<template>
  <aside class="sidebar">
    <div class="brand">
      <svg class="brand-mark" viewBox="0 0 56 44" aria-hidden="true"><path d="M3 22C16 6 21 4 31 18S43 34 53 20M3 35C16 19 21 17 31 31S43 47 53 33" fill="none" stroke="currentColor" stroke-width="5" stroke-linecap="round"/></svg>
      <div><strong>拾流</strong><span>ResourceLens</span></div>
    </div>
    <nav aria-label="主导航">
      <button v-for="item in navigation" :key="item.id" :class="{active: page === item.id}" :aria-current="page === item.id ? 'page' : undefined" :title="item.label + ' · ⌘ / Ctrl + ' + item.shortcut" @click="emit('navigate', item.id)">
        <component :is="item.icon" :size="20" :stroke-width="1.8"/><span>{{item.label}}</span>
        <span v-if="item.id === 'downloads' && activeJobs" class="nav-count">{{activeJobs}}</span>
      </button>
    </nav>
    <div class="sidebar-bottom">
      <div class="connection"><i :class="{online: loaded && !error}"></i>{{error ? '服务连接中断' : capture ? '正在捕获资源' : '本地服务已就绪'}}</div>
      <details class="proxy-details"><summary><Network :size="14"/>本地代理<ChevronDown :size="13"/></summary><button class="proxy-copy" @click="emit('copy', proxy)" :aria-label="'复制代理地址 ' + proxy">{{proxy}}<Copy :size="14"/></button></details>
      <span class="version">v{{version || '1.6.0'}} · {{platform}}</span>
    </div>
  </aside>
</template>
