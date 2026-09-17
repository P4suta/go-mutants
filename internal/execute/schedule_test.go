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

type attemptCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func newAttemptCounter() *attemptCounter { return &attemptCounter{n: map[string]int{}} }

func (a *attemptCounter) next(id string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n[id]++
	return a.n[id]
}

func activeOf(c call) string { return c.active() }

func TestScheduleReturnsResultsInTheInputOrder(t *testing.T) {
	ids := make([]string, 20)
	for i := range ids {
		ids[i] = "mutant-" + string(rune('a'+i))
	}
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
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

func TestScheduleLeavesAnUnconfirmedTimeoutNotRun(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := &fake{respond: func(context.Context, call) runner.Result {
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

func TestScheduleLetsAFailedRetryStandInsteadOfPromotingTheTimeout(t *testing.T) {
	cases := []struct {
		name           string
		retry          func(cancel context.CancelFunc) runner.Result
		wantFinal      mutation.Outcome
		wantMutantCode execute.Code
		wantRunCode    execute.Code
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
			if got.KilledBy != "" {
				t.Errorf("credited %q with a detection, want none", got.KilledBy)
			}
			if len(got.Attempts) != 2 {
				t.Fatalf("kept %d attempts, want both", len(got.Attempts))
			}
			if got.Attempts[0].Outcome != mutation.OutcomeTimedOut {
				t.Errorf("first attempt = %s, want the timeout that was retried", got.Attempts[0].Outcome)
			}
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

func TestScheduleReturnsNotRunAfterCancellation(t *testing.T) {
	for _, jobs := range []int{1, 4} {
		t.Run("with "+countNoun(jobs, "worker"), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			var done atomic.Int64
			f := &fake{respond: func(ctx context.Context, _ call) runner.Result {
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
			if got := len(f.seen()); got >= len(ids) {
				t.Errorf("started %d children for %d mutants; a cancelled run must stop taking work",
					got, len(ids))
			}
		})
	}
}

func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

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

	for range 100 {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("%d goroutines are running, want no more than the %d there were before",
		runtime.NumGoroutine(), before)
}

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

func TestScheduleInterruptionNamesTheBinaryThatWasCutOff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := &fake{respond: func(context.Context, call) runner.Result {
		cancel()
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
	opts.Packages = []string{"./internal/...", "./cmd/..."}

	queue := mutants(mutantTimeout, "quick", "slow", "steady")
	queue[0].Package = "example.com/m/a"
	queue[1].Package = "example.com/m/b"

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

	var pointed []int64
	for _, event := range events {
		pointed = append(pointed, event.Mutant.ExecSeqs...)
	}
	slices.Sort(pointed)
	if want := execSeqs(sink); !slices.Equal(pointed, want) {
		t.Errorf("the attempts point at %v, want the recording's own executions %v", pointed, want)
	}
}

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

func TestScheduleRecordsARetryPassACancellationSkippedAsFailed(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	f := &fake{respond: func(context.Context, call) runner.Result {
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

func TestScheduleRecordsARetryPassACancellationCutOffAsFailed(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	attempts := newAttemptCounter()
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if attempts.next(activeOf(c)) == 1 {
			return timedOut()
		}
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
			recorded[i].Attempts[j].Worker = 0
			untraced[i].Attempts[j].Worker = 0
		}
	}
	if !reflect.DeepEqual(recorded, untraced) {
		t.Errorf("the traced run produced\n%+v\nand the untraced one\n%+v", recorded, untraced)
	}
}

func TestAProvedRunawayIsNotMeasuredTwice(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name     string
		proved   bool
		outcome  mutation.Outcome
		attempts int
		final    mutation.Outcome
	}{
		{
			name: "a proved mutant that times out", proved: true,
			outcome: mutation.OutcomeTimedOut, attempts: 1, final: mutation.OutcomeTimedOut,
		},
		{
			name:    "an unproved mutant that times out",
			outcome: mutation.OutcomeTimedOut, attempts: 2, final: mutation.OutcomeTimedOut,
		},
		{
			name: "a proved mutant that is killed", proved: true,
			outcome: mutation.OutcomeKilled, attempts: 1, final: mutation.OutcomeKilled,
		},
		{
			name: "a proved mutant that survives", proved: true,
			outcome: mutation.OutcomeSurvived, attempts: 1, final: mutation.OutcomeSurvived,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			var started atomic.Int64
			f := &fake{respond: func(context.Context, call) runner.Result {
				started.Add(1)
				if c.outcome == mutation.OutcomeTimedOut {
					return runner.Result{TimedOut: true, ExitCode: runner.ExitCodeUnavailable}
				}
				if c.outcome == mutation.OutcomeKilled {
					return failed("--- FAIL: TestX\n")
				}
				return passed()
			}}
			opts := options(f, 2)
			runs := []execute.MutantRun{{
				ID:           "abc",
				Timeout:      time.Minute,
				NeverReturns: c.proved,
			}}

			results, err := execute.Schedule(t.Context(), opts, runs, testBins("example.com/m/a"), execute.Hooks{})
			if err != nil {
				t.Fatalf("Schedule: %v", err)
			}
			if len(results) != 1 {
				t.Fatalf("Schedule returned %d results, want 1", len(results))
			}
			if results[0].Final != c.final {
				t.Errorf("the verdict is %s, want %s", results[0].Final, c.final)
			}
			if got := int(started.Load()); got != c.attempts {
				t.Errorf("the mutant was measured %d times, want %d", got, c.attempts)
			}
		})
	}
}
