// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

const wholeModule = "./..."

const (
	scopeMarker = "go-mutants-package\t"
	scopeFormat = scopeMarker + "{{.Dir}}"
)

func testScope(command []string) (patterns []string, ok bool) {
	if len(command) < 3 || command[0] != "go" || command[1] != "test" {
		return nil, false
	}
	for _, arg := range command[2:] {
		if !isPackagePattern(arg) {
			return nil, false
		}
	}
	return slices.Clone(command[2:]), true
}

func isPackagePattern(arg string) bool {
	if arg != "." && !strings.HasPrefix(arg, "./") {
		return false
	}
	for element := range strings.SplitSeq(strings.ReplaceAll(arg, `\`, "/"), "/") {
		if element == ".." {
			return false
		}
	}
	return true
}

func narrowed(patterns []string) bool {
	for _, pattern := range patterns {
		if pattern == wholeModule {
			return false
		}
	}
	return len(patterns) > 0
}

func (s *session) resolveTestScope(
	ctx context.Context,
	toolchain gocmd.Toolchain,
	root string,
	env []string,
	patterns []string,
) error {
	for _, pattern := range patterns {
		spec := toolchain.Command("list", "-e", "-f", scopeFormat, pattern)
		spec.Dir = root
		spec.Env = env
		spec.Timeout = BaselineCap
		spec.Trace = s.trace
		spec.Kind = trace.ExecKindScopeList
		spec.Subject = pattern

		result := runner.Run(ctx, spec)
		if err := check(ctx, spec, result, CodeTestScope,
			"the package pattern "+strconv.Quote(pattern)+
				" in the test command could not be resolved"); err != nil {
			return err
		}
		if resolvedPackages(result.Output) == 0 {
			return &Error{
				Code: CodeTestScope,
				Message: "the test command names the package pattern " + strconv.Quote(pattern) +
					", which matches no package in the workspace; " +
					"go-mutants builds a test binary for each package the command names, " +
					"and widening the scope back to " + strconv.Quote(wholeModule) +
					" would run the tests the command excludes",
			}
		}
	}
	return nil
}

func resolvedPackages(output []byte) int {
	count := 0
	for line := range strings.SplitSeq(string(output), "\n") {
		dir, marked := strings.CutPrefix(strings.TrimRight(line, "\r"), scopeMarker)
		if marked && strings.TrimSpace(dir) != "" {
			count++
		}
	}
	return count
}

func scopedBinaries(patterns []string, built int) error {
	if built > 0 || !narrowed(patterns) {
		return nil
	}
	return &Error{
		Code: CodeTestScope,
		Message: "the test command's scope " + strconv.Quote(strings.Join(patterns, " ")) +
			" holds no package with a test file, so every mutant would be reported as having " +
			"survived a suite that was never run",
	}
}

func customTestCommand(command []string) string {
	return "coverage-guided selection is off because test.command " +
		strconv.Quote(strings.Join(command, " ")) +
		" is not `go test` over package patterns: go-mutants understands " +
		strconv.Quote(strings.Join(config.DefaultTestCommand(), " ")) +
		" and any other `go test` followed only by patterns such as `./internal/...`, " +
		"and it cannot tell which of its per-package test binaries any other command's coverage " +
		"belongs to, so every mutant will be measured against every one of them"
}
