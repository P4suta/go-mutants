// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"slices"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/config"
)

// A Decision is what a configured cache mode resolves to for one run.
//
// Read and Write move together in every case this package produces, and the
// two fields exist anyway because they are two different promises: a run that
// reads is one whose verdicts may come from somewhere else, and a run that
// writes is one that leaves something behind. Anything that reads without
// writing would keep answering from a cache it never refreshes, and anything
// that writes without reading would pay the whole cost of the cache for none of
// its benefit.
type Decision struct {
	// Read says cached outcomes may be adopted.
	Read bool
	// Write says outcomes measured by this run may be stored.
	Write bool
	// Reason is why `auto` turned itself off, in one line, or empty. It is a
	// warning for the caller to publish rather than an error: the run works, it
	// is simply doing all of the work.
	Reason string
}

// Enabled reports whether the cache does anything at all this run.
func (d Decision) Enabled() bool { return d.Read || d.Write }

// Resolve decides what a cache mode means for one run.
//
//   - off does nothing, in both directions.
//   - on reads and writes, whatever the run is doing. It is the mode for a
//     project whose `test.command` is its own and which has satisfied itself
//     that the command is deterministic — that promise is the user's to make,
//     and `on` is how they make it.
//   - auto reads and writes only when the effective test command is the
//     built-in default, and does nothing otherwise.
//
// The auto rule is the coverage rule, for the same reason and with the same
// wording. go-mutants knows exactly what `go test ./...` does; it knows nothing
// about a command somebody wrote themselves, which may consult a clock, a
// database, a network, or a file the snapshot does not cover — and none of
// those is in the key, because none of them can be. The two candidate
// behaviours for that case are to cache anyway, which risks reporting a
// detection that never happened, and to do nothing, which costs time. Only one
// of them can produce a wrong answer, so `auto` picks the other and says so.
//
// Turning off rather than degrading to read-only is the same argument once
// more. A read-only cache over a command go-mutants cannot reason about would
// still be adopting outcomes it cannot justify; it would merely stop
// accumulating new ones.
func Resolve(mode config.CacheMode, command []string) Decision {
	switch mode {
	case config.CacheOff:
		return Decision{}
	case config.CacheOn:
		return Decision{Read: true, Write: true}
	case config.CacheAuto:
		if slices.Equal(command, config.DefaultTestCommand()) {
			return Decision{Read: true, Write: true}
		}
		return Decision{Reason: customCommand(command)}
	default:
		// An unknown mode never reaches here — internal/config refuses one — and
		// the safe reading of a value this build does not understand is the one
		// that changes nothing about the run.
		return Decision{Reason: "the cache mode " + strconv.Quote(mode.String()) +
			" is not one this build knows, so no outcome was reused or stored"}
	}
}

// customCommand is what [CodeUnavailable] says when `auto` steps aside: which
// command was configured, why it cannot be reasoned about, and how to ask for
// the cache anyway.
func customCommand(command []string) string {
	return "the outcome cache is off because test.command is " +
		strconv.Quote(strings.Join(command, " ")) + " rather than the built-in " +
		strconv.Quote(strings.Join(config.DefaultTestCommand(), " ")) +
		"; go-mutants cannot tell whether a command of its own would give the same answer twice, " +
		"so cache.mode auto reuses nothing rather than risk a detection that never happened — " +
		"set cache.mode to \"on\" if the command is reproducible"
}
