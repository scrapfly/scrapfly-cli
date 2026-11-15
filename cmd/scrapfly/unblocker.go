package main

import (
	"strconv"

	"github.com/spf13/cobra"
)

// aspDeprecationNotice follows pflag's "Flag --asp has been deprecated, " on one line.
const aspDeprecationNotice = "use --unblocker instead; --asp keeps working"

// proxyPoolHelp closes the --help of every command that takes the pool flags.
const proxyPoolHelp = `Proxy pools: --proxy-pool is sent as given. public_datacenter_pool is the
default, public_residential_pool runs on residential proxies, public_tor_pool
on the Tor network (the only pool that reaches .onion addresses), and a pool
of your own is named the same way. --unblocker turns the Unblocker on, sent as
asp=true, whatever the pool.`

// unblockerFlagValue is --unblocker or its legacy name --asp, both writing dst;
// a supplied --asp wins over --unblocker whatever the order.
type unblockerFlagValue struct {
	dst *bool
	// aspSet records that --asp was supplied.
	aspSet *bool
	legacy bool
}

func (v unblockerFlagValue) String() string { return strconv.FormatBool(*v.dst) }

// Type reports "bool" so --help and shell completions treat the flag as a BoolVar.
func (v unblockerFlagValue) Type() string { return "bool" }

func (v unblockerFlagValue) Set(raw string) error {
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return err
	}
	if v.legacy {
		*v.aspSet = true
	} else if *v.aspSet {
		return nil
	}
	*v.dst = parsed
	return nil
}

// proxyPoolFlags holds --proxy-pool, sent as proxy_pool, and --unblocker (or
// --asp), sent as asp.
type proxyPoolFlags struct {
	pool      string
	unblocker bool
}

// bindProxyPoolFlags registers --proxy-pool, --unblocker and --asp on cmd;
// scope says what they apply to (" for child scrapes"), "" on scrape.
func bindProxyPoolFlags(cmd *cobra.Command, f *proxyPoolFlags, scope string) {
	cmd.Flags().StringVar(&f.pool, "proxy-pool", "", "proxy pool"+scope+", sent as given: public_datacenter_pool (the default), public_residential_pool, public_tor_pool or a pool of your own")
	bindUnblockerFlag(cmd, &f.unblocker, "enable the Unblocker (anti-bot bypass)"+scope+", sent as asp=true")
}

// bindUnblockerFlag registers --unblocker and its legacy name --asp, both
// writing dst; --asp is hidden from --help and completions and prints a
// one-line notice on stderr when used.
func bindUnblockerFlag(cmd *cobra.Command, dst *bool, usage string) {
	aspSet := new(bool)
	for _, name := range []string{"unblocker", "asp"} {
		cmd.Flags().Var(unblockerFlagValue{dst: dst, aspSet: aspSet, legacy: name == "asp"}, name, usage)
		// Keeps the flag usable bare, as `--unblocker`.
		cmd.Flags().Lookup(name).NoOptDefVal = "true"
	}
	if err := cmd.Flags().MarkDeprecated("asp", aspDeprecationNotice); err != nil {
		panic(err)
	}
}

// resolveLine returns the proxy_pool and asp of a scrape batch JSONL line:
// the line's own value of each (lineASP nil when it names neither asp nor
// unblocker), else the flags'.
func (f proxyPoolFlags) resolveLine(linePool string, lineASP *bool) (pool string, asp bool) {
	pool = linePool
	if pool == "" {
		pool = f.pool
	}
	asp = f.unblocker
	if lineASP != nil {
		asp = *lineASP
	}
	return pool, asp
}

// resolveUnblocker returns the asp value of an input carrying asp and
// unblocker as separate tri-states: asp when given, else unblocker, else
// false. An explicit false on either turns the Unblocker off.
func resolveUnblocker(asp, unblocker *bool) bool {
	if asp != nil {
		return *asp
	}
	if unblocker != nil {
		return *unblocker
	}
	return false
}
