<script setup>
import {computed,ref,watch} from 'vue';
import {Video,Music,Image,File} from 'lucide-vue-next';
import {duration} from './helpers';
const props=defineProps({resource:Object,tall:Boolean});
const failed=ref(false);watch(()=>props.resource?.id,()=>failed.value=false);
const icon=computed(()=>({audio:Music,image:Image,video:Video,playlist:Video}[props.resource?.type]||File));
</script>
<template><div class="media-thumb" :class="{tall}"><img v-if="resource?.hasPoster&&!failed" :src="'/poster/'+encodeURIComponent(resource.id)" alt="" loading="lazy" decoding="async" width="90" height="90" @error="failed=true"><component v-else :is="icon" :size="28" aria-hidden="true"/><span v-if="resource?.duration" class="duration">{{duration(resource.duration)}}</span></div></template>
