package main

import (
	"strconv"

	"github.com/spf13/cobra"
)

// aspDeprecationNotice is appended by pflag to "Flag --asp has been
// deprecated, ". Keep it one line: it is printed on every invocation that
// still uses the old name.
const aspDeprecationNotice = "use --unblocker instead; --asp keeps working"

// unblockerFlagValue is one of the two names the anti-bot toggle answers to.
// Both names write the same destination, so "off" is always expressible; what
// pflag cannot express on its own is the precedence, which therefore lives in
// Set: an explicitly supplied --asp wins over --unblocker whatever the order
// on the command line.
//
// A single shared BoolVar destination would be plain last-flag-wins, so
// `--asp=false --unblocker` would switch a paid feature back on for someone
// who had pinned it off. Every other surface in this binary settles the two
// names the same way — see resolveUnblocker.
type unblockerFlagValue struct {
	dst *bool
	// aspSet is shared by both registrations: it records that the legacy name
	// was supplied, which is what gives it precedence.
	aspSet *bool
	legacy bool
}

func (v unblockerFlagValue) String() string { return strconv.FormatBool(*v.dst) }

// Reported as "bool" so --help and shell completions treat the flag exactly as
// the BoolVar registration they replaced.
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

// bindUnblockerFlag registers the anti-bot bypass toggle under its current
// name and its legacy --asp alias, both writing dst.
//
// MarkDeprecated hides --asp from --help and from generated/dynamic shell
// completions while leaving it fully functional, and makes pflag emit a notice
// whenever it is used. That notice reaches the user through cobra's flag
// buffer, which ParseFlags flushes to Command.Print -> OutOrStderr; this
// binary never calls SetOut, so it lands on stderr and leaves the strict JSON
// envelope on stdout intact.
//
// The value still reaches the API under the "asp" wire key — only the
// customer-facing name changed. See go-scrapfly's ScrapeConfig.Unblocker.
func bindUnblockerFlag(cmd *cobra.Command, dst *bool, usage string) {
	aspSet := new(bool)
	for _, name := range []string{"unblocker", "asp"} {
		cmd.Flags().Var(unblockerFlagValue{dst: dst, aspSet: aspSet, legacy: name == "asp"}, name, usage)
		// Var registers a flag that demands a value; a boolean has to stay
		// usable bare, as `--unblocker`.
		cmd.Flags().Lookup(name).NoOptDefVal = "true"
	}
	if err := cmd.Flags().MarkDeprecated("asp", aspDeprecationNotice); err != nil {
		// Only reachable if the registration above changed name; a build-time
		// mistake, not a runtime condition.
		panic(err)
	}
}

// resolveUnblocker settles the two names for inputs that carry them as
// separate tri-state values (JSON tool arguments, JSONL batch lines) rather
// than as one shared flag destination.
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
