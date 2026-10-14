<script setup>
defineProps({modelValue: String});
const emit = defineEmits(['update:modelValue']);
const tabs = [{id: 'files', label: '下载与文件'}, {id: 'capture', label: '网络捕获'}, {id: 'appearance', label: '界面外观'}, {id: 'devices', label: '设备连接'}, {id: 'maintenance', label: '维护'}, {id: 'updates', label: '关于与更新'}];
function move(event, index) {
  let next;
  if (event.key === 'ArrowRight') next = (index + 1) % tabs.length;
  if (event.key === 'ArrowLeft') next = (index - 1 + tabs.length) % tabs.length;
  if (event.key === 'Home') next = 0;
  if (event.key === 'End') next = tabs.length - 1;
  if (next === undefined) return;
  event.preventDefault();
  emit('update:modelValue', tabs[next].id);
  event.currentTarget.parentElement.children[next].focus();
}
</script>
<template>
  <div class="settings-tabs" role="tablist" aria-label="设置类别">
    <button v-for="(tab,index) in tabs" :id="'settings-tab-' + tab.id" :key="tab.id" role="tab" :aria-selected="modelValue === tab.id" :aria-controls="'settings-panel-' + tab.id" :tabindex="modelValue === tab.id ? 0 : -1" :class="{selected: modelValue === tab.id}" @click="emit('update:modelValue', tab.id)" @keydown="move($event,index)">{{tab.label}}</button>
  </div>
</template>
