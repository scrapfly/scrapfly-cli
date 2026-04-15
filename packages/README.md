# Package wrappers

The canonical distribution of `scrapfly-cli` is the Go binary shipped by the
[Releases](https://github.com/scrapfly/scrapfly-cli/releases) pipeline. The
wrapper here makes the CLI reachable from ecosystems that already have
dependency managers wired up:

| Path | Ecosystem | Install |
|---|---|---|
| [`npm/`](npm) | npm / pnpm / yarn | `npm install -D scrapfly-cli` |

The wrapper is tiny: it downloads the matching `{os}-{arch}` tarball from
GitHub Releases on install, caches it, and exec's it with the caller's
argv. No Go toolchain required on the user's machine.

## Publishing

`.github/workflows/publish.yml` publishes on each `v*` tag push (and via
workflow_dispatch for a re-publish). The tag version is injected into
`package.json` before publish, so the package version always matches the
Go binary.

Auth: `NPM_AUTOMATION_TOKEN` secret. Published with `--provenance` so the
npm registry records an OIDC-signed statement of the build's source.

See the top-level README for the first-time setup steps (org creation,
token generation, Trusted Publisher as an alternative).
