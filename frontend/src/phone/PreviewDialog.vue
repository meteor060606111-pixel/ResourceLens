<script setup>
import {ref,onMounted,onUnmounted,nextTick} from 'vue';
import {X} from 'lucide-vue-next';
const props=defineProps({job:Object,type:String});const emit=defineEmits(['close']);
const failed=ref(false),dialog=ref(null);let trigger;
function key(event){if(event.key==='Escape'){emit('close');return}if(event.key!=='Tab')return;const items=[...dialog.value.querySelectorAll('button,a,video,audio')];const first=items[0],last=items.at(-1);if(event.shiftKey&&document.activeElement===first){event.preventDefault();last?.focus()}else if(!event.shiftKey&&document.activeElement===last){event.preventDefault();first?.focus()}}
onMounted(async()=>{trigger=document.activeElement;document.body.style.overflow='hidden';await nextTick();dialog.value.querySelector('button')?.focus()});
onUnmounted(()=>{document.body.style.overflow='';trigger?.focus()});
</script>
<template><div class="dialog-overlay" @click.self="emit('close')"><section ref="dialog" class="phone-dialog" role="dialog" aria-modal="true" :aria-label="job.name" @keydown="key"><header><h2>{{job.name}}</h2><button class="icon-button" @click="emit('close')" aria-label="关闭预览"><X :size="23"/></button></header><p v-if="failed" role="alert">当前文件无法在浏览器中预览，可保存到手机后打开。</p><img v-else-if="type==='image'" class="preview-image" :src="'/preview/'+encodeURIComponent(job.id)" :alt="job.name" @error="failed=true"><audio v-else-if="type==='audio'" preload="none" controls :src="'/preview/'+encodeURIComponent(job.id)" @error="failed=true"/><video v-else controls playsinline preload="none" :src="'/preview/'+encodeURIComponent(job.id)" @error="failed=true"/><a class="button primary wide" :href="'/files/'+encodeURIComponent(job.id)" download>保存到手机</a><p class="preview-hint">保存后可在 Safari 下载列表中打开，再分享至「文件」或「照片」。</p></section></div></template>
