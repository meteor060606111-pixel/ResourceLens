<script setup>
import {ref,watch,computed} from 'vue';
import {Folder,Download,Settings,LogOut,Monitor,AlertCircle,CheckCircle2,X,LoaderCircle} from 'lucide-vue-next';
import {usePhone} from './usePhone';
import PairPanel from './PairPanel.vue';import ResourcePanel from './ResourcePanel.vue';import DownloadPanel from './DownloadPanel.vue';import PreviewDialog from './PreviewDialog.vue';
const {paired,loading,offline,busy,notice,noticeError,lastUpdated,state,api,toast,refresh,reconnect,run,pair,disconnect}=usePhone();
const actionBusy=computed(()=>busy.value||Boolean(offline.value));
const page=ref('resources'),qualities=ref({}),formats=ref({}),qualityResource=ref(null),qualityLoading=ref(false),preview=ref(null);
const nav=[{id:'resources',label:'资源',icon:Folder},{id:'downloads',label:'下载',icon:Download},{id:'connection',label:'连接',icon:Settings}];
const previewType=computed(()=>state.value.resources.find(r=>r.id===preview.value?.resourceId)?.type||'video');
watch(paired,v=>{if(!v){page.value='resources';preview.value=null;qualityResource.value=null}});
async function add(urls,clear){await run(async()=>{if(!urls.length)throw Error('请先粘贴有效的 http 或 https 链接');if(urls.length>20)throw Error('每次最多添加 20 条链接');await api('add',{urls});clear();toast('已提交链接，Mac 正在解析')})}
async function download(id,force=false,quality){await run(async()=>{try{await api('download',{id,force,quality:quality||qualities.value[id]||'best'});page.value='downloads';toast('已加入 Mac 下载队列')}catch(e){if(e.message.startsWith('DUPLICATE:')){if(window.confirm('内容已经下载过，是否重新下载？')){await api('download',{id,force:true,quality:quality||qualities.value[id]||'best'});page.value='downloads'}}else throw e}})}
async function downloadMany(ids,settle){await run(async()=>{
  const unique=[...new Set(ids)];if(!unique.length||unique.length>20)throw Error('每次请选择 1 至 20 个资源');
  const handled=[];let queued=0,skipped=0,failed=0;
  for(const id of unique){
    try{await api('download',{id,quality:qualities.value[id]||'best'});queued++;handled.push(id)}
    catch(error){
      if(error.message.startsWith('DUPLICATE:')){skipped++;handled.push(id);continue}
      failed++;
      if(['connection','unauthorized'].includes(error.kind)){settle(handled);throw ErrorWithSummary(error,queued)}
    }
  }
  settle(handled);
  if(queued&&!failed)selectPage('downloads');
  toast(!queued&&skipped&&!failed?`所选内容已下载，已跳过 ${skipped} 项`:`已加入 ${queued} 个下载任务${skipped?`，跳过 ${skipped} 个已下载内容`:''}${failed?`，${failed} 项未成功，可重新选择重试`:''}`,failed>0);
})}
function ErrorWithSummary(error,queued){error.message=(queued?`已加入 ${queued} 个任务。请重连后核对实际队列；`:'')+error.message;return error}
function jobAction(action,id){void run(async()=>{await api(action,{id});toast(action==='pause'?'已暂停，进度保留':action==='cancel'?'任务已取消':'已加入续传队列')})}
let qualityRequest=0;
async function showQuality(r){const request=++qualityRequest;qualityResource.value=r;qualityLoading.value=true;try{const result=await api('formats',{id:r.id});if(request===qualityRequest&&qualityResource.value?.id===r.id)formats.value[r.id]=result}catch(e){if(request===qualityRequest){toast(e.message,true);qualityResource.value=null}}finally{if(request===qualityRequest)qualityLoading.value=false}}
function selectQuality(id){qualities.value[qualityResource.value.id]=id;qualityResource.value=null}
function selectPage(id){page.value=id;window.scrollTo({top:0,behavior:'instant'})}
</script>
<template><div class="phone-shell" :class="{unpaired:!paired}"><header class="phone-header"><a class="brand" href="/" aria-label="拾流首页"><svg viewBox="0 0 56 44" aria-hidden="true"><path d="M3 22C16 6 21 4 31 18S43 34 53 20M3 35C16 19 21 17 31 31S43 47 53 33" fill="none" stroke="currentColor" stroke-width="6" stroke-linecap="round"/></svg><span>拾流</span></a><div v-if="paired" class="connection-indicator"><i :class="{offline:offline}"/><span>{{offline?'Mac 已离线':'已连接 Mac'}}</span><button class="icon-button" :disabled="busy" @click="disconnect" aria-label="断开 Mac 连接"><LogOut :size="22"/></button></div><span v-else class="connect-label"><Monitor :size="19"/>连接 Mac</span></header>
  <div v-if="offline" class="offline-banner" role="alert"><AlertCircle :size="20"/><span>{{offline}}</span><button @click="reconnect" :disabled="loading">重连</button></div>
  <main><PairPanel v-if="!paired||page==='connection'" :paired="paired" :busy="busy" :loading="loading" :version="state.version" :last-updated="lastUpdated" :offline="offline" @pair="pair" @disconnect="disconnect"/><ResourcePanel v-else-if="page==='resources'" :resources="state.resources" :jobs="state.jobs" :batches="state.batches" :busy="actionBusy" :qualities="qualities" :formats="formats" @add="add" @download="download" @batch="downloadMany" @quality="showQuality" @refresh="refresh"/><DownloadPanel v-else :resources="state.resources" :jobs="state.jobs" :busy="actionBusy" @pause="id=>jobAction('pause',id)" @resume="id=>jobAction('resume',id)" @cancel="id=>jobAction('cancel',id)" @retry="j=>download(j.resourceId,false,j.requestedQuality)" @preview="j=>preview=j" @saving="toast('保存后可在 Safari 下载列表中打开文件')"/></main>
  <nav v-if="paired" class="bottom-nav" aria-label="手机端导航"><button v-for="item in nav" :key="item.id" :class="{selected:page===item.id}" :aria-current="page===item.id?'page':undefined" @click="selectPage(item.id)"><component :is="item.icon" :size="25"/><span>{{item.label}}</span></button></nav>
  <div v-if="notice" class="phone-toast" :class="{error:noticeError}" role="status"><component :is="noticeError?AlertCircle:CheckCircle2" :size="20"/><span>{{notice}}</span></div>
  <PreviewDialog v-if="preview" :job="preview" :type="previewType" @close="preview=null"/>
  <div v-if="qualityResource" class="dialog-overlay" @click.self="qualityResource=null" @keydown.esc="qualityResource=null"><section class="phone-dialog quality-dialog" role="dialog" aria-modal="true" aria-label="选择画质"><header><h2>选择画质</h2><button class="icon-button" aria-label="关闭画质选择" @click="qualityResource=null"><X :size="23"/></button></header><p v-if="qualityLoading"><LoaderCircle :size="20" class="spin"/>Mac 正在检查可用画质…</p><button v-for="f in formats[qualityResource.id]||[]" :key="f.id" class="quality-choice wide" :disabled="f.available===false" @click="selectQuality(f.id)">{{f.label||f.id}}<span v-if="f.available===false">不可用</span><CheckCircle2 v-else-if="f.id===(qualities[qualityResource.id]||'best')" :size="20"/></button></section></div>
</div></template>
