# Changelog

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
