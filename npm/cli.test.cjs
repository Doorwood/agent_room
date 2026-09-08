'use strict';
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const os=require('node:os');
const path=require('node:path');
const crypto=require('node:crypto');
const {binaryPath,verifyBinary}=require('./cli.cjs');
function fixture(t){const root=fs.mkdtempSync(path.join(os.tmpdir(),'agent-room-npm-'));t.after(()=>fs.rmSync(root,{recursive:true,force:true}));fs.mkdirSync(path.join(root,'native'));return root;}
test('platform mapping rejects unsupported combinations',()=>{
 assert.equal(binaryPath('/pkg','darwin','x64'),'/pkg/native/agent_room-darwin-amd64');
 assert.equal(binaryPath('/pkg','linux','arm64'),'/pkg/native/agent_room-linux-arm64');
 assert.throws(()=>binaryPath('/pkg','win32','x64'),/Unsupported/);
 assert.throws(()=>binaryPath('/pkg','linux','ia32'),/Unsupported/);
});
test('integrity verification detects corrupted and ambiguous binaries',t=>{
 const root=fixture(t),binary=binaryPath(root,'linux','x64');fs.writeFileSync(binary,'native binary',{mode:0o755});
 const sum=crypto.createHash('sha256').update('native binary').digest('hex');
 const line=sum+'  '+path.basename(binary)+'\n';
 fs.writeFileSync(path.join(root,'native/SHA256SUMS'),line);
 assert.equal(verifyBinary(root,'linux','x64'),binary);
 fs.writeFileSync(binary,'tampered');assert.throws(()=>verifyBinary(root,'linux','x64'),/checksum mismatch/);
 fs.writeFileSync(path.join(root,'native/SHA256SUMS'),line+line);assert.throws(()=>verifyBinary(root,'linux','x64'),/ambiguous/);
});
function executableFixture(t,source){
 const root=fixture(t);fs.mkdirSync(path.join(root,'bin'));fs.copyFileSync(path.join(__dirname,'cli.cjs'),path.join(root,'bin/agent_room.cjs'));
 const binary=binaryPath(root);fs.writeFileSync(binary,source,{mode:0o755});
 const sum=crypto.createHash('sha256').update(source).digest('hex');
 fs.writeFileSync(path.join(root,'native/SHA256SUMS'),sum+'  '+path.basename(binary)+'\n');return root;
}
test('launcher preserves arguments, cwd and exit code without a shell',t=>{
 const root=executableFixture(t,'#!/bin/sh\nprintf \'%s\\n\' "$PWD" "$@"\nexit 7\n');
 const result=require('node:child_process').spawnSync(process.execPath,[path.join(root,'bin/agent_room.cjs'),'two words','$(never execute)','--name','alice'],{cwd:root,encoding:'utf8'});
 assert.equal(result.status,7);assert.deepEqual(result.stdout.trim().split('\n'),[fs.realpathSync(root),'two words','$(never execute)','--name','alice']);
});
test('launcher forwards SIGTERM to child and reports its exit',async t=>{
 const root=executableFixture(t,'#!/bin/sh\ntrap "exit 23" TERM\nprintf "ready\\n"\nwhile :; do sleep 0.1; done\n');
 const {spawn}=require('node:child_process');const child=spawn(process.execPath,[path.join(root,'bin/agent_room.cjs')],{stdio:['ignore','pipe','pipe']});
 t.after(()=>{if(child.exitCode===null)child.kill('SIGKILL');});
 const timer=setTimeout(()=>child.kill('SIGKILL'),5000);t.after(()=>clearTimeout(timer));
 const exited=new Promise((resolve,reject)=>{child.once('exit',(code,signal)=>resolve({code,signal}));child.once('error',reject);});
 await new Promise((resolve,reject)=>{child.stdout.once('data',resolve);child.once('error',reject);child.once('exit',()=>reject(new Error('exited before ready')));});
 child.kill('SIGTERM');const result=await exited;assert.equal(result.code,23);assert.equal(result.signal,null);
});
test('launcher forwards subsequent signals while child is still alive',async t=>{
 const root=executableFixture(t,'#!/bin/sh\ntrap \'printf "term\\n"\' TERM\ntrap "exit 42" HUP\nprintf "ready\\n"\nwhile :; do sleep 0.1; done\n');
 const child=require('node:child_process').spawn(process.execPath,[path.join(root,'bin/agent_room.cjs')],{detached:true,stdio:['ignore','pipe','pipe']});
 const cleanup=()=>{try{process.kill(-child.pid,'SIGKILL');}catch{}};
 t.after(cleanup);const timer=setTimeout(cleanup,5000);t.after(()=>clearTimeout(timer));
 let readyResolve,termResolve;
 const ready=new Promise(resolve=>{readyResolve=resolve;});const term=new Promise(resolve=>{termResolve=resolve;});
 let output='';child.stdout.on('data',chunk=>{output+=chunk;if(output.includes('ready'))readyResolve();if(output.includes('term'))termResolve();});
 const exited=new Promise((resolve,reject)=>{child.once('exit',(code,signal)=>resolve({code,signal}));child.once('error',reject);});
 await Promise.race([ready,exited.then(()=>{throw new Error('exited before ready');})]);child.kill('SIGTERM');
 await Promise.race([term,exited.then(()=>{throw new Error('exited before TERM');})]);child.kill('SIGHUP');
 assert.deepEqual(await exited,{code:42,signal:null});
});
test('signal exits retain POSIX status',t=>{
 const root=executableFixture(t,'#!/bin/sh\nkill -KILL $$\n');
 const result=require('node:child_process').spawnSync(process.execPath,[path.join(root,'bin/agent_room.cjs')],{encoding:'utf8'});
 assert.equal(result.status,137);
});
