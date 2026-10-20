# Changelog

## Unreleased

### Behavior change

- `--unblocker` now selects the Unblocker pool (`proxy_pool=unblocker`, residential proxies with the Unblocker), which a target or anti-bot protection with a price of its own refuses with `ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED` (HTTP 422, not billed): use `--unblocker-ultra` there. 0.4.1 and earlier sent `asp=true`, which is deprecated; the [migration guide](https://scrapfly.io/new-pricing#legacy-spellings) covers requests written before the proxy pools.

### Proxy pools

- `--unblocker` selects the Unblocker pool (`proxy_pool=unblocker`,
  residential proxies with the Unblocker) on `scrape`, `scrape batch`,
  `crawl start` and `crawl run`, and the new `--unblocker-ultra` selects the
  Unblocker Ultra pool (`proxy_pool=unblocker_ultra`). When a site blocks,
  `--unblocker-ultra` is the flag to use: it is never refused, and costs the
  same as `--unblocker` except on a target or anti-bot protection with a price
  of its own, which it adds.
- `--asp` is now an alias of `--unblocker-ultra`: it sends
  `proxy_pool=unblocker_ultra` where it sent `asp=true`. It is listed in
  `--help`, prints no notice, and an explicit `--asp` wins over
  `--unblocker-ultra` whatever the order; `--asp=false` no longer turns
  `--unblocker` off.
- `--proxy-pool` takes `basic` (datacenter proxies, Unblocker off),
  `unblocker`, `unblocker_ultra` and `auto`, and wins over all three flags;
  `--unblocker-ultra` wins over `--unblocker`. With `auto` the API picks the
  pool before the request, from the target's profile: the Unblocker on
  datacenter proxies, or the pool the target needs. `auto` is accepted on every
  target, the ones that require `unblocker_ultra` included, where their price
  is added, and is priced as the pool it runs on. Any other `--proxy-pool`
  value, such as `tor` or a pool of your own, is sent as given, with
  `asp=true` when one of the flags is set, as before.
- On `ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED` the error envelope keeps the API's
  code and message and adds the hint `retry with --unblocker-ultra`, which
  also names the `--proxy-pool` value to drop when one picked the refused pool.
  A `scrape batch` item gets it only when its pool came from the flags, in
  `data.hint` when refused as a scrape result. An item whose JSONL line named
  its own `proxy_pool` gets none, even when the line names the pool the flags
  send, as no flag changes that line.
- The MCP `scrape` and `crawl_run` tools take `proxy_pool`: `basic`,
  `unblocker`, `unblocker_ultra`, `auto`, `tor` or `public_tor_pool` (the Tor
  pool's name in results), the only values their schemas list; a pool name
  written before the proxy pools is accepted too.
  Their `unblocker` and `asp` arguments, unlike the CLI flags of the same
  names, are deprecated: use `proxy_pool` (`unblocker_ultra` for the
  Unblocker). The [migration guide](https://scrapfly.io/new-pricing#legacy-spellings)
  covers requests written before the proxy pools. The tools add no
  `proxy_pool` to a call that names none.
- `scrape batch` reads a JSONL line's `proxy_pool` key, which was silently
  dropped (only `ProxyPool` bound), so a line's own pool now applies. A line
  naming `asp` or `unblocker` takes a pool word from the flags only when the
  word matches its value (`basic` with `false`; `unblocker`, `unblocker_ultra`
  or `auto` with `true`), and the word then goes out in place of that key.

These flags behave this way from the CLI release after 0.4.1. On 0.4.1 and
earlier, `--unblocker` and `--asp` send `asp=true`, and
`--proxy-pool unblocker_ultra` or `--proxy-pool auto` selects that pool.

### `scrape batch` correlation IDs

- A JSONL line's `correlation_id` key is read, which was silently dropped
  (only `CorrelationID` bound), so the line's result carries its own ID rather
  than an `item-N`.
- `item-N` numbers a config by its position in the batch, one numbering across
  `--url` and `--url-file`: a blank `--url` still used up its number, so a
  `--url-file` line could take the ID of a `--url` entry and the batch was
  refused. A JSONL line without a `correlation_id` never takes an ID another
  line names; it gets the first `item-N` past the end of the batch instead.
- Two JSONL lines naming the same `correlation_id` are refused before anything
  is sent, with the ID and its stdin line numbers, as the API refuses them.

## 0.4.1

### Signed macOS binaries

The macOS binaries (`darwin-universal`, `darwin-amd64`, `darwin-arm64`) are
signed with Scrapfly's Apple Developer ID and notarized by Apple. A manually
downloaded binary runs without the Gatekeeper prompt and without
`xattr -d com.apple.quarantine`.

### `scrapfly feedback`

New command: file product feedback on a Scrapfly surface (an API, the docs, the
dashboard, the MCP server, a skill, or the CLI itself) without leaving the
terminal.

```bash
scrapfly feedback --sentiment negative --feature crawler_api \
  --message "crawl status returned 200 with an empty urls array after the crawl finished" \
  --context "Mirroring a 300-page docs site, polling status every 15s, CLI 0.4.1"
```

`--sentiment`, `--message` and `--context` are required; `--feature` scopes the
report and an unknown value is rejected with the full list. `--actor` defaults
to `agent` when a coding-agent environment variable is present, so a report
written by an agent driving the CLI is stored as its observation rather than the
developer's own words. Reports are throttled per account per hour, and a
throttled report is to be dropped, not retried.

This is not support: it opens no ticket and no reply comes back.

## 0.4.0

### Behavior change

`scrape batch` no longer OR-s the `--asp` / `--unblocker` flag with a JSONL
line. A line that names either key now decides for itself, `false` included,
and the flag is only the default for lines that name neither. Before, a line
carrying `"asp": false` was silently switched back on by the flag.

If a generator emits `"asp": false` on every line while relying on
`--unblocker` (or the old `--asp`) to turn the bypass on, the bypass is now
off for every one of those URLs and blocked pages come back as blocked pages.
Drop the key from the lines that should follow the flag, or set it to `true`
on the lines that need the bypass.

This is deliberate: an explicit value in the payload outranks a flag-level
default on every Scrapfly surface, and it is the only rule under which a line
can turn the bypass off at all.

### Renamed

- `--asp` is now `--unblocker`, on `scrape`, `scrape batch`, `crawl start`
  and `crawl run`. `--asp` keeps working, hidden from `--help` and from shell
  completion, and prints a deprecation notice on stderr. When both names are
  given the explicit `--asp` wins, so a pinned `--asp=false` is never
  overridden.
- The MCP tools take `unblocker` and keep accepting `asp` with the same
  precedence. Both stay declared in the tool schemas as plain booleans, so a
  client pinned to the old key is not rejected.
- The wire key sent to the API is unchanged, and so is the account feature it
  bills.

### Added

- `crawl start --search` builds a semantic search index while the crawl runs,
  and `crawl start --refresh [--refresh-interval <seconds>]` keeps a finished
  crawl fresh under the same UUID.
- `crawl search <uuid...> --query` searches the content of one or more
  finished crawls, with `--mode vector|fts|hybrid`, repeatable `--filter
  key=value` (url_prefix, host, source_format, content_type, http_status,
  crawler_uuid), `--limit` and `--cursor` paging.
- `crawl prompt <uuid...> --prompt` answers a question from crawled content,
  with `--model` and the same retrieval flags as `crawl search`.
- `crawl refresh <uuid>` re-scrapes a crawl in place, or edits its schedule
  with `--enable`, `--disable` and `--interval` seconds.
- `crawl refresh-history <uuid>` shows the refresh timeline, `--limit` keeps
  only the last N rows.

### Fixed

- Installing from source works again: go.mod requires the published
  go-scrapfly module instead of a local replace directive, which
  `go install ...@version` refuses. `make check-no-replace` now blocks a
  release that would reintroduce one.
- `make test` runs `go test ./...` against that pinned module. It used to
  rewrite go.mod to point at a sibling SDK checkout and, on any failure, left
  the replace directive behind and broke the next release.
- The npm wrapper's in-tree version tracks the CLI version, so a wrapper run
  from a git checkout downloads the matching release assets instead of v0.2.0.

## Earlier releases

Not tracked in this file. See the release notes at
https://github.com/scrapfly/scrapfly-cli/releases
