# Changelog

## 0.5.0

### Behavior change

- macOS 13 Ventura or later: the binaries are built with Go 1.27 and do not
  start on macOS 12 Monterey, which 0.4.1 supported. On macOS 12, stay on
  0.4.1 and do not run `scrapfly update` from it, since 0.4.1 installs any
  newer release. From this release on, the installer refuses the latest
  release on macOS 12 (pass `--version v0.4.1` to it), the npm package refuses to
  install 0.5.0 or later there (`npm install scrapfly-cli@0.4.1`), and
  `scrapfly update` refuses a release whose binary needs a newer macOS than
  the Mac runs, keeping the binary it has. Building from source needs Go 1.27
  or later.
- `scrape batch` reads a JSONL line's `proxy_pool` key, which 0.4.1 silently
  dropped (only `ProxyPool` bound): a line carrying it now runs on that pool,
  ahead of `--proxy-pool`.
- `scrape batch` reads a JSONL line's `correlation_id` key, which 0.4.1
  silently dropped (only `CorrelationID` bound): the line's result carries its
  own ID rather than an `item-N`. Two lines naming the same `correlation_id`
  are refused before anything is sent, with the ID and its stdin line numbers,
  where 0.4.1 ignored the key and ran the batch. Give each line its own ID, or
  drop the key.
- `item-N` numbers a config by its position in the batch, one numbering across
  `--url` and `--url-file`: a blank `--url` no longer uses up a number, which
  let a `--url-file` line take the ID of a `--url` entry and got the batch
  refused. A JSONL line without a `correlation_id` never takes an ID another
  line names; it gets the first `item-N` past the end of the batch instead.
- `scrape batch` sends a JSONL line's `Method` and `Body`; 0.4.1 sent every
  line as a GET without its body.
- `browser execute --url`, `agent --url` and `browser session start --url
  --unblock` open the URL in the tab they attach to, waiting up to 15 seconds
  for it to load; 0.4.1 left that tab blank. A URL the browser cannot open now
  fails the command.
- `browser status` reports `alive: false`, with `error`, once the session lost
  its browser; 0.4.1 said `true` while the session process ran.
- A request that timed out is no longer sent again: the retry started the
  request over and waited out the deadline a second time.
- A large scrape result (`clob` or `blob`) the API sends compressed is
  decoded; 0.4.1 printed the compressed bytes. A failed fetch of it reports the
  API's error.
- A Screenshot API answer in JSON is reported as an error; 0.4.1 saved it as
  the image.
- `crawl contents --url` without `--plain` prints the page's content under
  `contents.<url>`, one key per format; 0.4.1 failed with `crawler contents
  response missing required field "contents"`.

### Proxy pools

- `--unblocker` and `--asp` still send `asp=true`, as in 0.4.1, on `scrape`,
  `scrape batch`, `crawl start` and `crawl run`; `--asp` stays hidden from
  `--help`.
- `--proxy-pool` is sent as given, and its help names the pools:
  `public_datacenter_pool` (the default), `public_residential_pool`,
  `public_tor_pool` or a pool of your own.
- The MCP `scrape` and `crawl_run` tools take `proxy_pool`, sent as given.
- `browser --proxy-pool` names the Cloud Browser pools: `datacenter`,
  `residential`, `tor` or `byop`.

### API key kept out of errors

- A network error or a timeout no longer prints the API key: the request URL it
  shows reads `key=REDACTED` (and `api_key=REDACTED` for the exit peer bundle
  download). A Cloud Browser connection that fails (`browser`, `agent`, a
  browser session) shows its URL with the API key, vault key and VNC or WebRTC
  password as `REDACTED`.

### Crawl prompt and extract

- `crawl prompt --no-stream` asks for the whole answer in one `crawl.prompt`
  envelope (`generation.stream: false`) instead of a token stream.
- `crawl extract` returns an object shaped by a JSON Schema (`--schema`, or
  `--schema-file`, `-` for stdin), built from one or more crawls started with
  `--search`. The schema constrains the model as it generates and is not
  validated again: validate anything you act on.

### Self-update

- `scrapfly update` on Windows unzips the release archive; it read the `.zip`
  as a tarball and failed. From 0.4.1 or older, install 0.5.0 once from the
  release's `scrapfly-windows-amd64.zip`; `scrapfly update` works from there.

### Webhooks

- `screenshot --webhook` prints `{"webhook_enqueued": true, "job_uuid": ...}`
  instead of failing with `API returned status 201`.
- `scrape --webhook` prints the queued result instead of failing with
  `scrape failed with status: QUEUED`.
- A `scrape batch` JSONL line with a webhook prints its queued result; 0.4.1
  printed an empty one.

### Browser

- `browser execute` takes two more actions: `cdp`, one raw protocol call on the
  page session, and `events`, the events that session buffered.

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
