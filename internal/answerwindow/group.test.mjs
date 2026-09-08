import {test} from 'node:test';
import assert from 'node:assert/strict';
import {groupMessages} from './group.mjs';
const prompt=(seq,owner,turn,ack='已接收，模型正在处理。')=>({seq,owner,turn,ack,role:'user',kind:'prompt',text:'question',clientId:'c'+seq});
const part=(seq,owner,turn,text,kind='progress')=>({seq,owner,turn,text,kind,role:'assistant'});
test('one evolving model card replaces progress with answer',()=>{
 const items=[prompt(1,10,'a'),part(2,10,'a','check inputs'),part(3,10,'a','run tests')];
 let cards=groupMessages(items);assert.equal(cards.length,2);assert.equal(cards[1].text,'run tests');assert.equal(cards[1].working,true);
 const key=cards[1].key;
 items.push(part(4,10,'a','final answer',''));
 cards=groupMessages(items);assert.equal(cards.length,2);assert.equal(cards[1].key,key);assert.equal(cards[1].final,true);assert.equal(cards[1].working,false);assert.deepEqual(cards[1].progress,['check inputs','run tests']);assert.equal(cards[1].text,'final answer');
});
test('interleaved users keep distinct cards and owner filters',()=>{
 const cards=groupMessages([prompt(1,10,'a'),prompt(2,20,'b'),part(3,10,'a','Alice answer',''),part(4,20,'b','Bob answer','')]);
 assert.deepEqual(cards.filter(c=>c.owner===10).map(c=>c.text),['question','Alice answer']);
 assert.deepEqual(cards.filter(c=>c.owner===20).map(c=>c.text),['question','Bob answer']);
});
test('unknown turns do not merge and terminal tasks stop waiting',()=>{
 assert.equal(groupMessages([part(1,undefined,'','one',''),part(2,undefined,'','two','')]).length,2);
 const cards=groupMessages([prompt(1,10,'a','任务已中断。'),part(2,10,'a','partial')]);
 assert.equal(cards[1].working,false);assert.equal(cards[1].text,'任务已中断。');
});
test('multiple final messages from a turn are preserved together',()=>{
 const cards=groupMessages([part(1,10,'a','one',''),part(2,10,'a','two','')]);
 assert.equal(cards.length,1);assert.equal(cards[0].text,'one\n\ntwo');
});
test('evicted prompt still uses independently retained terminal status',()=>{
 const message={...part(99,10,'a','last progress'),taskStatus:'处理失败，请查看原终端的错误提示。'};
 const cards=groupMessages([message]);assert.equal(cards[0].working,false);assert.equal(cards[0].text,message.taskStatus);
});
