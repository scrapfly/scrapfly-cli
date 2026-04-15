# scrapfly-cli (npm shim)

Installs the [Scrapfly CLI](https://github.com/scrapfly/scrapfly-cli) binary
into `node_modules/scrapfly-cli/vendor` and exposes it as `scrapfly` on your
`$PATH` (via `bin/scrapfly.js`).

```bash
npm install -D scrapfly-cli
npx scrapfly version
```

Set `SCRAPFLY_CLI_VERSION=v0.2.0` to pin a release. Set
`SCRAPFLY_CLI_SKIP_DOWNLOAD=1` to skip the postinstall step (e.g. on CI
runners where the binary is already installed).

See https://scrapfly.io for the platform docs.
