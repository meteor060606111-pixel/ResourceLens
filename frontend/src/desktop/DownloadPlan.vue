<script setup>
import {computed} from 'vue';
defineProps({name: String, busy: Boolean});
const model = defineModel({required: true});
const earliest = computed(() => {
  const date = new Date(Date.now() + 60000);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
});
const emit = defineEmits(['save', 'cancel']);
</script>
<template>
  <form @submit.prevent="emit('save')" class="download-plan">
    <p class="plan-resource">{{name}}</p>
    <div class="field"><label for="plan-priority">排队优先级</label><select id="plan-priority" v-model.number="model.priority"><option :value="1">优先下载</option><option :value="0">普通</option><option :value="-1">空闲时下载</option></select><p>同一优先级按加入顺序执行，进行中的任务继续传输。</p></div>
    <div class="field"><label for="plan-start">开始时间</label><select id="plan-start" v-model="model.mode"><option value="now">队列空闲时开始</option><option value="scheduled">预约时间</option></select></div>
    <div v-if="model.mode==='scheduled'" class="field"><label for="plan-date">预约日期与时间</label><input id="plan-date" type="datetime-local" v-model="model.date" :min="earliest" required/><p>按这台 Mac 的本地时区执行。Mac 与拾流须保持运行；退出后任务保留为暂停状态。</p></div>
    <div class="modal-actions"><button type="button" @click="emit('cancel')" :disabled="busy">取消</button><button type="submit" class="primary" :disabled="busy">保存下载计划</button></div>
  </form>
</template>
