#!/usr/bin/env node
// Thin shim that execs the downloaded binary with the caller's argv. Keeps
// stdio inherited so pipes work unchanged.

const path = require('node:path');
const { spawn } = require('node:child_process');

const isWindows = process.platform === 'win32';
const binary = path.join(__dirname, '..', 'vendor', isWindows ? 'scrapfly.exe' : 'scrapfly');

const child = spawn(binary, process.argv.slice(2), { stdio: 'inherit', windowsHide: true });

// Forward signals so Ctrl-C / kill propagate cleanly.
for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  process.on(sig, () => {
    try { child.kill(sig); } catch (_) {}
  });
}

child.on('error', (err) => {
  if (err.code === 'ENOENT') {
    console.error('[scrapfly] binary not found — re-run `npm install scrapfly-cli` or set SCRAPFLY_CLI_VERSION');
  } else {
    console.error(`[scrapfly] ${err.message}`);
  }
  process.exit(1);
});

child.on('exit', (code, signal) => {
  if (signal) {
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code ?? 0);
});
