// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"slices"
	"strconv"
	"strings"
)

const traceEnvironmentVariable = "GO_MUTANTS_TRACE"

const (
	keepTempEnvironmentVariable    = "GO_MUTANTS_KEEP_TEMP"
	diagnosticsEnvironmentVariable = "GO_MUTANTS_DIAGNOSTICS"
)

const tracingCommand = "run"

func withEnvironmentFlags(args []string, lookup func(string) (string, bool)) []string {
	requests := []struct {
		variable string
		flag     func(string) (string, bool)
	}{
		{traceEnvironmentVariable, traceFlag},
		{keepTempEnvironmentVariable, keepTempFlag},
		{diagnosticsEnvironmentVariable, diagnosticsFlag},
	}
	for _, request := range requests {
		value, _ := lookup(request.variable)
		flag, requested := request.flag(value)
		args = withEnvironmentFlag(args, flag, requested)
	}
	return args
}

func traceFlag(value string) (string, bool) {
	switch exported(value) {
	case requestUnset, requestOff:
		return "", false
	case requestOn:
		return "--trace", true
	case requestOther:
	}
	return "--trace=" + value, true
}

type request int

const (
	requestUnset request = iota
	requestOn
	requestOff
	requestOther
)

func exported(value string) request {
	if value == "" {
		return requestUnset
	}
	on, err := strconv.ParseBool(value)
	switch {
	case err != nil:
		return requestOther
	case on:
		return requestOn
	default:
		return requestOff
	}
}

func keepTempFlag(value string) (string, bool) {
	switch exported(value) {
	case requestUnset, requestOff:
		return "", false
	case requestOn:
		return "--keep-temp=" + keepTempAlways, true
	case requestOther:
	}
	if strings.TrimSpace(value) == keepTempNever {
		return "", false
	}
	return "--keep-temp=" + value, true
}

func diagnosticsFlag(value string) (string, bool) {
	switch exported(value) {
	case requestUnset, requestOn:
		return "", false
	case requestOff:
		return "--no-diagnostics", true
	case requestOther:
	}
	return "--no-diagnostics=" + value, true
}

func withEnvironmentFlag(args []string, flag string, requested bool) []string {
	if !requested || len(args) == 0 {
		return args
	}
	separator := slices.Index(args, "--")
	head := args
	if separator >= 0 {
		head = args[:separator]
	}
	if !runCommand(head) || hasFlag(head, flag) {
		return args
	}
	if separator >= 0 {
		return slices.Insert(slices.Clone(args), separator, flag)
	}
	return append(slices.Clone(args), flag)
}

func runCommand(head []string) bool {
	command := ""
	for _, argument := range head {
		switch argument {
		case "--help", "-h", "--version":
			return false
		}
		if command == "" && !strings.HasPrefix(argument, "-") {
			command = argument
		}
	}
	return command == tracingCommand
}

func hasFlag(head []string, flag string) bool {
	name, _, _ := strings.Cut(flag, "=")
	for _, argument := range head {
		if argument == name || strings.HasPrefix(argument, name+"=") {
			return true
		}
	}
	return false
}
