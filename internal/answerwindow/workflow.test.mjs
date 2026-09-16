import {test} from 'node:test';
import assert from 'node:assert/strict';
import {workflowMessage} from './workflow.mjs';
test('workflow preserves explicit fan-in and dotted members',()=>{
 const steps=[{id:'review',agent:'claude.a',prompt:'review',dependsOn:['build','tests']},{id:'build',agent:'codex.b',prompt:'implement',dependsOn:[]},{id:'tests',agent:'codex.b',prompt:'test',dependsOn:[]}];
 assert.deepEqual(JSON.parse(workflowMessage(steps).slice(6)),{steps});
});
test('workflow rejects cyclic and missing dependencies',()=>{
 const a={id:'a',agent:'codex',prompt:'work',dependsOn:['b']};
 assert.throws(()=>workflowMessage([a]),/依赖/);
 assert.throws(()=>workflowMessage([a,{...a,id:'b',dependsOn:['a']}]),/循环/);
 assert.throws(()=>workflowMessage([a,a]),/唯一/);
});
