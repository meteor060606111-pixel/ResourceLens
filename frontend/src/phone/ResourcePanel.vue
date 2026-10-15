<script setup>
import {ref,computed,watch} from 'vue';
import {RefreshCw,LoaderCircle,ChevronDown,Link2,Search,Square,SquareCheck} from 'lucide-vue-next';
import MediaThumb from './MediaThumb.vue';
import {size,inputLinks,activeStatus,matchesSearch} from './helpers';
const props=defineProps({resources:Array,jobs:Array,batches:Array,busy:Boolean,qualities:Object,formats:Object});
const emit=defineEmits(['add','download','refresh','quality','batch']);
const text=ref(''),search=ref(''),filter=ref('all'),selecting=ref(false),selected=ref([]);
const links=computed(()=>inputLinks(text.value));
const filters=[{id:'all',label:'全部'},{id:'video',label:'视频'},{id:'audio',label:'音频'},{id:'image',label:'图片'},{id:'file',label:'文件'}];
const shown=computed(()=>props.resources.filter(r=>matchesSearch(r,search.value)&&(filter.value==='all'||(filter.value==='video'?['video','playlist'].includes(r.type):filter.value==='file'?['file','other'].includes(r.type):r.type===filter.value))));
const latest=computed(()=>(props.batches||[]).slice().sort((a,b)=>new Date(b.createdAt)-new Date(a.createdAt))[0]);
const labels={video:'视频',playlist:'视频',audio:'音频',image:'图片',file:'文件',other:'文件'};
function add(){emit('add',inputLinks(text.value),()=>text.value='')}
function queued(id){return props.jobs.some(j=>j.resourceId===id&&(activeStatus(j.status)||j.status==='paused'))}
function queueLabel(id){return props.jobs.some(j=>j.resourceId===id&&activeStatus(j.status))?'队列中':props.jobs.some(j=>j.resourceId===id&&j.status==='paused')?'已暂停':'下载'}
function eligible(r){return !r.captureRequired&&!queued(r.id)}
function toggle(id){selected.value=selected.value.includes(id)?selected.value.filter(item=>item!==id):selected.value.length<20?[...selected.value,id]:selected.value}
function selectShown(){selected.value=shown.value.filter(eligible).slice(0,20).map(r=>r.id)}
function clearSelection(){selecting.value=false;selected.value=[]}
function batch(){emit('batch',[...selected.value],ids=>selected.value=selected.value.filter(id=>!ids.includes(id)))}
watch(()=>[props.resources,props.jobs],()=>{const available=new Set(props.resources.filter(eligible).map(r=>r.id));selected.value=selected.value.filter(id=>available.has(id))});
function quality(r){const q=props.qualities[r.id];return props.formats[r.id]?.find(f=>f.id===q)?.label||r.quality||(r.height?r.height+'p':'最佳画质')}
</script>
<template><section class="resource-panel"><h1>保存你喜欢的内容</h1>
  <form @submit.prevent="add" class="add-form"><label for="share-link">视频链接或分享文本</label><textarea id="share-link" v-model="text" placeholder="粘贴分享链接，自动提取网址。可一次粘贴多条。" rows="3" maxlength="16384" :disabled="busy"/><p v-if="text.trim()" class="link-count" :class="{warning:links.length>20}" role="status">{{links.length?`已识别 ${links.length} 条链接`:'还没有识别到有效链接'}} · 每次最多 20 条</p><button class="primary wide" :disabled="busy||!links.length||links.length>20"><LoaderCircle v-if="busy" :size="20" class="spin"/>{{links.length>1?`添加 ${links.length} 条链接`:'添加链接'}}</button></form>
  <div v-if="latest" class="parse-status" role="status"><LoaderCircle v-if="latest.status==='parsing'" :size="17" class="spin"/><span>{{latest.status==='parsing'?'电脑正在解析链接…':latest.items.some(i=>i.status==='failed')?'部分链接解析失败，可在电脑上查看详情':'链接解析完成'}}</span></div>
  <div class="section-heading"><h2>可下载资源</h2><button class="icon-button" aria-label="刷新资源" :disabled="busy" @click="emit('refresh')"><RefreshCw :size="23"/></button></div>
  <template v-if="resources.length"><label class="search-field"><Search :size="19"/><span class="sr-only">搜索资源</span><input v-model="search" type="search" placeholder="搜索名称、来源或作者" aria-label="搜索资源"/></label><div class="type-filters" role="group" aria-label="筛选资源类型"><button v-for="f in filters" :key="f.id" :class="{selected:filter===f.id}" :aria-pressed="filter===f.id" @click="filter=f.id">{{f.label}}</button></div><div class="list-tools"><span>{{shown.length}} 个资源</span><button v-if="!selecting" class="text-button" :disabled="busy" @click="selecting=true">批量选择</button><template v-else><button class="text-button" :disabled="busy" @click="selectShown">选择当前结果</button><button class="text-button" :disabled="busy" @click="clearSelection">取消选择</button></template></div></template>
  <div v-if="selecting" class="batch-toolbar"><span>已选 {{selected.length}} / 20 项</span><button class="primary" :disabled="busy||!selected.length" @click="batch">下载所选</button></div>
  <ul class="media-list" v-if="shown.length"><li v-for="r in shown" :key="r.id" class="resource-row" :class="{selecting}"><button v-if="selecting" class="resource-select icon-button" :aria-label="'选择 '+r.name" :aria-pressed="selected.includes(r.id)" :disabled="busy||!eligible(r)||(!selected.includes(r.id)&&selected.length>=20)" @click="toggle(r.id)"><component :is="selected.includes(r.id)?SquareCheck:Square" :size="23"/></button><MediaThumb :resource="r"/><div class="resource-detail"><h3 :title="r.name">{{r.name}}</h3><p>{{labels[r.type]||'文件'}}<template v-if="r.height"> · {{r.height}}p</template></p><p>{{size(r.size)}}</p><button v-if="['video','playlist'].includes(r.type)&&!r.captureRequired" class="quality-button" :disabled="busy" @click="emit('quality',r)">{{quality(r)}}<ChevronDown :size="13"/></button><p v-if="r.captureRequired" class="capture-note">请先在电脑捕获播放内容</p></div><button v-if="!selecting" class="primary download-button" :disabled="busy||!eligible(r)" @click="emit('download',r.id)">{{queueLabel(r.id)}}</button></li></ul>
  <div v-else class="empty-state"><Link2 :size="30"/><strong>{{resources.length?'没有匹配的资源':'还没有可下载资源'}}</strong><p>{{resources.length?'试试其他关键词或资源类型。':'粘贴分享链接，电脑解析后会显示在这里。'}}</p></div>
</section></template>
