<script setup>
import {ChevronLeft,ChevronRight,LoaderCircle} from 'lucide-vue-next';
defineProps({paging:Object,loading:Boolean});
defineEmits(['page']);
</script>
<template><nav v-if="paging?.pages>1" class="page-controls" aria-label="列表分页"><button :disabled="loading||paging.page<=1" aria-label="上一页" @click="$emit('page',paging.page-1)"><ChevronLeft :size="19"/>上一页</button><span aria-live="polite"><LoaderCircle v-if="loading" :size="15" class="spin"/>{{paging.page}} / {{paging.pages}}</span><button :disabled="loading||paging.page>=paging.pages" aria-label="下一页" @click="$emit('page',paging.page+1)">下一页<ChevronRight :size="19"/></button></nav></template>
