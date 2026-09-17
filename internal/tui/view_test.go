// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tui

import (
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/P4suta/go-mutants/internal/engine"
)

func rawFrame(t *testing.T, h *harness, width, height int) string {
	t.Helper()
	h.send(t, tea.WindowSizeMsg{Width: width, Height: height})
	return h.model.View()
}

func frame(t *testing.T, h *harness, width, height int) []string {
	t.Helper()
	return strings.Split(ansi.Strip(rawFrame(t, h, width, height)), "\n")
}

func colourTheme(p termenv.Profile) theme {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(p)
	return themeFrom(r)
}

func runInFlight(t *testing.T) *harness {
	t.Helper()
	return inFlight(t, asciiTheme(), false)
}

func coverageRunInFlight(t *testing.T, th theme) *harness {
	t.Helper()
	return inFlight(t, th, true)
}

func inFlight(t *testing.T, th theme, coverage bool) *harness {
	t.Helper()
	h := newThemedHarness(t, th)
	h.events(t, planned(3)...)
	h.events(t, engine.Validated{Accepted: 47, Rejected: 2})
	if coverage {
		h.events(t, engine.CoverageMapped{Binaries: 3, Covered: 40, Uncovered: 7})
	}

	h.clock.Advance(40 * time.Second)
	h.events(t, started(killedResult), started(survivorResult))
	h.events(t,
		engine.MutantFinished{Result: killedResult},
		engine.MutantFinished{Result: survivorResult},
	)
	if coverage {
		h.events(t, engine.MutantFinished{Result: uncoveredResult})
	}
	inFlight := killedResult
	inFlight.DisplayID = "abcdef01234567"
	inFlight.Path = "internal/instrument/flatten.go"
	inFlight.Line = 1204
	inFlight.Rule = "add-to-sub"
	h.events(t, started(inFlight))

	h.clock.Advance(2 * time.Second)
	h.send(t, tickMsg(h.clock.Now()))
	return h
}

func TestTheFrameFitsTheTerminal(t *testing.T) {
	sizes := []struct{ width, height int }{
		{80, 24},
		{120, 40},
		{50, 20},
		{30, 10},
		{200, 12},
	}
	for _, size := range sizes {
		t.Run(strconv.Itoa(size.width)+"x"+strconv.Itoa(size.height), func(t *testing.T) {
			h := runInFlight(t)
			lines := frame(t, h, size.width, size.height)
			if len(lines) != size.height {
				t.Errorf("frame is %d lines tall, want %d", len(lines), size.height)
			}
			for i, line := range lines {
				if got := ansi.StringWidth(line); got > size.width {
					t.Errorf("line %d is %d columns wide, want at most %d: %q", i, got, size.width, line)
				}
			}
		})
	}
}

var sgrSequence = regexp.MustCompile("\x1b\\[[0-9;]*m")

func assertEscapesAreWhole(t *testing.T, what, frame string) {
	t.Helper()
	open := ""
	for _, seq := range sgrSequence.FindAllString(frame, -1) {
		if seq == "\x1b[0m" || seq == "\x1b[m" {
			open = ""
			continue
		}
		if open != "" {
			t.Errorf("%s: style %q was opened and never reset before %q opened another\n%q", what, open, seq, frame)
			return
		}
		open = seq
	}
	if open != "" {
		t.Errorf("%s: the frame ends with %q still open, which leaks into every line drawn under it\n%q", what, open, frame)
		return
	}
	if rest := sgrSequence.ReplaceAllString(frame, ""); strings.ContainsRune(rest, '\x1b') {
		i := strings.IndexRune(rest, '\x1b')
		t.Errorf("%s: the frame contains an incomplete escape sequence %q\n%q", what, rest[i:min(i+16, len(rest))], frame)
	}
}

func TestTheStyledFrameFitsTheTerminalAndClosesEveryStyle(t *testing.T) {
	profiles := []struct {
		name    string
		profile termenv.Profile
	}{
		{"truecolor", termenv.TrueColor},
		{"ansi", termenv.ANSI},
	}
	clocks := []struct {
		name    string
		elapsed time.Duration
	}{
		{"minutes", 0},
		{"hours", time.Hour + 2*time.Minute + 3*time.Second},
	}
	for _, p := range profiles {
		for _, c := range clocks {
			t.Run(p.name+"/"+c.name, func(t *testing.T) {
				th := colourTheme(p.profile)

				if view := rawFrame(t, coverageRunInFlight(t, th), 80, 24); !strings.Contains(view, "\x1b[") {
					t.Fatalf("the %s profile emitted no escape sequences; this test would assert nothing", p.name)
				}

				for width := minWidth; width <= 100; width++ {
					for _, height := range []int{10, 24} {
						h := coverageRunInFlight(t, th)
						h.clock.Advance(c.elapsed)
						h.send(t, tickMsg(h.clock.Now()))

						what := strconv.Itoa(width) + "x" + strconv.Itoa(height)
						view := rawFrame(t, h, width, height)
						lines := strings.Split(view, "\n")
						if len(lines) != height {
							t.Fatalf("%s: frame is %d lines tall, want %d", what, len(lines), height)
						}
						for i, line := range lines {
							if got := ansi.StringWidth(line); got > width {
								t.Fatalf("%s: line %d is %d columns wide: %q", what, i, got, line)
							}
						}
						assertEscapesAreWhole(t, what, view)
						if t.Failed() {
							return
						}
					}
				}
			})
		}
	}
}

func TestTheCoveragePassAndTheUncoveredLabelReachTheFrame(t *testing.T) {
	h := coverageRunInFlight(t, asciiTheme())
	view := strings.Join(frame(t, h, 100, 30), "\n")

	want := []string{
		"coverage: 3 test binaries, 40 of 47 mutants covered, 7 uncovered",
		"survivors 2",
		"SURVIVED (uncovered)",
		"5c5c5c5c", "internal/glob/glob.go:88:3", "true-to-false",
		"- true",
		"+ false",
	}
	for _, s := range want {
		if !strings.Contains(view, s) {
			t.Errorf("frame does not contain %q\n--- frame ---\n%s", s, view)
		}
	}
}

func TestTheFrameShowsWhatTheRunIsDoing(t *testing.T) {
	sizes := []struct{ width, height int }{{80, 24}, {120, 40}}
	for _, size := range sizes {
		t.Run(strconv.Itoa(size.width)+"x"+strconv.Itoa(size.height), func(t *testing.T) {
			h := runInFlight(t)
			view := strings.Join(frame(t, h, size.width, size.height), "\n")

			want := []string{
				"go-mutants 0.1.0-dev",
				"run 20260819T101112Z-a1b2",
				"elapsed 00:42",
				"phase mutate:",
				"baseline ok: avg 170ms, slowest 210ms, timeout 10s (derived)",
				"validated 47 mutants, 2 rejections",
				"score 50.00%",
				"[#",
				"2/47",
				"eta ",
				"killed 1", "survived 1", "timeout 0", "inconcl 0", "errored 0", "not-run 0",
				"workers",
				"abcdef01", "internal/instrument/flatten.go:1204", "add-to-sub", "2s",
				"idle",
				"survivors 1",
				"SURVIVED", "9f8e7d6c", "internal/report/untested.go:9:12", "neq-to-eq",
				"- !=",
				"+ ==",
				"ctrl+c: stop, publish partial report",
			}
			for _, s := range want {
				if !strings.Contains(view, s) {
					t.Errorf("frame does not contain %q\n--- frame ---\n%s", s, view)
				}
			}
		})
	}
}

func TestANarrowFrameDropsWhatWillNotFit(t *testing.T) {
	h := runInFlight(t)
	view := strings.Join(frame(t, h, 50, 20), "\n")

	for _, s := range []string{"k 1", "s 1", "t 0", "i 0", "e 0", "n 0"} {
		if !strings.Contains(view, s) {
			t.Errorf("compact frame does not contain %q\n--- frame ---\n%s", s, view)
		}
	}
	for _, s := range []string{"killed 1", "survived 1"} {
		if strings.Contains(view, s) {
			t.Errorf("compact frame still spells out %q, which does not fit\n--- frame ---\n%s", s, view)
		}
	}
	if !strings.Contains(view, "score 50.00%") {
		t.Errorf("compact frame lost the score\n--- frame ---\n%s", view)
	}
	if strings.Contains(view, "[#") {
		t.Errorf("compact frame drew a bar it has no room for\n--- frame ---\n%s", view)
	}
	if !strings.Contains(view, ellipsis) {
		t.Errorf("compact frame did not truncate the long paths\n--- frame ---\n%s", view)
	}
	if !strings.Contains(view, "flatten.go:1204") {
		t.Errorf("compact frame truncated away the file and line\n--- frame ---\n%s", view)
	}
}

func TestResizingRecomputesTheFeed(t *testing.T) {
	h := runInFlight(t)

	frame(t, h, 80, 24)
	tall := h.model.feed.Height
	frame(t, h, 80, 40)
	taller := h.model.feed.Height
	if taller <= tall {
		t.Errorf("feed height %d on a 40-line terminal, want more than the %d it had on 24", taller, tall)
	}

	lines := frame(t, h, 80, 8)
	if len(lines) != 8 {
		t.Fatalf("frame is %d lines tall, want 8", len(lines))
	}
	if !strings.Contains(lines[len(lines)-1], "ctrl+c") {
		t.Errorf("the last line of a short frame is %q, want the help line", lines[len(lines)-1])
	}
}

func TestTheHelpLineSaysWhatCtrlCDidLastTime(t *testing.T) {
	h := runInFlight(t)
	if got := strings.Join(frame(t, h, 80, 24), "\n"); !strings.Contains(got, "ctrl+c: stop, publish partial report") {
		t.Errorf("help line missing before ctrl+c\n%s", got)
	}

	h.send(t, tea.KeyMsg{Type: tea.KeyCtrlC})
	view := strings.Join(frame(t, h, 80, 24), "\n")
	for _, s := range []string{"stopping", "ctrl+c again"} {
		if !strings.Contains(view, s) {
			t.Errorf("frame after ctrl+c does not contain %q\n--- frame ---\n%s", s, view)
		}
	}
	if strings.Contains(view, "eta ") {
		t.Errorf("frame still shows an eta while stopping\n--- frame ---\n%s", view)
	}
}

func TestAFrameBeforeAnythingHappensIsStillAFrame(t *testing.T) {
	h := newHarness(t)
	lines := frame(t, h, 80, 24)
	if len(lines) != 24 {
		t.Fatalf("frame is %d lines tall, want 24", len(lines))
	}
	view := strings.Join(lines, "\n")
	if !strings.Contains(view, "score n/a") {
		t.Errorf("a run with nothing measured does not say so\n--- frame ---\n%s", view)
	}
	if !strings.Contains(view, "0 done") {
		t.Errorf("a run with no catalogue yet invented a denominator\n--- frame ---\n%s", view)
	}
	if !strings.Contains(view, "survivors none yet") {
		t.Errorf("the feed title is missing\n--- frame ---\n%s", view)
	}
}

func TestTruncation(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"a short string is left alone", truncate("abc", 10), "abc"},
		{"an exact fit is left alone", truncate("abcdef", 6), "abcdef"},
		{"a long string is cut and marked", truncate("abcdefghij", 6), "abc..."},
		{"no room for the mark", truncate("abcdefghij", 2), "ab"},
		{"no room at all", truncate("abc", 0), ""},
		{"a short path is left alone", truncatePath("a/b.go:12", 20), "a/b.go:12"},
		{"an unpaddable path keeps its tail", truncatePath("internal/engine/engine.go:412", 5), "o:412"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}

	red := colourTheme(termenv.TrueColor).failed
	for _, width := range []int{1, 2, 3, 4, 6, 8, 10, 11} {
		got := truncate(red.Render("survived 12"), width)
		if visible := ansi.StringWidth(got); visible > width {
			t.Errorf("truncate(styled, %d) is %d columns wide: %q", width, visible, got)
		}
		if plain := ansi.Strip(got); !strings.HasPrefix("survived 12", strings.TrimSuffix(plain, ellipsis)) {
			t.Errorf("truncate(styled, %d) mangled the text: %q", width, plain)
		}
		assertEscapesAreWhole(t, "truncate(styled, "+strconv.Itoa(width)+")", got)
	}
	if got, want := truncate(red.Render("survived 12"), 11), red.Render("survived 12"); got != want {
		t.Errorf("truncate(styled, 11) = %q, want the string untouched %q", got, want)
	}

	got := truncatePath("internal/engine/deep/deeper/engine.go:412", 24)
	if len([]rune(got)) != 24 {
		t.Errorf("truncatePath produced %d columns, want 24: %q", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, "engine.go:412") {
		t.Errorf("truncatePath dropped the file and line: %q", got)
	}
	if !strings.HasPrefix(got, "int") {
		t.Errorf("truncatePath dropped the head: %q", got)
	}
	if !strings.Contains(got, ellipsis) {
		t.Errorf("truncatePath did not mark the cut: %q", got)
	}
}

func TestFormatClock(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "00:00"},
		{-time.Second, "00:00"},
		{999 * time.Millisecond, "00:01"},
		{42 * time.Second, "00:42"},
		{90 * time.Second, "01:30"},
		{59*time.Minute + 59*time.Second, "59:59"},
		{time.Hour + 2*time.Minute + 3*time.Second, "1:02:03"},
	}
	for _, tc := range tests {
		if got := formatClock(tc.in); got != tc.want {
			t.Errorf("formatClock(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGauge(t *testing.T) {
	theme := asciiTheme()
	tests := []struct {
		name     string
		fraction float64
		width    int
		want     string
	}{
		{"empty", 0, 4, "----"},
		{"full", 1, 4, "####"},
		{"half", 0.5, 4, "##--"},
		{"a sliver still shows", 0.01, 10, "#---------"},
		{"nearly full still shows the gap", 0.99, 10, "#########-"},
		{"out of range low", -1, 4, "----"},
		{"out of range high", 2, 4, "####"},
		{"no width", 0.5, 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ansi.Strip(gauge(tc.fraction, tc.width, theme.gaugeOn, theme.gaugeOff))
			if got != tc.want {
				t.Errorf("gauge(%v, %d) = %q, want %q", tc.fraction, tc.width, got, tc.want)
			}
		})
	}
}

func TestEstimator(t *testing.T) {
	var e estimator
	if _, ok := e.estimate(10, 4); ok {
		t.Error("an estimator with no observations produced an estimate")
	}

	e.observe(0)
	if _, ok := e.estimate(10, 4); ok {
		t.Error("a zero duration — a mutant that never ran — was treated as an observation")
	}

	e.observe(time.Second)
	got, ok := e.estimate(8, 4)
	if !ok {
		t.Fatal("no estimate after an observation")
	}
	if got != 2*time.Second {
		t.Errorf("estimate = %s, want 2s", got)
	}
	if _, ok := e.estimate(0, 4); ok {
		t.Error("an estimate was produced with nothing left to run")
	}
	if got, _ := e.estimate(9, 4); got != 3*time.Second {
		t.Errorf("estimate for 9 mutants on 4 workers = %s, want 3s", got)
	}
	if got, _ := e.estimate(3, 0); got != 3*time.Second {
		t.Errorf("estimate with no workers = %s, want 3s", got)
	}

	before := e.mean
	e.observe(11 * time.Second)
	if e.mean <= before || e.mean >= 11*time.Second {
		t.Errorf("mean = %s after a slow observation, want it between %s and 11s", e.mean, before)
	}
}
