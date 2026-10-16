import {test} from 'node:test';
import assert from 'node:assert/strict';
import {pollDelay,plannedLabel} from './helpers.js';

test('phone polling follows activity on other pages and backs off while idle',()=>{
  const state={jobs:[],batches:[],activity:true};
  assert.equal(pollDelay(state),1500);
  assert.equal(pollDelay({...state,activity:false}),10000);
  assert.equal(pollDelay(state,10),15000);
});

test('phone explains queued appointments and a paused Mac queue',()=>{
  assert.match(plannedLabel({status:'queued',scheduledAt:new Date(Date.now()+3600000).toISOString()},true),/^预约 /);
  assert.equal(plannedLabel({status:'queued'},true),'等待队列继续');
  assert.equal(plannedLabel({status:'queued'},false),'等待电脑下载');
  assert.equal(plannedLabel({status:'paused'},false),'已暂停');
});
