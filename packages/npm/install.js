#!/usr/bin/env node
// Postinstall: download the matching scrapfly binary from GitHub Releases,
// extract it into ./vendor/, and leave bin/scrapfly.js as the entry point.
//
// - Respects $SCRAPFLY_CLI_SKIP_DOWNLOAD=1 (dev / CI).
// - Defaults to this package's `version`; override with
//   $SCRAPFLY_CLI_VERSION="v0.2.0".
// - Uses the same `{os}-{arch}` artifact names the goreleaser job produces.

const fs = require('node:fs');
const path = require('node:path');
const https = require('node:https');
const { pipeline } = require('node:stream/promises');
const { spawnSync } = require('node:child_process');

if (process.env.SCRAPFLY_CLI_SKIP_DOWNLOAD === '1') {
  console.log('[scrapfly-cli] SCRAPFLY_CLI_SKIP_DOWNLOAD=1, skipping binary download');
  process.exit(0);
}

const pkg = require('./package.json');
const version = process.env.SCRAPFLY_CLI_VERSION || `v${pkg.version}`;

const platform = process.platform; // darwin | linux | win32
const arch = process.arch;         // x64 | arm64 | ia32
const mappedArch = arch === 'x64' ? 'amd64' : arch === 'arm64' ? 'arm64' : null;

let asset;
let binName = 'scrapfly';
if (platform === 'darwin' && mappedArch) {
  asset = `scrapfly-macos-${mappedArch}`;
} else if (platform === 'linux' && mappedArch) {
  asset = `scrapfly-linux-${mappedArch}`;
} else if (platform === 'win32' && mappedArch) {
  asset = `scrapfly-windows-${mappedArch}.exe`;
  binName = 'scrapfly.exe';
} else {
  console.error(`[scrapfly-cli] unsupported platform: ${platform}/${arch}`);
  process.exit(1);
}

const url = `https://github.com/scrapfly/scrapfly-cli/releases/download/${version}/${asset}`;
const vendor = path.join(__dirname, 'vendor');
fs.mkdirSync(vendor, { recursive: true });
const binTarget = path.join(vendor, binName);

async function download(u) {
  return new Promise((resolve, reject) => {
    https.get(u, { headers: { 'user-agent': `scrapfly-cli-npm/${pkg.version}` } }, (res) => {
      if (res.statusCode === 301 || res.statusCode === 302) {
        resolve(download(res.headers.location));
        return;
      }
      if (res.statusCode !== 200) {
        reject(new Error(`HTTP ${res.statusCode} fetching ${u}`));
        return;
      }
      resolve(res);
    }).on('error', reject);
  });
}

(async () => {
  console.log(`[scrapfly-cli] downloading ${asset} from ${url}`);
  const res = await download(url);
  await pipeline(res, fs.createWriteStream(binTarget));
  if (platform !== 'win32') {
    fs.chmodSync(binTarget, 0o755);
    // Strip macOS Gatekeeper quarantine attribute so the first run doesn't
    // hit the "Apple could not verify" dialog.
    if (platform === 'darwin') {
      spawnSync('xattr', ['-d', 'com.apple.quarantine', binTarget], { stdio: 'ignore' });
    }
  }
  console.log(`[scrapfly-cli] installed ${binTarget}`);
})().catch((err) => {
  console.error(`[scrapfly-cli] installation failed: ${err.message}`);
  console.error('  fix: re-run with SCRAPFLY_CLI_VERSION=v0.2.0 or download manually');
  console.error('       from https://github.com/scrapfly/scrapfly-cli/releases');
  process.exit(1);
});
