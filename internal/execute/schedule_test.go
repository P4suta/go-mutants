// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

// attemptCounter counts how many attempts each mutant has had, so that a fake
// can answer the first attempt differently from the retry. It is the whole
// mechanism the timeout policy tests are built on.
type attemptCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func newAttemptCounter() *attemptCounter { return &attemptCounter{n: map[string]int{}} }

// next records an attempt at id and returns which attempt it is, counting
// from one.
func (a *attemptCounter) next(id string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n[id]++
	return a.n[id]
}

// activeOf reads the mutant a call activated. Every scheduled call carries one.
func activeOf(c call) string { return c.active() }

// TestScheduleReturnsResultsInTheInputOrder pins determinism.
//
// Twenty mutants over four workers finish in whatever order the scheduler and
// the machine agree on, and none of that may reach the report: a run whose
// result order moved between invocations would produce a different diff every
// time for the same code.
func TestScheduleReturnsResultsInTheInputOrder(t *testing.T) {
	ids := make([]string, 20)
	for i := range ids {
		ids[i] = "mutant-" + string(rune('a'+i))
	}
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		// One mutant in the middle of the queue is killed, so the outcomes vary
		// along the slice and an order assertion is about more than identities.
		if strings.HasSuffix(activeOf(c), "c") {
			return failed("--- FAIL: TestX\n")
		}
		return passed()
	}}

	results, err := execute.Schedule(t.Context(), options(f, 4),
		mutants(mutantTimeout, ids...), testBins("example.com/a"), execute.Hooks{})
	if err != nil {
		t.Fatalf("scheduling: %v", err)
	}

	got := make([]string, len(results))
	for i, r := range results {
		got[i] = r.ID
	}
	if !slices.Equal(got, ids) {
		t.Errorf("results came back as %q, want the input order %q", got, ids)
	}
	for i, r := range results {
		if r.Final == mutation.OutcomeNotRun {
			t.Errorf("result %d (%s) was never settled", i, r.ID)
		}
	}
}

// TestScheduleConfirmsATimeoutOnlyWhenItRepeats is the detecting half of the
// retry rule: two timeouts in a row are a confirmed detection, and both
// attempts stay in the record so the report can show the confirmation happened.
func TestScheduleConfirmsATimeoutOnlyWhenItRepeats(t *testing.T) {
	f := &fake{respond: func(context.Context, call) runner.Result { return timedOut() }}

	results, err := execute.Schedule(t.Context(), options(f, 2),
		mutants(mutantTimeout, "slow"), testBins("example.com/a"), execute.Hooks{})
	if err != nil {
		t.Fatalf("scheduling: %v", err)
	}

	got := results[0]
	if got.Final != mutation.OutcomeTimedOut {
		t.Errorf("final outcome = %s, want %s", got.Final, mutation.OutcomeTimedOut)
	}
	if !got.Final.Detected() {
		t.Error("a confirmed timeout must count as detection")
	}
	if len(got.Attempts) != 2 {
		t.Fatalf("kept %d attempts, want both", len(got.Attempts))
	}
	for i, attempt := range got.Attempts {
		if attempt.Outcome != mutation.OutcomeTimedOut {
			t.Errorf("attempt %d = %s, want %s", i+1, attempt.Outcome, mutation.OutcomeTimedOut)
		}
	}
	if want := "example.com/a"; got.KilledBy != want {
		t.Errorf("detected by %q, want %q", got.KilledBy, want)
	}
	if want := 2 * time.Millisecond; got.Duration != want {
		t.Errorf("duration = %s, want %s (both attempts)", got.Duration, want)
	}
}

// TestScheduleCallsAMixedTimeoutInconclusive is the other half, and the reason
// the retry exists at all. A timeout that does not reproduce on an idle machine
// said something about the machine; reporting it as a detection would inflate
// the score exactly where the run is least entitled to.
//
// Both spellings of "the retry finished" are pinned, because the tempting bug
// is to treat a failing retry as confirmation of the timeout.
func TestScheduleCallsAMixedTimeoutInconclusive(t *testing.T) {
	cases := []struct {
		name  string
		retry func() runner.Result
	}{
		{"the retry passes", passed},
		{"the retry fails", func() runner.Result { return failed("--- FAIL: TestX\n") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			counter := newAttemptCounter()
			f := &fake{respond: func(_ context.Context, call call) runner.Result {
				if counter.next(activeOf(call)) == 1 {
					return timedOut()
				}
				return c.retry()
			}}

			results, err := execute.Schedule(t.Context(), options(f, 2),
				mutants(mutantTimeout, "flaky"), testBins("example.com/a"), execute.Hooks{})
			if err != nil {
				t.Fatalf("scheduling: %v", err)
			}

			got := results[0]
			if got.Final != mutation.OutcomeInconclusive {
				t.Errorf("final outcome = %s, want %s: disagreeing attempts are not evidence",
					got.Final, mutation.OutcomeInconclusive)
			}
			if got.Final.Detected() {
				t.Error("an inconclusive outcome must never count as detection")
			}
			if len(got.Attempts) != 2 {
				t.Fatalf("kept %d attempts, want both", len(got.Attempts))
			}
			if got.Attempts[0].Outcome != mutation.OutcomeTimedOut {
				t.Errorf("first attempt = %s, want the timeout that was retried", got.Attempts[0].Outcome)
			}
		})
	}
}

// TestScheduleLeavesAnUnconfirmedTimeoutNotRun covers the corner where the
// retry policy meets Ctrl-C: a mutant that timed out once and was cancelled
// before anybody could try to reproduce it.
//
// One timeout nobody was able to repeat measures nothing, so the verdict stays
// [mutation.OutcomeNotRun] — and under cancellation that *is* the settled
// outcome, which is why Finished fires for it. The blanket reading in
// TestScheduleFinishesEachMutantExactlyOnce, that a not-run mutant announced as
// finished was announced too early, holds only for a run that completed.
//
// What this branch really owns is the attempt. The timeout that did happen
// stays in the record, so a report can say the run tried once and gave up,
// and no second process is ever started.
func TestScheduleLeavesAnUnconfirmedTimeoutNotRun(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := &fake{respond: func(context.Context, call) runner.Result {
		// The plug is pulled while the first attempt is still in flight, so the
		// timeout is recorded and the retry pass never gets its turn.
		cancel()
		return timedOut()
	}}

	var mu sync.Mutex
	var started, finished int
	hooks := execute.Hooks{
		Started: func(string, int) {
			mu.Lock()
			defer mu.Unlock()
			started++
		},
		Finished: func(execute.MutantResult) {
			mu.Lock()
			defer mu.Unlock()
			finished++
		},
	}

	results, err := execute.Schedule(ctx, options(f, 2),
		mutants(mutantTimeout, "slow"), testBins("example.com/a"), hooks)

	if code := execute.CodeOf(err); code != execute.CodeInterrupted {
		t.Errorf("code = %q, want %q (%v)", code, execute.CodeInterrupted, err)
	}
	if !isCancellation(err) {
		t.Errorf("errors.Is(%v, context.Canceled) is false; internal/engine recognises a cancellation that way", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}

	got := results[0]
	if got.Final != mutation.OutcomeNotRun {
		t.Errorf("final outcome = %s, want %s: an unrepeated timeout is not a measurement",
			got.Final, mutation.OutcomeNotRun)
	}
	if got.Final.Detected() {
		t.Error("an unconfirmed timeout must never count as detection")
	}
	if got.KilledBy != "" {
		t.Errorf("credited %q with a detection that was never confirmed", got.KilledBy)
	}
	// The heart of it: a retry that ran anyway would leave two attempts, and a
	// branch that threw the evidence away would leave none.
	if len(got.Attempts) != 1 {
		t.Fatalf("kept %d attempts, want the one timeout that really happened", len(got.Attempts))
	}
	if got.Attempts[0].Outcome != mutation.OutcomeTimedOut {
		t.Errorf("the kept attempt = %s, want %s", got.Attempts[0].Outcome, mutation.OutcomeTimedOut)
	}
	if calls := len(f.seen()); calls != 1 {
		t.Errorf("started %d children, want 1: a cancelled run must not begin the retry", calls)
	}

	mu.Lock()
	defer mu.Unlock()
	if started != 1 {
		t.Errorf("Started fired %d times, want 1 (the attempt that happened)", started)
	}
	if finished != 1 {
		t.Errorf("Finished fired %d times, want 1: a mutant the run gave up on is still settled", finished)
	}
}

// TestScheduleLetsAFailedRetryStandInsteadOfPromotingTheTimeout covers the
// third arm of the retry rule, the one that is neither a confirmation nor a
// disagreement: a retry that established nothing at all.
//
// The tempting bug is to read "the retry did not finish cleanly" as a second
// timeout and call the mutant detected. It is not one. A binary that could not
// be started, or a child killed without a status, says something about the
// machine or about go-mutants, so the retry's own outcome stands — and the
// mutant is not credited to the binary it once hung on.
func TestScheduleLetsAFailedRetryStandInsteadOfPromotingTheTimeout(t *testing.T) {
	cases := []struct {
		name string
		// retry answers the second attempt. It is handed the run's cancel so
		// that the killed-mid-flight case can produce the state internal/runner
		// really reports for one.
		retry func(cancel context.CancelFunc) runner.Result
		// wantFinal is the verdict the retry's own outcome must impose.
		wantFinal mutation.Outcome
		// wantMutantCode is the code on the result's error, or "" when the
		// outcome carries none.
		wantMutantCode execute.Code
		// wantRunCode is the code Schedule itself returns, or "" for success.
		wantRunCode execute.Code
	}{
		{
			name:           "the retry could not be started",
			retry:          func(context.CancelFunc) runner.Result { return unstartable() },
			wantFinal:      mutation.OutcomeErrored,
			wantMutantCode: execute.CodeMutantStart,
		},
		{
			name: "the retry was killed mid-flight",
			retry: func(cancel context.CancelFunc) runner.Result {
				cancel()
				return cancelled()
			},
			wantFinal:   mutation.OutcomeNotRun,
			wantRunCode: execute.CodeInterrupted,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			counter := newAttemptCounter()
			f := &fake{respond: func(_ context.Context, call call) runner.Result {
				if counter.next(activeOf(call)) == 1 {
					return timedOut()
				}
				return c.retry(cancel)
			}}

			results, err := execute.Schedule(ctx, options(f, 2),
				mutants(mutantTimeout, "slow"), testBins("example.com/a"), execute.Hooks{})
			if code := execute.CodeOf(err); code != c.wantRunCode {
				t.Errorf("Schedule returned code %q, want %q (%v)", code, c.wantRunCode, err)
			}
			if len(results) != 1 {
				t.Fatalf("got %d results, want 1", len(results))
			}

			got := results[0]
			if got.Final != c.wantFinal {
				t.Errorf("final outcome = %s, want %s: a retry that established nothing does not confirm the timeout",
					got.Final, c.wantFinal)
			}
			if got.Final.Detected() {
				t.Error("a retry that established nothing must never count as detection")
			}
			if code := execute.CodeOf(got.Err); code != c.wantMutantCode {
				t.Errorf("code = %q, want %q (%v)", code, c.wantMutantCode, got.Err)
			}
			// The tell of the bug this pins: promoting the timeout would carry
			// the name of the binary it hung on into the verdict with it.
			if got.KilledBy != "" {
				t.Errorf("credited %q with a detection, want none", got.KilledBy)
			}
			if len(got.Attempts) != 2 {
				t.Fatalf("kept %d attempts, want both", len(got.Attempts))
			}
			if got.Attempts[0].Outcome != mutation.OutcomeTimedOut {
				t.Errorf("first attempt = %s, want the timeout that was retried", got.Attempts[0].Outcome)
			}
			// Where an interruption goes: onto the attempt that was cut off,
			// naming the binary still running, and never onto the verdict — a
			// mutant nobody measured did not error, which is what the
			// wantMutantCode assertion above is the other half of.
			if c.wantFinal == mutation.OutcomeNotRun {
				retry := got.Attempts[1].Err
				if code := execute.CodeOf(retry); code != execute.CodeInterrupted {
					t.Errorf("the cut-off attempt carries code %q, want %q (%v)",
						code, execute.CodeInterrupted, retry)
				}
				var failure *execute.Error
				if errors.As(retry, &failure) && failure.Command() == nil {
					t.Error("the cut-off attempt names no command, so nothing says what was still running")
				}
			}
		})
	}
}

// TestScheduleRetriesTimeoutsOneAtATime is the serialisation the whole retry
// policy rests on.
//
// A retry run alongside the rest of the queue would be measuring the same
// loaded machine that produced the first timeout, and the confirmation would
// confirm nothing. The check is an in-flight counter rather than a sleep: a
// sleep alone can pass by luck on a machine that happened not to interleave.
func TestScheduleRetriesTimeoutsOneAtATime(t *testing.T) {
	const jobs = 4

	counter := newAttemptCounter()
	var inFlight atomic.Int64
	var overlaps atomic.Int64
	var retries atomic.Int64

	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		concurrent := inFlight.Add(1)
		defer inFlight.Add(-1)

		if counter.next(activeOf(c)) == 1 {
			return timedOut()
		}
		retries.Add(1)
		// Held long enough that a parallel retry would be seen, and asserted on
		// rather than merely hoped about: nothing else — retry or main pass —
		// may be running while this one is.
		if concurrent > 1 {
			overlaps.Add(1)
		}
		time.Sleep(2 * time.Millisecond)
		if inFlight.Load() > 1 {
			overlaps.Add(1)
		}
		return passed()
	}}

	ids := []string{"m0", "m1", "m2", "m3", "m4", "m5", "m6", "m7"}
	results, err := execute.Schedule(t.Context(), options(f, jobs),
		mutants(mutantTimeout, ids...), testBins("example.com/a"), execute.Hooks{})
	if err != nil {
		t.Fatalf("scheduling: %v", err)
	}

	if got := retries.Load(); got != int64(len(ids)) {
		t.Errorf("%d mutants were retried, want %d", got, len(ids))
	}
	if got := overlaps.Load(); got != 0 {
		t.Errorf("%d retries overlapped with another run; the retry pass must be serial", got)
	}
	for _, r := range results {
		if r.Final != mutation.OutcomeInconclusive {
			t.Errorf("%s = %s, want %s", r.ID, r.Final, mutation.OutcomeInconclusive)
		}
	}
}

// TestScheduleRetriesWithTheSameTimeout pins that the retry is the same
// experiment run again. A retry given a longer budget would answer a different
// question, and "it finished when we let it run longer" is not evidence that
// the first timeout was noise.
func TestScheduleRetriesWithTheSameTimeout(t *testing.T) {
	counter := newAttemptCounter()
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if counter.next(activeOf(c)) == 1 {
			return timedOut()
		}
		return passed()
	}}

	if _, err := execute.Schedule(t.Context(), options(f, 2),
		mutants(mutantTimeout, "slow"), testBins("example.com/a"), execute.Hooks{}); err != nil {
		t.Fatalf("scheduling: %v", err)
	}

	seen := f.seen()
	if len(seen) != 2 {
		t.Fatalf("made %d attempts, want 2", len(seen))
	}
	for i, c := range seen {
		if c.Timeout != mutantTimeout {
			t.Errorf("attempt %d ran with timeout %s, want %s", i+1, c.Timeout, mutantTimeout)
		}
	}
}

// TestScheduleFinishesEachMutantExactlyOnce pins the hook contract the retry
// policy forces: an attempt is announced every time one starts, and a mutant is
// finished only when its outcome is settled — so a timed-out mutant is started
// twice and finished once, and never finished with the unconfirmed timeout.
func TestScheduleFinishesEachMutantExactlyOnce(t *testing.T) {
	counter := newAttemptCounter()
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if activeOf(c) == "slow" && counter.next("slow") == 1 {
			return timedOut()
		}
		return passed()
	}}

	var mu sync.Mutex
	started := map[string]int{}
	finished := map[string]int{}
	var finals []mutation.Outcome
	var workers []int

	hooks := execute.Hooks{
		Started: func(id string, worker int) {
			mu.Lock()
			defer mu.Unlock()
			started[id]++
			workers = append(workers, worker)
		},
		Finished: func(result execute.MutantResult) {
			mu.Lock()
			defer mu.Unlock()
			finished[result.ID]++
			finals = append(finals, result.Final)
		},
	}

	const jobs = 2
	if _, err := execute.Schedule(t.Context(), options(f, jobs),
		mutants(mutantTimeout, "quick", "slow"), testBins("example.com/a"), hooks); err != nil {
		t.Fatalf("scheduling: %v", err)
	}

	if want := map[string]int{"quick": 1, "slow": 2}; !mapsEqual(started, want) {
		t.Errorf("Started fired %v, want %v (once per attempt, and the timeout is retried)", started, want)
	}
	if want := map[string]int{"quick": 1, "slow": 1}; !mapsEqual(finished, want) {
		t.Errorf("Finished fired %v, want %v (once per mutant, only when settled)", finished, want)
	}
	for _, final := range finals {
		if final == mutation.OutcomeNotRun {
			t.Error("a mutant was announced as finished before its outcome was settled")
		}
	}
	for _, worker := range workers {
		if worker < 0 || worker >= jobs {
			t.Errorf("attempt announced on worker %d, want one of 0..%d", worker, jobs-1)
		}
	}
}

// TestScheduleHandsFinishedItsOwnAttempts proves a hook may keep what it is
// given: the slice it receives does not alias the one Schedule goes on to
// return, so a renderer that stored the result cannot be rewritten underneath.
func TestScheduleHandsFinishedItsOwnAttempts(t *testing.T) {
	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}

	var kept []execute.MutantResult
	hooks := execute.Hooks{Finished: func(result execute.MutantResult) {
		kept = append(kept, result)
	}}

	results, err := execute.Schedule(t.Context(), options(f, 1),
		mutants(mutantTimeout, "one"), testBins("example.com/a"), hooks)
	if err != nil {
		t.Fatalf("scheduling: %v", err)
	}
	if len(kept) != 1 || len(kept[0].Attempts) != 1 {
		t.Fatalf("the hook kept %d results", len(kept))
	}
	if &kept[0].Attempts[0] == &results[0].Attempts[0] {
		t.Error("the hook's attempts alias the returned ones")
	}
}

// TestScheduleReportsAStaleCatalogAsErrored carries the exit-97 rule through
// the scheduler, where it decides an entry in the report rather than a return
// value.
func TestScheduleReportsAStaleCatalogAsErrored(t *testing.T) {
	f := &fake{respond: func(context.Context, call) runner.Result { return staleCatalog() }}

	results, err := execute.Schedule(t.Context(), options(f, 2),
		mutants(mutantTimeout, "unknown"), testBins("example.com/a"), execute.Hooks{})
	if err != nil {
		t.Fatalf("scheduling: %v", err)
	}

	got := results[0]
	if got.Final != mutation.OutcomeErrored {
		t.Errorf("final outcome = %s, want %s", got.Final, mutation.OutcomeErrored)
	}
	if code := execute.CodeOf(got.Err); code != execute.CodeStaleCatalog {
		t.Errorf("code = %q, want %q (%v)", code, execute.CodeStaleCatalog, got.Err)
	}
	if got.Final.Detected() {
		t.Error("a stale catalogue must never count as detection")
	}
}

// TestScheduleReturnsNotRunAfterCancellation pins the Ctrl-C contract: the
// mutants that were measured keep their outcomes, everything else comes back
// not run, and the caller is told the run was interrupted in a form
// internal/engine already recognises.
//
// It runs at two widths deliberately. With a single worker the early return is
// indistinguishable from a pool that merely happened to stop; with four, several
// workers observe the cancellation at once and the assertions are about the
// pool draining rather than about one goroutine noticing.
func TestScheduleReturnsNotRunAfterCancellation(t *testing.T) {
	for _, jobs := range []int{1, 4} {
		t.Run("with "+countNoun(jobs, "worker"), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			var done atomic.Int64
			f := &fake{respond: func(ctx context.Context, _ call) runner.Result {
				// The first call always answers normally, so there is always
				// something measured to assert about; the second pulls the plug.
				if done.Add(1) == 2 {
					cancel()
				}
				if ctx.Err() != nil {
					return cancelled()
				}
				return passed()
			}}

			ids := make([]string, 40)
			for i := range ids {
				ids[i] = "m" + string(rune('a'+i%26)) + string(rune('0'+i/26))
			}
			results, err := execute.Schedule(ctx, options(f, jobs),
				mutants(mutantTimeout, ids...), testBins("example.com/a"), execute.Hooks{})

			if code := execute.CodeOf(err); code != execute.CodeInterrupted {
				t.Errorf("code = %q, want %q (%v)", code, execute.CodeInterrupted, err)
			}
			if !isCancellation(err) {
				t.Errorf("errors.Is(%v, context.Canceled) is false; internal/engine recognises a cancellation that way", err)
			}
			if len(results) != len(ids) {
				t.Fatalf("got %d results, want one per mutant (%d)", len(results), len(ids))
			}
			var measured, notRun int
			for _, r := range results {
				if r.Final == mutation.OutcomeNotRun {
					notRun++
					continue
				}
				measured++
			}
			if measured == 0 {
				t.Error("no mutant was measured before the cancellation; the test proves nothing")
			}
			if notRun == 0 {
				t.Error("every mutant was measured; the cancellation did not stop the queue")
			}
			// The queue must stop rather than drain: far fewer starts than mutants.
			if got := len(f.seen()); got >= len(ids) {
				t.Errorf("started %d children for %d mutants; a cancelled run must stop taking work",
					got, len(ids))
			}
		})
	}
}

// countNoun renders "1 worker" or "4 workers" for a subtest name.
func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// TestScheduleLeavesNoGoroutinesBehind proves the shutdown is a join and not a
// hope. Schedule returning while a worker is still starting processes would
// leave test binaries running after the run reported its results.
func TestScheduleLeavesNoGoroutinesBehind(t *testing.T) {
	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(t.Context())
	var done atomic.Int64
	f := &fake{respond: func(ctx context.Context, _ call) runner.Result {
		if done.Add(1) == 4 {
			cancel()
		}
		time.Sleep(time.Millisecond)
		if ctx.Err() != nil {
			return cancelled()
		}
		return passed()
	}}

	ids := make([]string, 64)
	for i := range ids {
		ids[i] = "m" + string(rune('a'+i%26)) + string(rune('0'+i/26))
	}
	if _, err := execute.Schedule(ctx, options(f, 8),
		mutants(mutantTimeout, ids...), testBins("example.com/a"), execute.Hooks{}); err == nil {
		t.Fatal("scheduling a cancelled run reported success")
	}

	// Polled rather than sampled once: the goroutines this test is about are
	// already joined when Schedule returns, but the test binary's own runtime
	// has transients that would make a single sample flaky.
	for range 100 {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("%d goroutines are running, want no more than the %d there were before",
		runtime.NumGoroutine(), before)
}

// TestScheduleRefusesToMeasureAgainstNoBinaries is the flattering-green
// refusal. Every mutant would otherwise be reported as survived by a run that
// executed nothing at all.
func TestScheduleRefusesToMeasureAgainstNoBinaries(t *testing.T) {
	f := &fake{}
	results, err := execute.Schedule(t.Context(), options(f, 2),
		mutants(mutantTimeout, "a", "b"), nil, execute.Hooks{})

	if code := execute.CodeOf(err); code != execute.CodeNoTestBinaries {
		t.Errorf("code = %q, want %q (%v)", code, execute.CodeNoTestBinaries, err)
	}
	for _, r := range results {
		if r.Final != mutation.OutcomeNotRun {
			t.Errorf("%s = %s, want %s", r.ID, r.Final, mutation.OutcomeNotRun)
		}
	}
	if got := len(f.seen()); got != 0 {
		t.Errorf("started %d processes, want none", got)
	}
}

// TestScheduleGivesEachWorkerItsOwnTemporaryDirectory proves the isolation the
// package documentation promises. Two mutants running at once that shared a
// temporary directory could overwrite each other's files, and the resulting
// failure would be indistinguishable from a detection.
//
// The workers are held at a rendezvous so that all of them really are in flight
// at once; without it one worker could take the whole queue and the assertion
// would be about nothing.
func TestScheduleGivesEachWorkerItsOwnTemporaryDirectory(t *testing.T) {
	const jobs = 3
	scratch := t.TempDir()

	arrived := make(chan struct{}, jobs)
	release := make(chan struct{})
	f := &fake{respond: func(context.Context, call) runner.Result {
		arrived <- struct{}{}
		<-release
		return passed()
	}}
	opts := execute.WithRunner(execute.Options{Jobs: jobs, ScratchDir: scratch}, f.run)

	type outcome struct {
		results []execute.MutantResult
		err     error
	}
	finished := make(chan outcome, 1)
	go func() {
		results, err := execute.Schedule(t.Context(), opts,
			mutants(mutantTimeout, "a", "b", "c"), testBins("example.com/x"), execute.Hooks{})
		finished <- outcome{results, err}
	}()

	for i := range jobs {
		select {
		case <-arrived:
		case <-time.After(30 * time.Second):
			close(release)
			t.Fatalf("only %d of %d workers started; the pool is not running them concurrently", i, jobs)
		}
	}
	close(release)

	got := <-finished
	if got.err != nil {
		t.Fatalf("scheduling: %v", got.err)
	}

	dirs := map[string]bool{}
	for _, c := range f.seen() {
		dirs[envValue(c.Env, "TMPDIR")] = true
	}
	if len(dirs) != jobs {
		t.Errorf("the %d workers used %d temporary directories, want one each: %v", jobs, len(dirs), dirs)
	}
	for dir := range dirs {
		if filepath.Dir(dir) != scratch {
			t.Errorf("temporary directory %q is not under the run's scratch directory %q", dir, scratch)
		}
		if ok, err := statDir(dir); err != nil || !ok {
			t.Errorf("temporary directory %q was not created: %v", dir, err)
		}
	}
}

// TestScheduleResolvesARelativeScratchParent carries the resolution rule
// through the scheduler, which is where it matters in a real run: every worker
// derives its own directory from the parent, so a parent that stayed relative
// would give every worker at once a temporary directory that means one place to
// go-mutants and another to the child running inside the snapshot.
func TestScheduleResolvesARelativeScratchParent(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	opts := execute.WithRunner(execute.Options{Jobs: 2, ScratchDir: filepath.Join("run", "tmp")}, f.run)

	if _, err := execute.Schedule(t.Context(), opts,
		mutants(mutantTimeout, "a", "b", "c"), testBins("example.com/x"), execute.Hooks{}); err != nil {
		t.Fatalf("scheduling: %v", err)
	}

	seen := f.seen()
	if len(seen) != 3 {
		t.Fatalf("started %d children, want 3", len(seen))
	}
	for _, c := range seen {
		dir := envValue(c.Env, "TMPDIR")
		if !filepath.IsAbs(dir) {
			t.Errorf("a worker's TMPDIR = %q, want an absolute path", dir)
		}
		if ok, err := statDir(dir); err != nil || !ok {
			t.Errorf("the worker temporary directory %q was not created: %v", dir, err)
		}
	}
}

// TestScheduleWithoutMutantsIsNotAnError covers the empty queue, which a
// `--changed` or `--shard` selection can legitimately produce.
func TestScheduleWithoutMutantsIsNotAnError(t *testing.T) {
	f := &fake{}
	results, err := execute.Schedule(t.Context(), options(f, 4), nil,
		testBins("example.com/a"), execute.Hooks{})
	if err != nil {
		t.Fatalf("scheduling an empty queue: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results for no mutants", len(results))
	}
}

// TestWorkerScratchDirNamesOneDirectoryPerWorker pins the naming, including the
// empty parent that means "leave the inherited temporary directory alone".
func TestWorkerScratchDirNamesOneDirectoryPerWorker(t *testing.T) {
	if got := execute.WorkerScratchDir("", 3); got != "" {
		t.Errorf("with no scratch parent = %q, want empty", got)
	}
	first := execute.WorkerScratchDir(filepath.Join("run", "tmp"), 0)
	second := execute.WorkerScratchDir(filepath.Join("run", "tmp"), 1)
	if first == second {
		t.Errorf("workers 0 and 1 share %q", first)
	}
	if want := filepath.Join("run", "tmp", "w0"); first != want {
		t.Errorf("worker 0 = %q, want %q", first, want)
	}
}

// mapsEqual compares two counting maps.
func mapsEqual(got, want map[string]int) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}

// TestScheduleInterruptionNamesTheBinaryThatWasCutOff carries the one thing a
// reader of a Ctrl-C is looking for all the way out to the error they see.
//
// The attempt that was cut off knows which binary it was in, and until now that
// was where the knowledge stopped: a report keeps the *number* of attempts and
// not the attempts, so nothing downstream could reach it, and the error this
// function returns — the one internal/cli prints — said only that the phase was
// interrupted. The verdict is untouched by this: a mutant nobody measured is
// still not-run and still carries no error of its own.
func TestScheduleInterruptionNamesTheBinaryThatWasCutOff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := &fake{respond: func(context.Context, call) runner.Result {
		cancel()
		// The partial output of a child the supervisor killed mid-test: how
		// far the suite got before the signal arrived.
		return runner.Result{
			ExitCode: runner.ExitCodeUnavailable,
			Duration: time.Millisecond,
			Output:   []byte("=== RUN   TestSlow\n"),
		}
	}}
	bins := testBins("example.com/a")

	results, err := execute.Schedule(ctx, options(f, 1),
		mutants(mutantTimeout, "slow"), bins, execute.Hooks{})

	if code := execute.CodeOf(err); code != execute.CodeInterrupted {
		t.Fatalf("code = %q, want %q (%v)", code, execute.CodeInterrupted, err)
	}
	// The message is the stable contract and does not change because the error
	// grew a command.
	if want := "GOM7520: the execution phase was interrupted"; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("Error() = %q, want it to begin %q", err.Error(), want)
	}
	if !isCancellation(err) {
		t.Errorf("the cancellation is not reachable through %v", err)
	}

	var failure *execute.Error
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v, want an *execute.Error", err)
	}
	command := failure.Command()
	if command == nil {
		t.Fatal("Command() = nil, want the binary that was still running")
	}
	started := f.seen()
	if len(started) != 1 {
		t.Fatalf("the fake saw %d calls, want 1", len(started))
	}
	if !slices.Equal(command.Argv, started[0].Argv) || command.Dir != bins[0].Dir {
		t.Errorf("Command() = %+v, want the argv and directory of %q in %q",
			command, started[0].Argv, bins[0].Dir)
	}
	if got := failure.RetainedOutput(); !strings.Contains(got, "TestSlow") {
		t.Errorf("RetainedOutput() = %q, want what the killed child had printed", got)
	}

	// And the verdict is exactly what it was: not run, with no error of its
	// own. Naming the command is a diagnostic about the run, not a statement
	// that this mutant errored.
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Final != mutation.OutcomeNotRun {
		t.Errorf("final outcome = %s, want %s", results[0].Final, mutation.OutcomeNotRun)
	}
	if results[0].Err != nil {
		t.Errorf("the verdict carries %v, want none", results[0].Err)
	}
}

// TestScheduleRecordsOneMutantExecPerAttemptWithWorkerAndBinaries is the claim
// the whole recording of this phase rests on: one event per *attempt*, not per
// mutant.
//
// A mutant that timed out and was retried is two measurements with two
// durations, and collapsing them into the one verdict they produced is exactly
// what makes a flaky suite unreadable after the fact. Each event names the
// worker that ran it — which is what a reader with an overloaded machine is
// looking for — the binaries the attempt tried, and the executions underneath
// it, so the account of an attempt and the commands it issued are one thing.
func TestScheduleRecordsOneMutantExecPerAttemptWithWorkerAndBinaries(t *testing.T) {
	t.Parallel()

	attempts := newAttemptCounter()
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		id := activeOf(c)
		if id == "slow" && attempts.next(id) == 1 {
			return timedOut()
		}
		if id == "slow" {
			return failed("--- FAIL: TestSlow\n")
		}
		return passed()
	}}
	opts, sink := traced(t, f, options(f, 3))
	// The scope the *binaries* were built from is deliberately several
	// patterns, so that a `package` taken from it rather than from the mutant
	// would be visibly wrong.
	opts.Packages = []string{"./internal/...", "./cmd/..."}

	queue := mutants(mutantTimeout, "quick", "slow", "steady")
	queue[0].Package = "example.com/m/a"
	queue[1].Package = "example.com/m/b"
	// The third names none, which a caller that has no package for a mutant
	// says by leaving it empty.

	// The worker a mutant ran on is not this test's to predict, so it is taken
	// from the hook that announces it: the event and the hook are two accounts
	// of one attempt and have to agree.
	var mu sync.Mutex
	started := map[string][]int{}
	hooks := execute.Hooks{Started: func(id string, worker int) {
		mu.Lock()
		defer mu.Unlock()
		started[id] = append(started[id], worker)
	}}

	results, err := execute.Schedule(t.Context(), opts, queue, testBins("example.com/a"), hooks)
	if err != nil {
		t.Fatalf("scheduling: %v", err)
	}
	if got := results[1].Final; got != mutation.OutcomeInconclusive {
		t.Fatalf("the retried mutant settled as %s, want %s", got, mutation.OutcomeInconclusive)
	}

	events := eventsOf(sink, trace.TypeMutantExec)
	if len(events) != 4 {
		t.Fatalf("the recording holds %d mutant-exec events, want one per attempt (3 mutants, one retried)",
			len(events))
	}

	// The mutant's own package, verbatim, and nothing at all for the mutant
	// that has none: `package` is a join key a consumer reads as an import
	// path, and the test scope a run happens to have been given is not one.
	packages := map[string]string{}
	for _, event := range events {
		packages[event.Mutant.ID] = event.Mutant.Package
	}
	want := map[string]string{"quick": "example.com/m/a", "slow": "example.com/m/b", "steady": ""}
	if !maps.Equal(packages, want) {
		t.Errorf("the attempts are about %v, want the mutants' own packages %v", packages, want)
	}

	byID := map[string][]*trace.MutantRecord{}
	for _, event := range events {
		if event.Mutant == nil {
			t.Fatalf("a mutant-exec event carries no mutant payload: %+v", event)
		}
		byID[event.Mutant.ID] = append(byID[event.Mutant.ID], event.Mutant)
	}
	for id, records := range byID {
		for i, record := range records {
			if record.Attempt != i+1 {
				t.Errorf("%s attempt %d is numbered %d, want %d", id, i+1, record.Attempt, i+1)
			}
			if want := started[id][i]; record.Worker != want {
				t.Errorf("%s attempt %d says worker %d, want the %d the hook announced",
					id, i+1, record.Worker, want)
			}
			if want := []string{"example.com/a"}; !slices.Equal(record.Binaries, want) {
				t.Errorf("%s attempt %d ran %q, want %q", id, i+1, record.Binaries, want)
			}
			if len(record.ExecSeqs) != 1 {
				t.Errorf("%s attempt %d points at %v, want the one execution it made", id, i+1, record.ExecSeqs)
			}
			if record.TimeoutMS != mutantTimeout.Milliseconds() {
				t.Errorf("%s attempt %d was given %d ms, want %d", id, i+1, record.TimeoutMS,
					mutantTimeout.Milliseconds())
			}
		}
	}

	slow := byID["slow"]
	if len(slow) != 2 {
		t.Fatalf("the retried mutant has %d events, want both attempts", len(slow))
	}
	if slow[0].Outcome != trace.OutcomeTimedOut || slow[1].Outcome != trace.OutcomeKilled {
		t.Errorf("the retried mutant reads %q then %q, want %q then %q",
			slow[0].Outcome, slow[1].Outcome, trace.OutcomeTimedOut, trace.OutcomeKilled)
	}
	for i, record := range slow {
		if want := "example.com/a"; record.KilledBy != want {
			t.Errorf("attempt %d says killed_by %q, want %q", i+1, record.KilledBy, want)
		}
	}

	// Every sequence an attempt points at is an execution the recording holds,
	// and every execution belongs to exactly one attempt.
	var pointed []int64
	for _, event := range events {
		pointed = append(pointed, event.Mutant.ExecSeqs...)
	}
	slices.Sort(pointed)
	if want := execSeqs(sink); !slices.Equal(pointed, want) {
		t.Errorf("the attempts point at %v, want the recording's own executions %v", pointed, want)
	}
}

// TestScheduleRecordsTheRetryPassAsAStage times the serial pass rather than
// leaving it as a gap in the recording.
//
// The retry pass is the one part of an execution phase that is deliberately not
// parallel, so on a queue with many timeouts it is where a run's wall-clock time
// disappears — and to a reader of a recording that has no stage it looks like
// the phase simply took longer for no reason. The detail says how much work the
// pass was given, which is the number that explains the duration.
func TestScheduleRecordsTheRetryPassAsAStage(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return timedOut() }}
	opts, sink := traced(t, f, options(f, 2))

	if _, err := execute.Schedule(t.Context(), opts,
		mutants(mutantTimeout, "slow", "slower"), testBins("example.com/a"), execute.Hooks{}); err != nil {
		t.Fatalf("scheduling: %v", err)
	}

	stages := eventsOf(sink, trace.TypeStage)
	if len(stages) != 2 {
		t.Fatalf("the recording holds %d stage events, want the retry pass's started/finished pair", len(stages))
	}
	for i, want := range []string{trace.StateStarted, trace.StateFinished} {
		if stages[i].Stage.Name != "retry" {
			t.Errorf("stage %d is named %q, want %q", i, stages[i].Stage.Name, "retry")
		}
		if stages[i].Stage.State != want {
			t.Errorf("stage %d is %q, want %q", i, stages[i].Stage.State, want)
		}
	}
	if got := stages[0].Stage.Detail; !strings.Contains(got, "2 timeouts") {
		t.Errorf("the retry pass started with detail %q, want the number of timeouts it was given", got)
	}
	if got := stages[1].Stage.Result; got != trace.ResultSucceeded {
		t.Errorf("the retry pass finished as %q, want %q", got, trace.ResultSucceeded)
	}

	// The retried attempts are inside the pair, which is what makes the stage a
	// span rather than a label.
	for _, event := range eventsOf(sink, trace.TypeMutantExec) {
		if event.Mutant.Attempt != 2 {
			continue
		}
		if event.Seq < stages[0].Seq || event.Seq > stages[1].Seq {
			t.Errorf("a retried attempt was recorded at %d, outside the retry stage %d..%d",
				event.Seq, stages[0].Seq, stages[1].Seq)
		}
	}
}

// TestScheduleRecordsARetryPassACancellationSkippedAsFailed is the other half
// of the stage's result.
//
// A retry pass that was cut off left the very evidence it existed to gather
// ungathered — the mutants it did not reach stay not-run — so reporting it as
// succeeded would make a run stopped halfway read like a run that finished. The
// stage is still a pair, because a step that started and was abandoned is
// exactly what a reader needs to see.
func TestScheduleRecordsARetryPassACancellationSkippedAsFailed(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	f := &fake{respond: func(context.Context, call) runner.Result {
		// Timed out and, by the time the retry pass begins, cancelled.
		cancel()
		return timedOut()
	}}
	opts, sink := traced(t, f, options(f, 1))

	results, err := execute.Schedule(ctx, opts,
		mutants(mutantTimeout, "slow"), testBins("example.com/a"), execute.Hooks{})
	if got := execute.CodeOf(err); got != execute.CodeInterrupted {
		t.Fatalf("Schedule failed with %q, want %q: %v", got, execute.CodeInterrupted, err)
	}
	if got := results[0].Final; got != mutation.OutcomeNotRun {
		t.Fatalf("the unretried mutant settled as %s, want %s", got, mutation.OutcomeNotRun)
	}

	stages := eventsOf(sink, trace.TypeStage)
	if len(stages) != 2 {
		t.Fatalf("the recording holds %d stage events, want the retry pass's started/finished pair", len(stages))
	}
	if got := stages[1].Stage.Result; got != trace.ResultFailed {
		t.Errorf("the retry pass finished as %q, want %q: it never retried anything", got, trace.ResultFailed)
	}
}

// TestScheduleRecordsARetryPassACancellationCutOffAsFailed is the same claim
// about the retry that *did* start.
//
// A retry the signal killed mid-suite is not visible in the pass's own control
// flow: the loop asked the context before starting it and got no cancellation,
// and [RunOne] comes back with the not-run outcome rather than with an error.
// The mutant is left exactly as unretried as one the pass never reached — its
// timeout was never reproduced and its verdict is not-run — so the stage has to
// close the same way, or a run stopped during its last retry would read as a
// pass that finished.
func TestScheduleRecordsARetryPassACancellationCutOffAsFailed(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	attempts := newAttemptCounter()
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if attempts.next(activeOf(c)) == 1 {
			return timedOut()
		}
		// The retry started, and the signal arrived while it was running.
		cancel()
		return cancelled()
	}}
	opts, sink := traced(t, f, options(f, 1))

	results, err := execute.Schedule(ctx, opts,
		mutants(mutantTimeout, "slow"), testBins("example.com/a"), execute.Hooks{})
	if got := execute.CodeOf(err); got != execute.CodeInterrupted {
		t.Fatalf("Schedule failed with %q, want %q: %v", got, execute.CodeInterrupted, err)
	}
	if got := len(results[0].Attempts); got != 2 {
		t.Fatalf("the mutant kept %d attempts, want the timeout and the retry that was cut off", got)
	}
	if got := results[0].Final; got != mutation.OutcomeNotRun {
		t.Fatalf("the cut-off mutant settled as %s, want %s", got, mutation.OutcomeNotRun)
	}

	stages := eventsOf(sink, trace.TypeStage)
	if len(stages) != 2 {
		t.Fatalf("the recording holds %d stage events, want the retry pass's started/finished pair", len(stages))
	}
	if got := stages[1].Stage.Result; got != trace.ResultFailed {
		t.Errorf("the retry pass finished as %q, want %q: the mutant it started is still unretried",
			got, trace.ResultFailed)
	}
}

// TestScheduleRecordsNoRetryStageWhenNothingTimedOut keeps the stage a
// statement about work that happened. A run in which nothing timed out has no
// serial pass, and a zero-length "retry" in every recording would be a line
// every reader learns to skip.
func TestScheduleRecordsNoRetryStageWhenNothingTimedOut(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	opts, sink := traced(t, f, options(f, 2))

	if _, err := execute.Schedule(t.Context(), opts,
		mutants(mutantTimeout, "a", "b"), testBins("example.com/a"), execute.Hooks{}); err != nil {
		t.Fatalf("scheduling: %v", err)
	}
	if stages := eventsOf(sink, trace.TypeStage); len(stages) != 0 {
		t.Errorf("the recording holds %d stage events, want none: nothing was retried", len(stages))
	}
}

// TestScheduleWithoutARecorderIsUnchanged is the promise the whole feature is
// worth nothing without: a recording is an account of a run and never a
// participant in it.
//
// The one difference between the two runs is the sequences an attempt points
// at, because an untraced run has no recording to point into. Everything a
// verdict is computed from — the outcomes, the binaries, the durations, the
// attempts kept — is identical, and this compares the whole of it rather than
// the fields somebody thought to check.
func TestScheduleWithoutARecorderIsUnchanged(t *testing.T) {
	t.Parallel()

	respond := func(_ context.Context, c call) runner.Result {
		switch activeOf(c) {
		case "killed":
			return failed("--- FAIL: TestX\n")
		case "slow":
			return timedOut()
		default:
			return passed()
		}
	}
	queue := mutants(mutantTimeout, "killed", "survived", "slow")
	bins := testBins("example.com/a", "example.com/b")

	plain := &fake{respond: respond}
	untraced, err := execute.Schedule(t.Context(), options(plain, 2), queue, bins, execute.Hooks{})
	if err != nil {
		t.Fatalf("scheduling without a recorder: %v", err)
	}

	spy := &fake{respond: respond}
	opts, sink := traced(t, spy, options(spy, 2))
	recorded, err := execute.Schedule(t.Context(), opts, queue, bins, execute.Hooks{})
	if err != nil {
		t.Fatalf("scheduling with a recorder: %v", err)
	}
	if len(eventsOf(sink, trace.TypeMutantExec)) == 0 {
		t.Fatal("the traced run recorded no attempts, so this compares two untraced runs")
	}

	for i := range recorded {
		for j := range recorded[i].Attempts {
			if len(untraced[i].Attempts[j].ExecSeqs) != 0 {
				t.Errorf("the untraced %s attempt %d points at %v, want nothing: there is no recording",
					untraced[i].ID, j+1, untraced[i].Attempts[j].ExecSeqs)
			}
			recorded[i].Attempts[j].ExecSeqs = nil
		}
	}
	if !reflect.DeepEqual(recorded, untraced) {
		t.Errorf("the traced run produced\n%+v\nand the untraced one\n%+v", recorded, untraced)
	}
}
