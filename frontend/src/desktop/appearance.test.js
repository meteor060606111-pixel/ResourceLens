import test from 'node:test';
import assert from 'node:assert/strict';
import {normalizeAppearance,readAppearance} from './appearance.js';
test('appearance storage recovers from malformed, inaccessible and old values',()=>{
 const defaults={tint:50,reduceTransparency:false,reduceMotion:false};
 assert.deepEqual(readAppearance({getItem(){throw Error('blocked')}}),defaults);
 assert.deepEqual(readAppearance({getItem(){return '{bad'}}),defaults);
 assert.deepEqual(normalizeAppearance(null),defaults);
 assert.deepEqual(normalizeAppearance({tint:Infinity,reduceMotion:'false'}),defaults);
 assert.deepEqual(normalizeAppearance({tint:120,reduceTransparency:true,reduceMotion:true}),{tint:100,reduceTransparency:true,reduceMotion:true});
 assert.equal(normalizeAppearance({tint:-2}).tint,0);
 assert.equal(normalizeAppearance({tint:35.6}).tint,36);
 assert.equal(readAppearance({getItem(){return '{"tint":23,"reduceMotion":true}'}}).reduceMotion,true);
});
