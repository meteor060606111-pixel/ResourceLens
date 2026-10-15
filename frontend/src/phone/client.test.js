import {test} from 'node:test';
import assert from 'node:assert/strict';
import {createPhoneAPI} from './client.js';

test('expired and changed connections produce actionable errors, not HTML parsing errors', async () => {
  let revoked = 0;
  const expired = createPhoneAPI({fetcher: async () => new Response('please pair', {status: 401}), onUnauthorized: () => revoked++});
  await assert.rejects(expired('state'), error => error.kind === 'unauthorized' && /配对码/.test(error.message));
  assert.equal(revoked, 1);
  const changed = createPhoneAPI({fetcher: async () => new Response('<h1>Connection rejected</h1>', {status: 403})});
  await assert.rejects(changed('state'), error => error.kind === 'connection' && /二维码/.test(error.message));
});

test('a failed submission is sent once and can never be silently retried', async () => {
  const requests = [];
  const api = createPhoneAPI({fetcher: async (url, options) => {requests.push({url, options}); throw new TypeError('network disconnected');}});
  await assert.rejects(api('download', {id: 'chosen-resource'}), error => error.kind === 'connection');
  assert.equal(requests.length, 1);
  assert.equal(requests[0].options.method, 'POST');
  assert.equal(requests[0].options.credentials, 'same-origin');
  assert.deepEqual(JSON.parse(requests[0].options.body), {id: 'chosen-resource'});
});

test('cancelling stale reads preserves an accepted download request', async () => {
  const calls = [];
  const fetcher = (url, options) => new Promise((resolve, reject) => {
    calls.push({url, options, resolve});
    options.signal.addEventListener('abort', () => reject(new DOMException('cancelled', 'AbortError')), {once: true});
  });
  const api = createPhoneAPI({fetcher});
  const state = api('state');
  const download = api('download', {id: 'selected'});
  api.abortReads();
  await assert.rejects(state, error => error.kind === 'aborted');
  assert.equal(calls[1].options.signal.aborted, false);
  calls[1].resolve(Response.json({ok: true, data: 'job-id'}));
  assert.equal(await download, 'job-id');
  api.close();
});

test('an unresponsive电脑times out and releases the request', async () => {
  let signal;
  const api = createPhoneAPI({timeout: 25, fetcher: (_, options) => new Promise((resolve, reject) => {
    signal = options.signal;
    signal.addEventListener('abort', () => reject(new DOMException('timeout', 'AbortError')), {once: true});
  })});
  await assert.rejects(api('state'), error => error.kind === 'connection' && /连接不到电脑/.test(error.message));
  assert.equal(signal.aborted, true);
  api.close();
});
