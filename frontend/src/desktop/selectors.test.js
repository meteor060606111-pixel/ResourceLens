import test from 'node:test';
import assert from 'node:assert/strict';
import {sortResources, matchesQuery, filterJobs, normalizedSettings, shortcutAction} from './selectors.js';
import {percent, size} from './formatters.js';

test('sorting keeps backend records immutable and gives stable order after a poll', () => {
  const rows = [{id: 'b', name: '视频10', size: 100, capturedAt: '2026-01-01'}, {id: 'a', name: '视频2', size: 200, capturedAt: '2026-01-01'}, {id: 'c', name: '视频1', size: -1, capturedAt: '2026-02-01'}, {id: 'd', name: '空文件', size: 0, capturedAt: '2025-01-01'}];
  const before = structuredClone(rows);
  assert.deepEqual(sortResources(rows, 'recent').map(r => r.id), ['c', 'a', 'b', 'd']);
  assert.deepEqual(sortResources(rows, 'size').map(r => r.id), ['a', 'b', 'd', 'c']);
  const names = sortResources(rows.slice(0, 3), 'name').map(r => r.name);
  assert.deepEqual(names, ['视频1', '视频2', '视频10']);
  assert.deepEqual(rows, before);
  assert.deepEqual(sortResources(rows.slice().reverse(), 'recent'), sortResources(rows, 'recent'));
});

test('task search composes with status filters and tolerates old missing metadata', () => {
  const jobs = [{id: '1', name: 'CITY.mp4', file: '/Downloads/旅行/CITY.mp4', status: 'downloading'}, {id: '2', name: 'City.mp4', status: 'completed'}, {id: '3', status: 'paused'}, {id: '4', name: 'city2.mp4', status: 'merging'}];
  assert.deepEqual(filterJobs(jobs, 'active', 'city').map(j => j.id), ['1', '4']);
  assert.deepEqual(filterJobs(jobs, 'all', ' CITY  旅行 ').map(j => j.id), ['1']);
  assert.deepEqual(filterJobs(jobs, 'completed', 'city').map(j => j.id), ['2']);
  assert.equal(filterJobs(jobs, 'paused', 'city').length, 0);
  assert.equal(filterJobs(jobs, 'paused', '').length, 1);
  assert.equal(matchesQuery({name: '海边日落.mp4', author: '测试'}, '海边 测试', ['name', 'author']), true);
});

test('settings dirty comparison normalizes select strings, legacy defaults and key order', () => {
  assert.deepEqual(normalizedSettings({segments: '8', concurrent: '3', https: true}), normalizedSettings({https: true, concurrent: 3, segments: 8}));
  assert.notDeepEqual(normalizedSettings({segments: 8}), normalizedSettings({segments: 4}));
  assert.equal(normalizedSettings({https: false}).https, false);
  assert.equal(normalizedSettings({onboardingDone: false}).onboardingDone, false);
});

test('keyboard shortcuts leave dialogs, IME composition and OS modifier chords alone', () => {
  const key = (name, extra = {}) => ({key: name, ctrlKey: true, ...extra});
  assert.equal(shortcutAction(key('n')), 'add');
  assert.equal(shortcutAction(key('F', {ctrlKey: false, metaKey: true})), 'search');
  assert.equal(shortcutAction(key('2')), 'downloads');
  assert.equal(shortcutAction(key('5')), 'music');
  for (const event of [key('n', {shiftKey: true}), key('n', {altKey: true}), key('n', {isComposing: true}), key('n', {repeat: true}), key('n', {defaultPrevented: true}), key('c'), key('n', {ctrlKey: false})]) assert.equal(shortcutAction(event), '');
  assert.equal(shortcutAction(key('n'), true), '');
});

test('transfer values never claim completion before validation or format invalid sizes', () => {
  assert.equal(percent({status: 'validating', total: 10, done: 10}), 99);
  assert.equal(percent({status: 'completed', total: 0, done: 0}), 100);
  assert.equal(percent({status: 'failed', percent: -5}), 0);
  assert.equal(size(-1), '未知');
  assert.equal(size(NaN), '未知');
  assert.equal(size('0'), '0 B');
  assert.equal(size(1048576), '1.0 MB');
});
