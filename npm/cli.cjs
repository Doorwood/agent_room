#!/usr/bin/env node
'use strict';
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const {spawn} = require('node:child_process');
const {constants} = require('node:os');

function binaryPath(root, platform = process.platform, architecture = process.arch) {
  const arch = {x64:'amd64', arm64:'arm64'}[architecture];
  if (!['darwin', 'linux'].includes(platform) || !arch) {
    throw new Error(`Unsupported platform ${platform}/${architecture}; agent_room supports macOS/Linux x64 and arm64.`);
  }
  return path.join(root, 'native', `agent_room-${platform}-${arch}`);
}
function verifyBinary(root, platform, architecture) {
  const binary = binaryPath(root, platform, architecture);
  const filename = path.basename(binary);
  const lines = fs.readFileSync(path.join(root, 'native', 'SHA256SUMS'), 'utf8').split('\n');
  const checksums = lines.map(line => /^([a-f0-9]{64})\s+([^\s]+)$/.exec(line)).filter(match => match && match[2] === filename);
  if (checksums.length !== 1) throw new Error(`Missing or ambiguous checksum for ${filename}; reinstall the npm package.`);
  const actual = crypto.createHash('sha256').update(fs.readFileSync(binary)).digest('hex');
  if (actual !== checksums[0][1]) throw new Error(`Binary checksum mismatch for ${filename}; reinstall the npm package.`);
  fs.accessSync(binary, fs.constants.X_OK);
  return binary;
}
function run(root, args) {
  let executable;
  try { executable = verifyBinary(root, process.platform, process.arch); }
  catch (error) {console.error(`agent_room: ${error.message}`);process.exitCode = 1;return;}
  const child = spawn(executable, args, {stdio:'inherit', shell:false});
  const handlers = new Map();
  for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
    const handler = () => {if (child.exitCode === null && child.signalCode === null) child.kill(signal);};
    handlers.set(signal, handler);process.on(signal, handler);
  }
  function cleanup() {for (const [signal,handler] of handlers) process.removeListener(signal,handler);}
  child.once('error', error => {cleanup();console.error(`agent_room: could not launch native client: ${error.message}`);process.exitCode=1;});
  child.once('exit', (code,signal) => {
    cleanup();
    process.exitCode = code ?? (constants.signals[signal] ? 128 + constants.signals[signal] : 1);
  });
}
if (require.main === module) run(path.resolve(__dirname, '..'), process.argv.slice(2));
module.exports = {binaryPath, verifyBinary, run};
