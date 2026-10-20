package main

import (
	"errors"
	"strconv"
	"strings"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

// Proxy pools of the Web Scraping API and the Crawler, sent as proxy_pool.
// Spelled here rather than taken from go-scrapfly so the CLI builds against
// SDK releases that predate them.
const (
	proxyPoolBasic          = "basic"
	proxyPoolUnblocker      = "unblocker"
	proxyPoolUnblockerUltra = "unblocker_ultra"
	// The API picks the pool from the target's profile, the Unblocker on in
	// every pool it picks.
	proxyPoolAuto = "auto"
	// The API's alias of the Tor pool, which customer outputs name
	// public_tor_pool. Not a pool word: asp still decides the Unblocker on
	// it.
	proxyPoolTor = "tor"
	// proxyPoolPublicTor is the Tor pool as results name it, a current name, not one written before the pools.
	proxyPoolPublicTor = "public_tor_pool"
)

// errUnblockerUltraRequired is the API's refusal of basic and unblocker on a
// target or anti-bot protection with a price of its own (HTTP 422, not
// billed).
const errUnblockerUltraRequired = "ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED"

// proxyPoolHelp closes the --help of every command that takes the pool flags.
const proxyPoolHelp = `Proxy pools: when a site blocks, --unblocker-ultra is the flag to use. It sends
proxy_pool=unblocker_ultra, is never refused, and costs the same as --unblocker
except on a target or anti-bot protection with a price of its own, which it
adds. --unblocker (proxy_pool=unblocker, residential proxies with the
Unblocker) is refused there with ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED (HTTP
422, not billed). --asp is an alias of --unblocker-ultra, and --proxy-pool
basic|unblocker|unblocker_ultra|auto wins over all three. --proxy-pool auto
lets the API pick the pool before the request, from the target's profile: the
Unblocker on datacenter proxies, or the pool the target needs. It is accepted
on every target, the ones that require unblocker_ultra included, where their
price is added, and is priced as the pool it runs on.`

// poolWordUnblocker reports whether pool is one of the four pool words,
// matched the way the API matches it (case-insensitive, surrounding spaces
// ignored), and whether that pool runs the Unblocker.
func poolWordUnblocker(pool string) (unblocker, isWord bool) {
	switch strings.ToLower(strings.TrimSpace(pool)) {
	case proxyPoolBasic:
		return false, true
	case proxyPoolUnblocker, proxyPoolUnblockerUltra, proxyPoolAuto:
		return true, true
	}
	return false, false
}

// poolDecidesUnblocker reports whether the API derives the Unblocker from the
// pool alone, ignoring an asp sent beside it. Any other pool name does not:
// asp still decides the Unblocker on it.
func poolDecidesUnblocker(pool string) bool {
	_, isWord := poolWordUnblocker(pool)
	return isWord
}

// ultraFlagValue is one of the two names of the Unblocker Ultra toggle,
// --unblocker-ultra and its alias --asp. Both write the same destination, so
// "off" is always expressible; what pflag cannot express on its own is the
// precedence, which therefore lives in Set: an explicitly supplied --asp wins
// over --unblocker-ultra whatever the order on the command line.
//
// A single shared BoolVar destination would be plain last-flag-wins, so
// `--asp=false --unblocker-ultra` would switch a paid pool back on for someone
// who had pinned it off. The JSON surfaces settle their two names the same
// way, see resolveUnblocker.
type ultraFlagValue struct {
	dst *bool
	// aspSet is shared by both registrations: it records that --asp was
	// supplied, which is what gives it precedence.
	aspSet *bool
	asp    bool
}

func (v ultraFlagValue) String() string { return strconv.FormatBool(*v.dst) }

// Reported as "bool" so --help and shell completions treat the flag exactly as
// a BoolVar registration.
func (v ultraFlagValue) Type() string { return "bool" }

func (v ultraFlagValue) Set(raw string) error {
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return err
	}
	if v.asp {
		*v.aspSet = true
	} else if *v.aspSet {
		return nil
	}
	*v.dst = parsed
	return nil
}

// proxyPoolFlags is what --proxy-pool, --unblocker and --unblocker-ultra (or
// its alias --asp) set on scrape, scrape batch, crawl start and crawl run.
type proxyPoolFlags struct {
	pool           string
	unblocker      bool
	unblockerUltra bool
}

// bindProxyPoolFlags registers the pool flags on cmd; scope says what they
// apply to (" for child scrapes"), "" on scrape.
//
// Every one of them is documented: none is hidden or marked deprecated, which
// in pflag hides a flag and prints "deprecated" on every use.
func bindProxyPoolFlags(cmd *cobra.Command, f *proxyPoolFlags, scope string) {
	cmd.Flags().StringVar(&f.pool, "proxy-pool", "", "proxy pool"+scope+": basic (datacenter proxies, Unblocker off), unblocker, unblocker_ultra or auto (the API picks the pool from the target's profile), winning over --unblocker, --unblocker-ultra and --asp. Another pool name, such as tor or a pool of your own, is sent as given, with asp=true if one of those flags is set; the migration guide covers asp: https://scrapfly.io/new-pricing#legacy-spellings")
	cmd.Flags().BoolVar(&f.unblocker, "unblocker", false, "Unblocker pool"+scope+": proxy_pool=unblocker, residential proxies with the Unblocker. A target or anti-bot protection with a price of its own refuses it with ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED (HTTP 422, not billed): use --unblocker-ultra there")

	aspSet := new(bool)
	cmd.Flags().Var(ultraFlagValue{dst: &f.unblockerUltra, aspSet: aspSet}, "unblocker-ultra", "Unblocker Ultra pool"+scope+": proxy_pool=unblocker_ultra, the flag to use when a site blocks. Never refused, and costs the same as --unblocker except on a target or anti-bot protection with a price of its own, which it adds. Wins over --unblocker")
	cmd.Flags().Var(ultraFlagValue{dst: &f.unblockerUltra, aspSet: aspSet, asp: true}, "asp", "alias of --unblocker-ultra; wins over it when both are given")
	for _, name := range []string{"unblocker-ultra", "asp"} {
		// Var registers a flag that demands a value; a boolean has to stay
		// usable bare, as `--unblocker-ultra`.
		cmd.Flags().Lookup(name).NoOptDefVal = "true"
	}
}

// resolve returns the proxy_pool and asp to send. A pool word goes out alone:
// it decides the Unblocker, and the API ignores an asp beside it. With no
// pool, --unblocker-ultra sends unblocker_ultra, else --unblocker sends
// unblocker. Any other pool name is sent as given, with asp=true when either
// flag is set, as the CLI sent it before the pool words existed.
func (f proxyPoolFlags) resolve() (pool string, asp bool) {
	switch {
	case poolDecidesUnblocker(f.pool):
		return f.pool, false
	case f.pool != "":
		return f.pool, f.unblocker || f.unblockerUltra
	case f.unblockerUltra:
		return proxyPoolUnblockerUltra, false
	case f.unblocker:
		return proxyPoolUnblocker, false
	}
	return "", false
}

// wantsUnblocker reports whether the flags ask for the Unblocker: a pool
// word's own answer, else either toggle.
func (f proxyPoolFlags) wantsUnblocker() bool {
	if unblocker, isWord := poolWordUnblocker(f.pool); isWord {
		return unblocker
	}
	return f.unblocker || f.unblockerUltra
}

// resolveLine settles a scrape batch JSONL line against the flags. linePool
// and lineASP are the line's own proxy_pool and asp/unblocker (nil when it
// names neither key); they keep the meaning the API gives them, and the flags
// fill in only what the line leaves out. A line's asp decides the Unblocker
// for that line, so a pool word from the flags, which decides the Unblocker
// too, reaches it only when both answer the same.
//
// fromFlags reports whether pool is the flags' pool, which reaches only a
// line naming none: a line naming its own keeps it whatever the flags say,
// even when it names the pool they send.
func (f proxyPoolFlags) resolveLine(linePool string, lineASP *bool) (pool string, asp, fromFlags bool) {
	switch {
	case linePool == "" && lineASP == nil:
		pool, asp = f.resolve()
		return pool, asp, true
	case linePool == "":
		pool, _ = f.resolve()
		if unblocker, isWord := poolWordUnblocker(pool); isWord {
			if unblocker != *lineASP {
				return "", *lineASP, false
			}
			return pool, false, true
		}
		return pool, *lineASP, true
	case poolDecidesUnblocker(linePool):
		return linePool, false, false
	case lineASP != nil:
		return linePool, *lineASP, false
	}
	// The line's pool replaces --proxy-pool; the flags still decide whether
	// the Unblocker runs on it.
	return linePool, f.wantsUnblocker(), false
}

// ultraRefusalHint is the hint an error envelope adds to
// ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED, beside the API's own code and message.
// Only a pool word can be refused, and one named by --proxy-pool wins over
// --unblocker-ultra, so the hint says to drop it.
func (f proxyPoolFlags) ultraRefusalHint() string {
	if poolDecidesUnblocker(f.pool) {
		return "retry with --unblocker-ultra in place of --proxy-pool " + strings.TrimSpace(f.pool)
	}
	return "retry with --unblocker-ultra"
}

// hintUltraRefusal returns err with ultraRefusalHint attached when the API
// answered ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED, else err unchanged.
func (f proxyPoolFlags) hintUltraRefusal(err error) error {
	if isUltraRefusal(err) {
		return &out.HintedError{Err: err, Hint: f.ultraRefusalHint()}
	}
	return err
}

func isUltraRefusal(err error) bool {
	var apiErr *scrapfly.APIError
	return errors.As(err, &apiErr) && apiErr.Code == errUnblockerUltraRequired
}

// resolveUnblocker settles the two names for inputs that carry them as
// separate tri-state values (JSON tool arguments, JSONL batch lines) rather
// than as one shared flag destination. There both are the API's asp
// parameter.
//
// An explicitly supplied legacy value wins; the current name is consulted only
// when the legacy one is absent; they are never OR-ed, so an explicit false on
// either name turns the feature off.
func resolveUnblocker(asp, unblocker *bool) bool {
	if asp != nil {
		return *asp
	}
	if unblocker != nil {
		return *unblocker
	}
	return false
}

// resolveProxyPool settles an MCP tool's proxy_pool against its asp and
// unblocker arguments and returns the pool and the asp value to send. A pool
// that decides the Unblocker goes out alone, since the API ignores an asp
// beside it. Any other call, one naming no pool at all included, is sent
// exactly as the releases before proxy_pool sent it.
func resolveProxyPool(pool string, asp, unblocker *bool) (string, bool) {
	if poolDecidesUnblocker(pool) {
		return pool, false
	}
	return pool, resolveUnblocker(asp, unblocker)
}
