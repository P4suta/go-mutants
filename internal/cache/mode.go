// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"slices"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/config"
)

type Decision struct {
	Read   bool
	Write  bool
	Reason string
}

func (d Decision) Enabled() bool { return d.Read || d.Write }

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
		return Decision{Reason: "the cache mode " + strconv.Quote(mode.String()) +
			" is not one this build knows, so no outcome was reused or stored"}
	}
}

func customCommand(command []string) string {
	return "the outcome cache is off because test.command is " +
		strconv.Quote(strings.Join(command, " ")) + " rather than the built-in " +
		strconv.Quote(strings.Join(config.DefaultTestCommand(), " ")) +
		"; go-mutants cannot tell whether a command of its own would give the same answer twice, " +
		"so cache.mode auto reuses nothing rather than risk a detection that never happened — " +
		"set cache.mode to \"on\" if the command is reproducible"
}
