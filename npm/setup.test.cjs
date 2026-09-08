const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {spawnSync} = require('node:child_process');
const setup = path.resolve(__dirname, '../scripts/setup.sh');
function fixture(t) {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'agent-room-setup-'));
  t.after(() => fs.rmSync(home, {recursive:true, force:true}));
  const bin = path.join(home, 'mock-bin'); fs.mkdirSync(bin);
  fs.writeFileSync(path.join(bin, 'npm'), `#!/usr/bin/env node
const fs=require('node:fs'),path=require('node:path');
const args=process.argv.slice(2);
if(process.env.FAIL_REGISTRY)process.exit(1);
if(args[0]==='view'){console.log(process.env.LATEST || '0.1.2');process.exit(0);}
if(args[0]!=='install')process.exit(2);
if(process.env.FAIL_INSTALL)process.exit(9);
fs.appendFileSync(path.join(process.env.HOME,'installs'),JSON.stringify(args)+'\\n');
const root=path.join(args[args.indexOf('--prefix')+1],'node_modules/menmu-agent-room');
fs.mkdirSync(path.join(root,'bin'),{recursive:true});
fs.writeFileSync(path.join(root,'package.json'),JSON.stringify({version:args.at(-1).split('@')[1]}));
fs.writeFileSync(path.join(root,'bin/agent_room.cjs'),process.env.BROKEN_BINARY ? 'process.exit(9)' : 'if(process.argv.includes("--version")){console.log(JSON.stringify({version:require("../package.json").version}))}else{console.log(JSON.stringify(process.argv.slice(2)))}');
`, {mode:0o755});
  const env = {...process.env, HOME:home, ZDOTDIR:path.join(home,'zsh'), PATH:bin+':'+process.env.PATH};
  function run(file=setup,args=[],extra={}) {return spawnSync('sh',[file,...args],{env:{...env,...extra},encoding:'utf8'});}
  return {home,env,run,updater:path.join(home,'.local/bin/agent_room-update')};
}
test('setup persists shell PATH, preserves login selection, and is idempotent', t => {
  const f=fixture(t);
  fs.writeFileSync(path.join(f.home,'.profile'),'# existing profile\n');
  assert.equal(f.run().status,0);
  assert.equal(fs.existsSync(path.join(f.home,'.bash_profile')),false);
  assert.equal(f.run().status,0);
  for(const name of ['.profile','.bashrc','zsh/.zprofile','zsh/.zshrc']) {
    assert.equal(fs.readFileSync(path.join(f.home,name),'utf8').split('# agent_room user PATH').length,2);
  }
  assert.equal(fs.readFileSync(path.join(f.home,'installs'),'utf8').trim().split('\n').length,1);
  const launched=spawnSync('sh',['-c','. "$HOME/.profile"; agent_room join "host address" session'],{env:f.env,encoding:'utf8'});
  assert.equal(launched.status,0);assert.deepEqual(JSON.parse(launched.stdout),['join','host address','session']);
});
test('installed updater checks without mutation, upgrades, and skips newer installs', t => {
  const f=fixture(t);assert.equal(f.run().status,0);
  assert.equal(f.run(f.updater,['--check'],{LATEST:'0.1.3'}).status,0);
  assert.equal(fs.readFileSync(path.join(f.home,'installs'),'utf8').trim().split('\n').length,1);
  assert.equal(f.run(f.updater,[],{LATEST:'0.1.3'}).status,0);
  assert.equal(f.run(f.updater,[],{LATEST:'0.1.2'}).status,0);
  assert.equal(fs.readFileSync(path.join(f.home,'installs'),'utf8').trim().split('\n').length,2);
});
test('failed or invalid registry responses leave installation intact', t => {
  const f=fixture(t);assert.equal(f.run().status,0);
  assert.notEqual(f.run(f.updater,[],{FAIL_REGISTRY:'1'}).status,0);
  assert.notEqual(f.run(f.updater,[],{LATEST:'invalid'}).status,0);
  assert.equal(fs.readFileSync(path.join(f.home,'installs'),'utf8').trim().split('\n').length,1);
});
test('setup respects an existing bash login file', t => {
  const f=fixture(t);fs.writeFileSync(path.join(f.home,'.bash_login'),'# login\n');
  assert.equal(f.run().status,0);
  assert.match(fs.readFileSync(path.join(f.home,'.bash_login'),'utf8'),/# agent_room user PATH/);
  assert.equal(fs.existsSync(path.join(f.home,'.bash_profile')),false);
});

test('failed staged updates and concurrent update leave current executable intact', t => {
 const f=fixture(t);assert.equal(f.run().status,0);
 const root=path.join(f.home,'.local/share/agent_room/npm');
 const before=fs.realpathSync(path.join(root,'current'));
 for(const failure of [{FAIL_INSTALL:'1'},{BROKEN_BINARY:'1'}]) {
  const result=f.run(f.updater,[],{LATEST:'0.1.3',...failure});assert.notEqual(result.status,0);
  assert.equal(fs.realpathSync(path.join(root,'current')),before);
  assert.equal(fs.existsSync(path.join(root,'.update-lock')),false);
 }
 fs.mkdirSync(path.join(root,'.update-lock'));
 assert.notEqual(f.run(f.updater,[],{LATEST:'0.1.3'}).status,0);
 assert.equal(fs.realpathSync(path.join(root,'current')),before);
});
test('rollback restores previous verified version and check does not create user files', t => {
 const f=fixture(t);
 assert.equal(f.run(setup,['--check']).status,0);
 assert.equal(fs.existsSync(path.join(f.home,'.local')),false);
 assert.equal(f.run().status,0);
 const root=path.join(f.home,'.local/share/agent_room/npm');
 const old=fs.realpathSync(path.join(root,'current'));
 assert.equal(f.run(f.updater,[],{LATEST:'0.1.3'}).status,0);
 assert.notEqual(fs.realpathSync(path.join(root,'current')),old);
 assert.equal(f.run(f.updater,['--rollback']).status,0);
 assert.equal(fs.realpathSync(path.join(root,'current')),old);
});
test('legacy 0.1.3 installation migrates without deleting old package', t => {
 const f=fixture(t);const old=path.join(f.home,'.local/share/agent_room/npm/node_modules/menmu-agent-room');
 fs.mkdirSync(path.join(old,'bin'),{recursive:true});
 fs.writeFileSync(path.join(old,'package.json'),JSON.stringify({version:'0.1.3'}));
 fs.writeFileSync(path.join(old,'bin/agent_room.cjs'),'console.log("legacy")');
 const result=f.run(setup,[],{LATEST:'1.0.4'});assert.equal(result.status,0,result.stderr);
 assert.equal(JSON.parse(fs.readFileSync(path.join(old,'package.json'))).version,'0.1.3');
 const current=path.join(f.home,'.local/share/agent_room/npm/current/node_modules/menmu-agent-room/package.json');
 assert.equal(JSON.parse(fs.readFileSync(current)).version,'1.0.4');
 assert.equal(f.run(f.updater,['--rollback']).status,0);
});
test('unknown destination executable is not overwritten', t => {
 const f=fixture(t);const bin=path.join(f.home,'.local/bin');fs.mkdirSync(bin,{recursive:true});
 const target=path.join(bin,'agent_room');fs.writeFileSync(target,'my unrelated executable');
 assert.notEqual(f.run().status,0);assert.equal(fs.readFileSync(target,'utf8'),'my unrelated executable');
});
