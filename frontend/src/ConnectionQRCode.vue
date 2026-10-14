<script setup>
import {computed} from 'vue';
import QRCode from 'qrcode';
const props = defineProps({value: {type: String, required: true}});
const qr = computed(() => {
  const modules = QRCode.create(props.value, {errorCorrectionLevel: 'M'}).modules;
  const edge = modules.size + 8;
  const path = [];
  for (let y = 0; y < modules.size; y++) for (let x = 0; x < modules.size; x++) {
    if (modules.get(y, x)) path.push(`M${x + 4} ${y + 4}h1v1h-1z`);
  }
  return {edge, path: path.join('')};
});
</script>
<template><svg class="connection-qr" :viewBox="`0 0 ${qr.edge} ${qr.edge}`" role="img" :aria-label="'用 iPhone 相机扫描，打开 ' + value" shape-rendering="crispEdges"><rect :width="qr.edge" :height="qr.edge" fill="#fff"/><path :d="qr.path" fill="#142b4d"/></svg></template>
<style scoped>.connection-qr{width:164px;height:164px;flex-shrink:0;border-radius:8px;border:1px solid #dfebee}</style>
