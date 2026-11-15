# scrapfly-cli (npm shim)

Installs the [Scrapfly CLI](https://github.com/scrapfly/scrapfly-cli) binary
into `node_modules/scrapfly-cli/vendor` and exposes it as `scrapfly` on your
`$PATH` (via `bin/scrapfly.js`).

```bash
npm install -D scrapfly-cli
npx scrapfly version
```

0.5.0 and later need macOS 13 or later; on macOS 12 install
`scrapfly-cli@0.4.1`.

Set `SCRAPFLY_CLI_VERSION=v0.2.0` to pin a release. Set
`SCRAPFLY_CLI_SKIP_DOWNLOAD=1` to skip the postinstall step (e.g. on CI
runners where the binary is already installed).

See https://scrapfly.io for the platform docs.
