// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache_test

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/cache"
	"github.com/P4suta/go-mutants/internal/config"
)

func TestResolveIsTheWholeModeMatrix(t *testing.T) {
	t.Parallel()

	custom := []string{"go", "test", "-run", "TestParser", "./..."}
	cases := []struct {
		name       string
		mode       config.CacheMode
		command    []string
		want       cache.Decision
		wantReason bool
	}{
		{
			name: "off, whatever the command",
			mode: config.CacheOff, command: config.DefaultTestCommand(),
			want: cache.Decision{},
		},
		{
			name: "off with a custom command says nothing extra",
			mode: config.CacheOff, command: custom,
			want: cache.Decision{},
		},
		{
			name: "on with the built-in command",
			mode: config.CacheOn, command: config.DefaultTestCommand(),
			want: cache.Decision{Read: true, Write: true},
		},
		{
			name: "on with a custom command, because the user asked",
			mode: config.CacheOn, command: custom,
			want: cache.Decision{Read: true, Write: true},
		},
		{
			name: "auto with the built-in command",
			mode: config.CacheAuto, command: config.DefaultTestCommand(),
			want: cache.Decision{Read: true, Write: true},
		},
		{
			name: "auto stands down for a custom command",
			mode: config.CacheAuto, command: custom,
			want: cache.Decision{}, wantReason: true,
		},
		{
			name: "a mode this build does not know changes nothing",
			mode: config.CacheMode("sometimes"), command: config.DefaultTestCommand(),
			want: cache.Decision{}, wantReason: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got := cache.Resolve(c.mode, c.command)
			if got.Read != c.want.Read || got.Write != c.want.Write {
				t.Errorf("Resolve(%q) read=%t write=%t, want read=%t write=%t",
					c.mode, got.Read, got.Write, c.want.Read, c.want.Write)
			}
			if got.Enabled() != (c.want.Read || c.want.Write) {
				t.Errorf("Enabled = %t, want %t", got.Enabled(), c.want.Read || c.want.Write)
			}
			if (got.Reason != "") != c.wantReason {
				t.Errorf("reason = %q, want one: %t", got.Reason, c.wantReason)
			}
		})
	}
}

func TestTheAutoReasonSaysWhatToDoAboutIt(t *testing.T) {
	t.Parallel()

	reason := cache.Resolve(config.CacheAuto, []string{"make", "test"}).Reason
	for _, want := range []string{"make test", strings.Join(config.DefaultTestCommand(), " "), "on"} {
		if !strings.Contains(reason, want) {
			t.Errorf("the reason does not mention %q: %s", want, reason)
		}
	}
	if strings.Contains(reason, "\n") {
		t.Errorf("the reason is not one line: %q", reason)
	}
}

func TestReadAndWriteMoveTogether(t *testing.T) {
	t.Parallel()

	for _, mode := range append(config.CacheModes(), config.CacheMode("unknown")) {
		for _, command := range [][]string{config.DefaultTestCommand(), {"make", "test"}} {
			decision := cache.Resolve(mode, command)
			if decision.Read != decision.Write {
				t.Errorf("Resolve(%q, %v) read=%t write=%t", mode, command, decision.Read, decision.Write)
			}
		}
	}
}
