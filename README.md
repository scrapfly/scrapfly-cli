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
  vLLM). Point any other tool-use loop at the full CLI instead via
  [`agent-onboarding/SKILL.md`](agent-onboarding/SKILL.md).
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

Or grab a binary directly from
[Releases](https://github.com/scrapfly/scrapfly-cli/releases). Artifacts
are the raw executable named `scrapfly-{os}-{arch}` (`.exe` on Windows).
Supported: `macos-amd64`, `macos-arm64`, `linux-amd64`, `linux-arm64`,
`windows-amd64`, `windows-arm64`.

From source (Go 1.24+):

```bash
go install github.com/scrapfly/scrapfly-cli/cmd/scrapfly@latest
```

Or from a package manager:

```bash
# Homebrew (macOS, once the tap is published)
brew install scrapfly/tap/scrapfly

# npm / pnpm / yarn
npm install -D scrapfly-cli
npx scrapfly version
```

### First-run warnings (unsigned binaries)

The released binaries aren't code-signed yet. First run shows a one-time
warning on macOS and Windows; click-through and you won't see it again.

**macOS** - *"Apple could not verify 'scrapfly' is free of malware"*:

- `install.sh` strips the quarantine bit automatically.
- If you downloaded the binary manually, run once:
  ```bash
  xattr -d com.apple.quarantine /path/to/scrapfly
  ```
  (or right-click → Open in Finder the first time).

**Windows** - *"Windows protected your PC"* (SmartScreen):

1. Click **More info** on the blue dialog.
2. Click **Run anyway**.

Windows remembers the decision after the first run.

Proper Apple notarization + Windows Authenticode signing is on the
roadmap.

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
# Scrape a JS-heavy page with anti-bot + markdown output
scrapfly scrape https://web-scraping.dev/products --render-js --asp --format markdown

# Pipe scrape into extract (two-step: fetch + AI extraction)
scrapfly scrape https://web-scraping.dev/product/1 --render-js --proxified \
  | scrapfly extract --content-type text/html --prompt "name, price, sku"

# Screenshot auto-named into a directory
scrapfly -O ./shots screenshot https://example.com --resolution 1920x1080

# Crawl synchronously, then extract the WARC you downloaded
scrapfly crawl run https://example.com --max-pages 20 --content-format markdown
scrapfly crawl artifact <uuid> --type warc -o crawl.warc
scrapfly crawl parse warc-list crawl.warc --pretty
```

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

## Product coverage

| Scrapfly API            | REST                                             | CLI                                    |
|-------------------------|--------------------------------------------------|----------------------------------------|
| Web Scraping            | `GET/POST /scrape`                               | `scrapfly scrape <url>`                |
| Screenshot              | `POST /screenshot`                               | `scrapfly screenshot <url>`            |
| Extraction              | `POST /extraction`                               | `scrapfly extract`                     |
| Crawler                 | `POST /crawl` + `/crawl/{uuid}/...`              | `scrapfly crawl {start,run,status,...}`|
| Browser (CDP)           | `wss://browser.scrapfly.io`                      | `scrapfly browser [start/...]`         |
| Browser Unblock         | `POST /unblock`                                  | `scrapfly browser <url> --unblock`     |
| Account                 | `GET /account`                                   | `scrapfly account` / `scrapfly status` |

Every documented SDK field is exposed as a flag. See
[`agent-onboarding/SKILL.md`](agent-onboarding/SKILL.md) for the complete
map.

## For LLM agents

Point your tool-use loop at [`agent-onboarding/SKILL.md`](agent-onboarding/SKILL.md):
a compact guide covering the six usage paths, auth, envelope contract,
and every CLI verb grouped by intent.

## GitHub Actions

Use this repo as a reusable action to install the CLI on a runner:

```yaml
- uses: scrapfly/scrapfly-cli@v0.2.0
  with:
    api-key: ${{ secrets.SCRAPFLY_API_KEY }}
- run: scrapfly scrape https://example.com --format markdown
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

Exposes `scrape`, `screenshot`, `extract`, `crawl_run`, `selector`, and
8 browser tools (`browser_navigate`, `browser_snapshot`, `browser_click`,
`browser_fill`, `browser_content`, `browser_eval`, `browser_screenshot`,
`browser_close`) as MCP tools. Browser session is auto-started on first
browser tool call.

For the full Playwright tool surface (navigate, click, fill, screenshot,
pdf, ...) backed by Scrapfly:

```jsonc
{
  "mcpServers": {
    "scrapfly-playwright": {
      "command": "scrapfly",
      "args": ["mcp", "playwright", "--country", "us"],
      "env": { "SCRAPFLY_API_KEY": "scp-live-..." }
    }
  }
}
```

Requires Node.js (`npx @playwright/mcp` runs under the hood).

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
