<script setup>
import {Sparkles, Download, Layers} from 'lucide-vue-next';
defineProps({modelValue: Object, systemMotion: Boolean, systemTransparency: Boolean});
const emit = defineEmits(['update:modelValue', 'reset']);
</script>
<template>
  <div class="settings-group appearance-group" id="settings-panel-appearance" role="tabpanel" aria-labelledby="settings-tab-appearance">
    <h2>界面外观</h2><p class="settings-intro">调整玻璃的通透程度，变化立即生效。</p>
    <div class="glass-preview" aria-hidden="true"><Sparkles :size="28"/><strong>拾流 <small>ResourceLens</small></strong><span><Layers :size="16"/>通透 · 轻盈</span><span class="preview-primary"><Download :size="16"/>流动光影</span></div>
    <div class="tint-field"><label for="glass-tint">玻璃透明程度 <output>{{modelValue.tint}}%</output></label><div><span>更清透</span><input id="glass-tint" type="range" min="0" max="100" :value="modelValue.tint" :disabled="modelValue.reduceTransparency || systemTransparency" @input="emit('update:modelValue',{...modelValue,tint:Number($event.target.value)})"/><span>更柔和</span></div><p>清透时背景更明显；柔和时文字更突出。</p></div>
    <div class="switch-row"><div><strong>减少透明效果</strong><p>{{systemTransparency?'系统已开启减少透明效果。':'使用更实的背景，便于阅读。'}}</p></div><label class="switch"><input type="checkbox" aria-label="减少透明效果" :checked="modelValue.reduceTransparency || systemTransparency" :disabled="systemTransparency" @change="emit('update:modelValue',{...modelValue,reduceTransparency:$event.target.checked})"/><span></span></label></div>
    <div class="switch-row"><div><strong>减少动态效果</strong><p>{{systemMotion?'系统已开启减少动态效果。':'关闭流动光影、跟随高光和面板动画。'}}</p></div><label class="switch"><input type="checkbox" aria-label="减少动态效果" :checked="modelValue.reduceMotion || systemMotion" :disabled="systemMotion" @change="emit('update:modelValue',{...modelValue,reduceMotion:$event.target.checked})"/><span></span></label></div>
    <div class="appearance-footer"><span>外观偏好自动保存在这台电脑。</span><button @click="emit('reset')">恢复默认外观</button></div>
  </div>
</template>
