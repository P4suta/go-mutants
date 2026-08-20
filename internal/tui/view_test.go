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

// rawFrame resizes the model and returns the frame exactly as it would be
// written to the terminal, escape sequences and all.
func rawFrame(t *testing.T, h *harness, width, height int) string {
	t.Helper()
	h.send(t, tea.WindowSizeMsg{Width: width, Height: height})
	return h.model.View()
}

// frame resizes the model and returns the painted frame, one line per element,
// with the escape sequences stripped.
//
// The assertions below are substrings and shapes rather than whole-frame
// goldens on purpose. What lipgloss emits depends on the colour profile it was
// built with, and a byte-exact golden of a styled frame would be a test of
// lipgloss's escape sequences rather than of this package's layout — and would
// have to be regenerated every time the library changed one. What is asserted
// instead is what a user would notice: that the numbers are there, that the
// diff is there, and that nothing is wider or taller than the terminal.
//
// Most of these tests run on [asciiTheme], where there is nothing to strip:
// what they assert is the layout. The escape sequences production actually
// emits get their own test, on a theme pinned to a colour profile; see
// TestTheStyledFrameFitsTheTerminalAndClosesEveryStyle.
func frame(t *testing.T, h *harness, width, height int) []string {
	t.Helper()
	return strings.Split(ansi.Strip(rawFrame(t, h, width, height)), "\n")
}

// colourTheme is the theme pinned to a colour profile: the path production
// takes, where every style becomes several bytes that occupy no columns.
func colourTheme(p termenv.Profile) theme {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(p)
	return themeFrom(r)
}

// runInFlight is a run part-way through its mutants: one worker busy, one
// survivor found, one kill recorded, on a three-worker plan.
func runInFlight(t *testing.T) *harness {
	t.Helper()
	return inFlight(t, asciiTheme(), false)
}

// coverageRunInFlight is the same run with a coverage pass behind it: the
// coverage line in the head, and a second survivor that no test binary reaches.
//
// That survivor is what makes the feed's widest case reachable. Its label
// carries internal/console's "(uncovered)" qualifier, which is wider than the
// outcome column, so the part of a feed line that is otherwise fixed is not —
// and on a narrow frame it is wider than the terminal on its own.
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
		// Published after validation and before the first mutant, which is
		// where the engine publishes it.
		h.events(t, engine.CoverageMapped{Binaries: 3, Covered: 40, Uncovered: 7})
	}

	h.clock.advance(40 * time.Second)
	h.events(t, started(killedResult), started(survivorResult))
	h.events(t,
		engine.MutantFinished{Result: killedResult},
		engine.MutantFinished{Result: survivorResult},
	)
	if coverage {
		// Settled without ever being started: nothing ran it, which is what
		// makes it uncovered.
		h.events(t, engine.MutantFinished{Result: uncoveredResult})
	}
	// Worker 0 picks up something else and is still on it when the frame is
	// painted, so that the table has a busy row and two idle ones.
	inFlight := killedResult
	inFlight.DisplayID = "abcdef01234567"
	inFlight.Path = "internal/instrument/flatten.go"
	inFlight.Line = 1204
	inFlight.Rule = "add-to-sub"
	h.events(t, started(inFlight))

	h.clock.advance(2 * time.Second)
	h.send(t, tickMsg(h.clock.now()))
	return h
}

func TestTheFrameFitsTheTerminal(t *testing.T) {
	sizes := []struct{ width, height int }{
		{80, 24},
		{120, 40},
		{50, 20},  // narrower than the compact threshold
		{30, 10},  // absurd, and still must not wrap
		{200, 12}, // wide and short: the feed is what gives way
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

// sgrSequence matches one complete Select Graphic Rendition sequence, which is
// the only kind of escape the styles in this package emit.
var sgrSequence = regexp.MustCompile("\x1b\\[[0-9;]*m")

// assertEscapesAreWhole checks that every escape sequence in a frame survived
// the layout intact.
//
// Two failures are possible and both are invisible to a test that strips the
// escapes before looking. A cut that lands inside a sequence leaves a bare or
// partial ESC, and the terminal then eats however many of the following bytes
// it takes to finish the sequence it was given the start of. A cut that lands
// after a style was opened and before it was closed drops the reset, and the
// colour then applies to everything drawn beneath it until something else
// resets it — for the rest of the alternate screen, on a frame that is
// repainted four times a second.
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
	// Widths are swept rather than sampled. The arithmetic that cuts a styled
	// string only runs at the exact widths where something stops fitting — the
	// header spreads until it cannot, the score line changes spelling below
	// narrowWidth — and where those widths fall depends on the version string
	// and on how long the run has been going. A sweep does not have to be told.
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
		// Past an hour the clock is two columns wider, which moves the header's
		// truncating branch to a terminal wide enough that a user would have one.
		{"hours", time.Hour + 2*time.Minute + 3*time.Second},
	}
	for _, p := range profiles {
		for _, c := range clocks {
			t.Run(p.name+"/"+c.name, func(t *testing.T) {
				th := colourTheme(p.profile)

				// Without this the rest of the test would pass on a theme that
				// emits nothing at all, which is exactly how the styled path
				// went untested before.
				if view := rawFrame(t, coverageRunInFlight(t, th), 80, 24); !strings.Contains(view, "\x1b[") {
					t.Fatalf("the %s profile emitted no escape sequences; this test would assert nothing", p.name)
				}

				for width := minWidth; width <= 100; width++ {
					for _, height := range []int{10, 24} {
						h := coverageRunInFlight(t, th)
						h.clock.advance(c.elapsed)
						h.send(t, tickMsg(h.clock.now()))

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
		// The engine's coverage line, worded as internal/console words it: the
		// uncovered count stated outright rather than left as the remainder.
		"coverage: 3 test binaries, 40 of 47 mutants covered, 7 uncovered",
		// Two survivors now, and the uncovered one carries the qualifier that
		// internal/console's ResultLabel gives it, so that the feed and the
		// summary replayed underneath the dashboard read the same.
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
				// The header: the tool, the run, and the clock.
				"go-mutants 0.1.0-dev",
				"run 20260819T101112Z-a1b2",
				"elapsed 00:42",
				// The phase and what the engine established on the way here.
				"phase mutate:",
				"baseline ok: avg 170ms, slowest 210ms, timeout 10s (derived)",
				"validated 47 mutants, 2 rejections",
				// The gauge: the score, a bar, the progress, and an estimate.
				"score 50.00%",
				"[#",
				"2/47",
				"eta ",
				// The counters, spelled out at this width.
				"killed 1", "survived 1", "timeout 0", "inconcl 0", "errored 0", "not-run 0",
				// The worker table: one busy row and two idle ones.
				"workers",
				"abcdef01", "internal/instrument/flatten.go:1204", "add-to-sub", "2s",
				"idle",
				// The survivor feed: the result line and the two-line diff.
				"survivors 1",
				"SURVIVED", "9f8e7d6c", "internal/report/untested.go:9:12", "neq-to-eq",
				"- !=",
				"+ ==",
				// The help line.
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

	// The counters keep their numbers and give up their words.
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
	// The score survives everywhere; the bar does not.
	if !strings.Contains(view, "score 50.00%") {
		t.Errorf("compact frame lost the score\n--- frame ---\n%s", view)
	}
	if strings.Contains(view, "[#") {
		t.Errorf("compact frame drew a bar it has no room for\n--- frame ---\n%s", view)
	}
	// A path too long for the row is truncated from the middle, keeping the
	// end, which is the half that names the file and the line.
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

	// A short terminal gives the feed up entirely rather than pushing the help
	// line off the screen.
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
	// The estimate is replaced by the state, because a run that is unwinding
	// is not going to take as long as the remaining mutants would have.
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

	// A styled string is cut by columns and copied through by bytes. An escape
	// sequence is several runes wide and no columns wide, so a cut that counted
	// runes would land inside one; what a terminal does with half a sequence is
	// eat the bytes after it, and what it does with a style that was opened and
	// never reset is apply it to the rest of the screen.
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
	// An exact fit keeps every byte, decoration included.
	if got, want := truncate(red.Render("survived 12"), 11), red.Render("survived 12"); got != want {
		t.Errorf("truncate(styled, 11) = %q, want the string untouched %q", got, want)
	}

	// The middle is what a truncated path gives up: the file and the line
	// survive at the end, and the package survives at the start.
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
	// Eight mutants, four workers, a second each: two waves.
	if got != 2*time.Second {
		t.Errorf("estimate = %s, want 2s", got)
	}
	if _, ok := e.estimate(0, 4); ok {
		t.Error("an estimate was produced with nothing left to run")
	}
	// A partial wave still costs a whole one.
	if got, _ := e.estimate(9, 4); got != 3*time.Second {
		t.Errorf("estimate for 9 mutants on 4 workers = %s, want 3s", got)
	}
	// Zero workers cannot divide, and are treated as one rather than panicking.
	if got, _ := e.estimate(3, 0); got != 3*time.Second {
		t.Errorf("estimate with no workers = %s, want 3s", got)
	}

	// The mean follows the run: a slower mutant moves it without dominating it.
	before := e.mean
	e.observe(11 * time.Second)
	if e.mean <= before || e.mean >= 11*time.Second {
		t.Errorf("mean = %s after a slow observation, want it between %s and 11s", e.mean, before)
	}
}
