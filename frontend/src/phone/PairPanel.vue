<script setup>
import {ref} from 'vue';
import {LoaderCircle,Wifi,LogOut} from 'lucide-vue-next';
const props=defineProps({paired:Boolean,busy:Boolean,loading:Boolean,version:String,lastUpdated:Number,offline:String});
const emit=defineEmits(['pair','disconnect']);
const code=ref('');
const address=window.location.host;
function submit(){if(/^\d{8}$/.test(code.value))emit('pair',code.value)}
</script>
<template>
  <section v-if="!paired" class="pair-panel">
    <h1>连接你的电脑</h1><p class="intro">同一 Wi-Fi，输入电脑上显示的配对码。</p>
    <form @submit.prevent="submit" class="pair-form"><label class="sr-only" for="pair-code">8 位配对码</label><input id="pair-code" v-model="code" placeholder="8 位配对码" inputmode="numeric" pattern="[0-9]{8}" maxlength="8" autocomplete="off" required :disabled="loading||busy"><button class="primary wide" :disabled="loading||busy||!/^\d{8}$/.test(code)"><LoaderCircle v-if="busy||loading" class="spin" :size="20"/>{{loading?'正在连接…':'连接'}}</button></form>
    <ol class="pair-steps"><li><b>1</b>电脑打开拾流</li><li><b>2</b>设置中开启手机连接</li><li><b>3</b>Safari 打开连接地址</li></ol>
    <p class="home-hint">在 Safari 分享菜单中添加到主屏幕。</p>
  </section>
  <section v-else class="connection-panel"><h1>连接电脑</h1><div class="connection-card"><Wifi :size="28"/><strong>{{offline?'正在等待电脑恢复连接':'已连接电脑'}}</strong><p class="connection-address">{{address}}</p><p>文件由电脑下载。保持拾流运行、电脑唤醒，并连接同一 Wi-Fi。</p><small v-if="lastUpdated">上次更新 {{new Date(lastUpdated).toLocaleTimeString('zh-CN')}}</small><small>拾流 {{version}}</small></div><p class="home-hint">在 Safari 分享菜单中添加到主屏幕。</p><button class="wide" :disabled="busy||!!offline" @click="emit('disconnect')"><LogOut :size="19"/>断开连接</button></section>
</template>
