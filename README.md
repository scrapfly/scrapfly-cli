<p align="center">
  <a href="https://scrapfly.io"><img src="https://scrapfly.io/logo.svg" width="120" alt="Scrapfly" /></a>
</p>

<h1 align="center">scrapfly-cli</h1>

<p align="center">
  <b>The agentic CLI for web data and browser automation.</b><br/>
  Scrape, extract, and drive a cloud browser over CDP from any LLM
  tool-use loop. JSON-first, pipe-friendly, one static binary.
</p>

<p align="center">
  <a href="https://github.com/scrapfly/scrapfly-cli/releases"><img src="https://img.shields.io/github/v/release/scrapfly/scrapfly-cli?label=release" alt="Release"/></a>
  <a href="https://scrapfly.io/docs"><img src="https://img.shields.io/badge/docs-scrapfly.io-0051ff" alt="Docs"/></a>
  <a href="https://github.com/scrapfly/scrapfly-cli/blob/main/LICENSE"><img src="https://img.shields.io/github/license/scrapfly/scrapfly-cli" alt="License"/></a>
</p>

---

`scrapfly` is the official CLI for the [Scrapfly](https://scrapfly.io)
platform. It is built **agent-first**: every verb returns a stable JSON
envelope, every product is a tool an LLM can call, and an autonomous
agent is baked in.

- **Agentic by design**: `scrapfly agent "<task>"` runs a Playwright-MCP
  style loop against a cloud browser. Providers ship out of the box for
  Anthropic, OpenAI, Gemini, and any OpenAI-compatible endpoint (Ollama,
  vLLM). For any other tool-use loop, install the [`scrapfly-cli` agent
  skill](https://github.com/scrapfly/skills/tree/main/skills/scrapfly-cli) — see
  [For LLM agents](#for-llm-agents) below.
- **Full product surface**: Web Scraping, Screenshot, Extraction, Crawler,
  and the CDP-driven Browser, each with complete SDK-parity flags.
- **Pipe-friendly**: stable envelope (`{success, product, data|error}`),
  `--content-only`/`--data-only` for single-line piping, ndjson for
  batch runs, `--pretty` for humans.
- **Composable**: multi-call browser sessions via a Unix-socket daemon
  (cookies + AXTree refs persist across invocations), WARC/HAR in and
  out, `scrapfly browser` outputs a CDP URL you can hand to Playwright,
  Puppeteer, or browser-use.

## Install

One-liner (macOS / Linux):

```bash
curl -fsSL https://scrapfly.io/scrapfly-cli/install | sh
```

Pinned version or custom prefix:

```bash
curl -fsSL https://scrapfly.io/scrapfly-cli/install | sh -s -- --version v0.1.0 --prefix ~/.local/bin
```

Or grab a release tarball directly from
[Releases](https://github.com/scrapfly/scrapfly-cli/releases) - artifacts
follow `scrapfly-{os}-{arch}.tar.gz` (`scrapfly-windows-amd64.zip` on
Windows). Supported: `darwin-universal`, `darwin-amd64`, `darwin-arm64`,
`linux-amd64`, `linux-arm64`, `windows-amd64`. The macOS builds need macOS 13
Ventura or later; on macOS 12 Monterey, install v0.4.1, the last release for
it (`--version v0.4.1`), and do not run `scrapfly update` there: 0.4.1 installs
a release that does not start on macOS 12.

From source (Go 1.27+):

```bash
go install github.com/scrapfly/scrapfly-cli/cmd/scrapfly@latest
```

Or from a package manager:

```bash
# npm / pnpm / yarn
npm install -D scrapfly-cli
npx scrapfly version
```

### macOS Gatekeeper

The macOS binaries are signed with Scrapfly's Apple Developer ID and notarized
by Apple, so they run without a Gatekeeper prompt. Check a download with:

```bash
codesign --verify --strict --verbose=2 /path/to/scrapfly
```

Releases up to 0.4.0 are unsigned and may hit *"Apple could not verify
'scrapfly' is free of malware"* on first run after a browser download. Run
once:

```bash
xattr -d com.apple.quarantine /path/to/scrapfly
```

### Self-update

Once installed, keep the binary current without re-running the installer:

```bash
scrapfly update           # upgrade in place to the latest GitHub release
scrapfly update --check   # just print whether a newer version exists
```

Concurrent updates to the same installation are refused while a binary swap is
in progress. An empty `<executable>.update.lock` file (normally `scrapfly.update.lock`) remains
beside the executable. Leave
it in place, including after an interrupted update. The operating system releases
the lock when the updater exits. Older CLI versions do not honor this lock.
The `.bak` suffix is reserved for recovery. An updater running from a backup
refuses to install. Restore the backup under its original name before updating.

To pin a specific release, pass `--version` to the installer:

```bash
curl -fsSL https://scrapfly.io/scrapfly-cli/install | sh -s -- --version v0.3.0
```

The binary always comes from the GitHub release + `checksums.txt`; the
release-notes feed is only used for the human-readable changelog.

## Authentication

```bash
export SCRAPFLY_API_KEY=scp-live-...
# or persist it
scrapfly config set-key scp-live-...
```

Resolution order: `--api-key` flag > `SCRAPFLY_API_KEY` env >
`~/.scrapfly/config.json`.

## Quick start

```bash
# Scrape a JS-heavy page with the Unblocker + markdown output
# (--asp is the previous name for --unblocker; it still works, hidden from --help)
scrapfly scrape https://web-scraping.dev/products --render-js --unblocker --format markdown

# Pipe scrape into extract (two-step: fetch + AI extraction)
scrapfly scrape https://web-scraping.dev/product/1 --render-js --proxified \
  | scrapfly extract --content-type text/html --prompt "name, price, sku"

# Screenshot auto-named into a directory
scrapfly -O ./shots screenshot https://web-scraping.dev --resolution 1920x1080

# Crawl synchronously, then extract the WARC you downloaded
scrapfly crawl run https://web-scraping.dev --max-pages 20 --content-format markdown
scrapfly crawl artifact <uuid> --type warc -o crawl.warc
scrapfly crawl parse warc-list crawl.warc --pretty
```

## Proxy pools

`scrape`, `scrape batch`, `crawl start` and `crawl run` send `--proxy-pool` as
given:

| `--proxy-pool`            | Runs on                                                    |
|---------------------------|------------------------------------------------------------|
| `public_datacenter_pool`  | datacenter proxies, the default                            |
| `public_residential_pool` | residential proxies                                        |
| `public_tor_pool`         | the Tor network, the only pool that reaches .onion sites   |

A pool of your own is named the same way. The Unblocker is a flag of its own,
on any pool: `--unblocker` sends `asp=true` (`--asp`, its previous name, still
works).

```bash
scrapfly scrape https://web-scraping.dev/products --proxy-pool public_residential_pool --unblocker
```

In a `scrape batch` JSONL line, the `proxy_pool`, `asp` and `unblocker` keys
mean what they mean to the API (`asp` wins over `unblocker` on the same line),
and the flags fill in only what a line leaves out.

## Browser & Agent

`scrapfly browser` gives you a CDP WebSocket URL you can hand to
Playwright, Puppeteer, [browser-use](https://github.com/browser-use/browser-use),
or drive directly through the built-in session daemon:

```bash
scrapfly browser --session demo start &
scrapfly browser navigate https://web-scraping.dev/login
scrapfly browser fill 'input[name=username]' user123
scrapfly browser fill 'input[name=password]' password
scrapfly browser click 'button[type=submit]'
scrapfly browser close
```

On Scrapfly's custom browser, `fill` / `click` / `wait` / `slide`
automatically go through the **Antibot** CDP domain (human-like timing,
slider-captcha support) and `content` uses **Page.getRenderedContent**
(HTML with iframes inlined).

For fully autonomous runs:

```bash
ANTHROPIC_API_KEY=... scrapfly agent "Find the first product name and price" \
  --url https://web-scraping.dev/products \
  --schema '{"type":"object","properties":{"name":{"type":"string"},"price":{"type":"string"}},"required":["name","price"]}'
```

Providers: Anthropic (default), OpenAI, Google Gemini, and any
OpenAI-compatible endpoint (Ollama, vLLM, …).

## Credential vault

`scrapfly vault` manages the credentials the Cloud Browser injects over CDP on
a session's first page. Items are encrypted under a key generated on create and
returned once: Scrapfly keeps no copy, and no response ever carries a plaintext
item secret.

```bash
# Create a vault; the key is printed once, store it now
scrapfly vault create checkout --pretty

export SCRAPFLY_VAULT_KEY=<the key>

# Seal a password. Read it from a file or stdin to keep it out of shell history
echo '{"password":"hunter2"}' | scrapfly vault item create <vault-id> \
  --type password --label storefront \
  --origin https://web-scraping.dev/login --username user123 --secret -

scrapfly vault item list <vault-id> --pretty

# Attach it to a session
scrapfly browser --vault checkout --vault-key "$SCRAPFLY_VAULT_KEY"
```

Mirroring a 1Password vault instead of entering items by hand:

```bash
export SCRAPFLY_VAULT_SERVICE_TOKEN=<1Password service-account token>

# Which upstream vaults can the token see?
scrapfly vault service test <vault-id> --pretty

scrapfly vault service link <vault-id> --upstream-vault-id <id> --sync-mode on_session
scrapfly vault service sync <vault-id> --pretty

# --keep-items or --drop-items is required; dropping cannot be undone
scrapfly vault service unlink <vault-id> --keep-items
```

`SCRAPFLY_VAULT_KEY` and `SCRAPFLY_VAULT_SERVICE_TOKEN` are read for every
subcommand that needs them, so neither secret has to appear in argv. `vault
rotate` returns a new key and invalidates the old one for the whole vault.

## Product coverage

| Scrapfly API            | REST                                             | CLI                                    |
|-------------------------|--------------------------------------------------|----------------------------------------|
| Web Scraping            | `GET/POST /scrape`                               | `scrapfly scrape <url>`                |
| Scrape Batch            | `POST /scrape/batch`                             | `scrapfly scrape batch --url <url>…`   |
| Screenshot              | `POST /screenshot`                               | `scrapfly screenshot <url>`            |
| Extraction              | `POST /extraction`                               | `scrapfly extract`                     |
| Classify                | `POST /classify`                                 | `scrapfly scrape classify`             |
| Crawler                 | `POST /crawl` + `/crawl/{uuid}/...`              | `scrapfly crawl {start,run,status,...}`|
| Browser (CDP)           | `wss://browser.scrapfly.io`                      | `scrapfly browser [start/...]`         |
| Browser Unblock         | `POST /unblock`                                  | `scrapfly browser <url> --unblock`     |
| Browser Vault           | `/vault`, `/vault/{id}/item`, `/vault/{id}/service` | `scrapfly vault {list,create,item,service}` |
| Account                 | `GET /account`                                   | `scrapfly account` / `scrapfly status` |
| Feedback                | `POST /dashboard/api/feedback` (on `scrapfly.io`) | `scrapfly feedback`                    |

Every documented SDK field is exposed as a flag. See the
[`scrapfly-cli` agent skill](https://github.com/scrapfly/skills/blob/main/skills/scrapfly-cli/SKILL.md)
for the complete map.

## For LLM agents

The agent skill that teaches Claude Code, Cursor, Codex, Copilot, Cline,
Windsurf, and [50+ other agents](https://skills.sh) how to drive this CLI
lives in the central [`scrapfly/skills`](https://github.com/scrapfly/skills)
repo. Install it via the open [`skills`](https://www.npmjs.com/package/skills)
CLI:

```bash
# Just the CLI skill
npx skills add scrapfly/skills --skill scrapfly-cli

# Or every Scrapfly skill (SDK + CLI + MCP agent rules)
npx skills add scrapfly/skills --all
```

That ships a compact guide covering the six usage paths, auth, the JSON
envelope contract, and every CLI verb grouped by intent. Or point your own
tool-use loop at the raw markdown:
[`scrapfly/skills/skills/scrapfly-cli/SKILL.md`](https://github.com/scrapfly/skills/blob/main/skills/scrapfly-cli/SKILL.md).

## GitHub Actions

Use this repo as a reusable action to install the CLI on a runner:

```yaml
- uses: scrapfly/scrapfly-cli@v0.2.0
  with:
    api-key: ${{ secrets.SCRAPFLY_API_KEY }}
- run: scrapfly scrape https://web-scraping.dev --format markdown
```

Inputs: `version` (default `latest`), `api-key` (exported to `SCRAPFLY_API_KEY`
with log masking), `prefix` (default `$HOME/.local/bin`).

## MCP server

For Cursor, Claude Desktop, Claude Code, and any other MCP-compatible
client:

```jsonc
{
  "mcpServers": {
    "scrapfly": {
      "command": "scrapfly",
      "args": ["mcp", "serve"],
      "env": { "SCRAPFLY_API_KEY": "scp-live-..." }
    }
  }
}
```

Exposes `scrape`, `screenshot`, `extract`, `crawl_run`, and `selector` as
tools. Their inputs mirror the CLI flags: `proxy_pool` is sent as given, and
`unblocker` (or its previous name `asp`) sends `asp=true`.

## Shell completion

```bash
scrapfly completion zsh  > "${fpath[1]}/_scrapfly"       # zsh
scrapfly completion bash > ~/.bash_completion.d/scrapfly # bash
scrapfly completion fish > ~/.config/fish/completions/scrapfly.fish
```

## Development

```bash
make install   # go mod download
make dev       # build dist/scrapfly
make test      # go test ./...
make lint      # go vet ./...
make fmt       # gofmt -w .

# cut a release (tags + pushes; CI publishes binaries)
make release VERSION=0.2.0 NEXT_VERSION=0.2.1
```

## Links

- Scrapfly docs: https://scrapfly.io/docs
- Official SDKs (Python, TypeScript, Go, Rust): https://scrapfly.io/docs/sdk
- Issues: https://github.com/scrapfly/scrapfly-cli/issues

## License

MIT. See [LICENSE](LICENSE).
