import {mkdirSync, mkdtempSync, rmSync, copyFileSync, chmodSync, readFileSync, writeFileSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import path from 'node:path';
import {spawnSync} from 'node:child_process';
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
function run(command,args,options={}) {
  const result=spawnSync(command,args,{cwd:root,stdio:'inherit',...options});
  if (result.error) throw result.error;
  if (result.status!==0) throw new Error(`${command} failed (${result.status ?? result.signal})`);
}
run('sh',['scripts/package.sh']);
const manifest=JSON.parse(readFileSync(path.join(root,'npm/package.json'),'utf8'));
// Check target metadata on all binaries; the shared build passes the same version
// to each target. Run the local binary to verify the version at runtime.
for (const os of ['darwin','linux']) for (const arch of ['amd64','arm64']) {
 const binary=path.join(root,'dist',`agent_room-${os}-${arch}`);
 const metadata=spawnSync('go',['version','-m',binary],{encoding:'utf8'});
 if(metadata.status!==0 || (!metadata.stdout.includes(`GOOS=${os}`) || !metadata.stdout.includes(`GOARCH=${arch}`))) throw new Error(`Platform metadata mismatch: ${binary}`);
 if(os===process.platform && arch===({x64:'amd64',arm64:'arm64'}[process.arch])) {
  const version=spawnSync(binary,['--version','--json'],{encoding:'utf8'});
  if(version.status!==0 || JSON.parse(version.stdout).version!==manifest.version)throw new Error('Native version does not match npm manifest');
 }
}
// Exact allowlist staging prevents workspace docs, credentials and state from
// entering the npm artifact. A unique staging directory avoids stale files.
const output=path.join(root,'dist/npm');mkdirSync(output,{recursive:true});
const stage=mkdtempSync(path.join(output,'stage-'));
try {
mkdirSync(path.join(stage,'bin'));mkdirSync(path.join(stage,'native'));mkdirSync(path.join(stage,'scripts'));
copyFileSync(path.join(root,'scripts/setup.sh'),path.join(stage,'scripts/setup.sh'));
chmodSync(path.join(stage,'scripts/setup.sh'),0o755);
writeFileSync(path.join(stage,'package.json'),JSON.stringify(manifest,null,2)+'\n');
copyFileSync(path.join(root,'npm/README.md'),path.join(stage,'README.md'));
copyFileSync(path.join(root,'LICENSE'),path.join(stage,'LICENSE'));
copyFileSync(path.join(root,'THIRD_PARTY_NOTICES.md'),path.join(stage,'THIRD_PARTY_NOTICES.md'));
copyFileSync(path.join(root,'npm/cli.cjs'),path.join(stage,'bin/agent_room.cjs'));
chmodSync(path.join(stage,'bin/agent_room.cjs'),0o755);
for (const os of ['darwin','linux']) for (const arch of ['amd64','arm64']) {
  const name=`agent_room-${os}-${arch}`;
  copyFileSync(path.join(root,'dist',name),path.join(stage,'native',name));
  chmodSync(path.join(stage,'native',name),0o755);
}
copyFileSync(path.join(root,'dist/SHA256SUMS'),path.join(stage,'native/SHA256SUMS'));
run('npm',['pack','--ignore-scripts','--pack-destination',output],{cwd:stage});
} finally {rmSync(stage,{recursive:true,force:true});}
console.log(`\nInstall: npm install -g ${path.join(output,manifest.name+'-'+manifest.version+'.tgz')}`);
