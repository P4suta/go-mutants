// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/testkit"
)

// The two variables the scripted `go` protocol owns.
//
// Neither begins with GO_MUTANTS_, and that is the whole reason the prefix was
// chosen: internal/execute and internal/engine strip that namespace from every
// child they start — which is how a developer's exported GO_MUTANTS_ACTIVE is
// kept from running a mutant as the baseline — so a fake whose switch wore it
// would be stripped out of the very environments it exists to observe, and the
// test binary would run its own suite in place of the go command.
const (
	// FakeGoRulesEnv names the file holding the rule table, and is what turns
	// this test binary into the `go` command. Its presence is the switch; its
	// value is where the answers are.
	FakeGoRulesEnv = "TESTKIT_FAKE_GO_RULES"
	// FakeGoCallsEnv names the append-only log each call is recorded in.
	FakeGoCallsEnv = "TESTKIT_FAKE_GO_CALLS"
)

// FakeGoNoRule is the status the fake exits with when nothing else can be
// true: a call no rule matched, a rule table it could not read, an `-o` path a
// rule promised to create and the argv did not carry.
//
// It is [testkit.HelperMisuse], which is a status no test in this repository
// asks a child to produce, so a fake that was misconfigured can never be read as
// the exit code a test was expecting.
const FakeGoNoRule = testkit.HelperMisuse

// fakeGoName is what the scripted toolchain is installed as. os/exec resolves a
// bare `go` through PATH by this name, and PATHEXT wants the suffix on Windows.
func fakeGoName() string {
	if runtime.GOOS == "windows" {
		return "go.exe"
	}
	return "go"
}

// recordedEnvValues are the variables whose *values* a call log keeps.
//
// Every other variable is recorded by name alone, and the difference is a
// promise rather than an economy. A call log is written into a test's scratch
// directory and, under the keep policy, uploaded as a CI artifact — so it must
// never become the place a developer's tokens, a runner's credentials or a
// private module's password are written down.
//
// These are the ones go-mutants itself is supposed to compose, which is what
// gives a test a claim to make about them, and every one of them is a flag list
// or a filesystem path rather than a secret: the flags a compile carries, the
// workspace and the toolchain policy it pinned, the caches and directories it
// pointed the go command at, the PATH it put the located toolchain in front of
// — which internal/execute does on purpose, so that a `go` cannot hand work to
// a different `go` it finds ahead of itself — and the two activation variables
// whose presence or absence is the difference between a baseline and a mutant.
var recordedEnvValues = []string{
	"GOFLAGS",
	"GOWORK",
	"GOCACHE",
	"GOTOOLCHAIN",
	"GOENV",
	"GOMODCACHE",
	"PATH",
	instrument.ActiveEnv,
	instrument.ProbeEnv,
}

// A Call is one invocation the fake answered, as the call log kept it.
//
// Argv is what followed the binary name, which is the vector a call site
// composed and the only part of it that is not this package's own doing. Dir is
// where the child ran, because a `go list` issued from the wrong directory
// resolves a different module and that is a defect a test should be able to
// name. EnvNames is every variable the child could see and Env is the value of
// the few named in [recordedEnvValues].
type Call struct {
	Argv     []string          `json:"argv"`
	Dir      string            `json:"dir"`
	EnvNames []string          `json:"env_names"`
	Env      map[string]string `json:"env"`
}

// String renders a call the way somebody would type it, so that a failure
// naming a call is one line rather than the whole environment.
//
// The environment is deliberately left out. A call carries the name of every
// variable the child could see — a hundred and fifty of them on an ordinary
// developer's machine — and a failure that printed them would bury the argv it
// was about.
func (c Call) String() string {
	if c.Dir == "" {
		return "`go " + strings.Join(c.Argv, " ") + "`"
	}
	return "`go " + strings.Join(c.Argv, " ") + "` (in " + c.Dir + ")"
}

// A ListPackage is one record `go list -json` prints.
//
// The fields are the ones internal/execute's decoder reads — where to build a
// test binary from, what to call it, and whether the package has any test files
// at all — spelled the way the go command spells them, so that a scripted
// listing and a real one are read by the same code.
type ListPackage struct {
	ImportPath   string   `json:"ImportPath"`
	Dir          string   `json:"Dir"`
	TestGoFiles  []string `json:"TestGoFiles,omitempty"`
	XTestGoFiles []string `json:"XTestGoFiles,omitempty"`
}

// fakeRule is one row of the table, in the encoding the child reads.
//
// It is a separate type from [FakeRule] because the two have different jobs:
// this one crosses a process boundary as JSON and has to stay a plain record,
// while the other is the builder a test writes rules with.
type fakeRule struct {
	Argv         []string `json:"argv"`
	Stdout       string   `json:"stdout,omitempty"`
	Stderr       string   `json:"stderr,omitempty"`
	Exit         int      `json:"exit,omitempty"`
	SleepMS      int64    `json:"sleep_ms,omitempty"`
	CreateOutput bool     `json:"create_output,omitempty"`
}

// A Fake is a scripted stand-in for the `go` command.
//
// See [FakeGo] for what it is for and how a test reaches it.
type Fake struct {
	t     testing.TB
	bin   string
	rules string
	calls string

	mu    sync.Mutex
	table []*fakeRule
}

// A FakeRule is one scripted answer, and the builder that fills it in.
//
// Every setter writes the whole table back to disk before it returns, so a rule
// may be amended at any point up to the call it answers — including after the
// binary's path has already been handed to the code under test. The write and
// the change to the rule happen under one lock, so a table published while
// another goroutine was amending it is never a table with half an amendment in
// it.
type FakeRule struct {
	f    *Fake
	rule *fakeRule
}

// FakeGo returns a scripted stand-in for the `go` command.
//
// # What it is for
//
// Every question of the form "what happens when `go` misbehaves" used to need a
// real toolchain: a version probe that hangs, one that answers garbage, a
// compile that fails with diagnostics, a `go list` that cannot resolve a
// pattern. Those are the failures the layers above `go` exist to report, and
// they were either tested in the integration tier — minutes, and a toolchain —
// or not tested at all, because there is no way to install a `go` that hangs.
//
// A scripted one costs a process and answers from a table, so the tests become
// unit-tier tests that run everywhere, and the unit tier gains something it
// never had: the argv and the environment a call site really composed, read off
// a log rather than off an injected function.
//
// # How the test reaches it
//
// [Fake.Bin] is an executable named `go` (`go.exe` on Windows), and there are
// two ways to put it where the code under test will find it. Both are used in
// this repository:
//
//   - By path, for a call site that takes one: gocmd.Options.Explicit,
//     execute.Options.Toolchain, gomutants.OpenOptions.GoBinary. This is the
//     form to prefer — it names the binary under test, and it leaves the test
//     free to run in parallel.
//   - On PATH, for a call site that locates the toolchain itself: internal/cli's
//     `doctor` and internal/engine's run pipeline both call gocmd.Locate with no
//     explicit path and no environment, so the only way in is the process's own.
//     [Fake.Export] does that with t.Setenv, and a test that calls it may not
//     call t.Parallel.
//
// Either way the child needs the two control variables, [FakeGoRulesEnv] and
// [FakeGoCallsEnv]: [Fake.Env] adds them to an environment a test composed, and
// [Fake.Export] publishes them into the process so that a child which inherits
// finds them too.
//
// # How it is scripted
//
// [Fake.On] matches a call by argv prefix and the longest match wins, so a
// general rule and the one call that has to behave differently can both be
// scripted:
//
//	f := mutantkit.FakeGo(t)
//	f.Version("1.99.0")                       // `go version`
//	f.On("list").Stdout(listing)              // every `go list`
//	f.On("list", "-e").Exit(1).Stderr(oops)   // except the scope resolution
//
// A call no rule matches is refused with [FakeGoNoRule] and a message naming
// the argv. That is the one behaviour of the fake that is not configurable: a
// stand-in that answered an unscripted command with a silent success would let
// a test pass on a command its author never considered, which is the failure
// shape a fake must never have.
//
// # What it needs from the package
//
// The fake is this very test binary re-executed, so the package's TestMain has
// to dispatch: see [Main]. Without it the child runs the whole suite in place of
// the go command, and [Main] refuses a binary started as `go` with the switch
// unset rather than letting that happen quietly.
func FakeGo(t testing.TB) *Fake {
	t.Helper()
	bin, err := sharedFakeGo()
	if err != nil {
		t.Fatalf("installing the fake `go` for this test binary: %v", err)
		return nil
	}
	dir := testkit.Scratch(t)
	f := &Fake{
		t:     t,
		bin:   bin,
		rules: filepath.Join(dir, "fake-go-rules.json"),
		calls: filepath.Join(dir, "fake-go-calls.jsonl"),
	}
	f.mu.Lock()
	f.saveLocked()
	f.mu.Unlock()
	// Created empty, so that [Fake.Calls] on a fake nothing ran is an empty log
	// rather than a missing file — the difference between "it was not called"
	// and "something went wrong" is exactly what such a test is asking about.
	if err := os.WriteFile(f.calls, nil, 0o600); err != nil {
		t.Fatalf("creating the fake `go` call log %s: %v", f.calls, err)
	}
	logInputs(t, "fake-go="+f.bin, "rules="+f.rules, "calls="+f.calls)
	return f
}

// fakeGoInstalls counts the links and copies of this test binary the package
// has made. [FakeGoInstalls] reads it; see [sharedFakeGo] for why it is one.
var fakeGoInstalls atomic.Int64

// fakeGoTempRoot is the operating system's temporary directory as the process
// started with it.
//
// It is captured here rather than asked for at the point of use, and the
// difference is a directory that disappears. [testkit.Env] redirects TMPDIR,
// TMP and TEMP at the test's own scratch, so an os.TempDir() called from inside
// such a test would put the shared install in a directory the testing package
// removes when that test ends — and every later test in the binary would find
// its toolchain gone.
var fakeGoTempRoot = os.TempDir()

// sharedFakeGoDir is the directory [sharedFakeGo] made, for [Main] to remove.
var sharedFakeGoDir atomic.Pointer[string]

// sharedFakeGo installs the scripted `go` once for the whole test binary.
//
// Once, rather than once per [Fake], because the file is this test binary: six
// megabytes of it, and twenty-eight fakes in one unit-tier run of this
// repository. A hard link makes that free on a machine with one filesystem and
// a Windows runner is not one — GitHub's puts RUNNER_TEMP on D: and the build
// cache on C:, so os.Link cannot answer and every install is a copy — which is
// a hundred and seventy megabytes of copying per run for a program whose entire
// behaviour comes from two environment variables. The same file answers every
// fake, because the rule table and the call log are named by the environment
// rather than by the binary.
//
// It is out of the tests' scratch directories for a second reason. Under the
// keep policy a failing test's scratch is what CI uploads, and a six-megabyte
// test binary in it is six megabytes of artifact saying nothing: what a reader
// needs is the rule table and the call log, which are two small text files and
// stay where they were.
//
// The directory is the process's own and [Main] removes it when the suite ends.
var sharedFakeGo = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp(fakeGoTempRoot, "go-mutants-fake-go-")
	if err != nil {
		return "", err
	}
	sharedFakeGoDir.Store(&dir)
	return installFakeGo(dir)
})

// removeSharedFakeGo takes the process's shared install away, if it made one.
//
// The removal retries, which is the Windows rule and the same one
// internal/testkit applies to a scratch directory: a child the supervisor has
// just killed can still hold its own image open for a moment, and the operating
// system refuses to unlink a file that is mapped. It is reported on standard
// error rather than failed — the suite has already finished, there is no test
// left to fail, and a temporary directory nobody removed is a smaller problem
// than a green run reported as red.
func removeSharedFakeGo() {
	dir := sharedFakeGoDir.Swap(nil)
	if dir == nil {
		return
	}
	var err error
	for attempt := range fakeGoRemovalAttempts {
		if err = os.RemoveAll(*dir); err == nil {
			return
		}
		if attempt < fakeGoRemovalAttempts-1 {
			time.Sleep(fakeGoRemovalDelay)
		}
	}
	fmt.Fprintf(os.Stderr, "fake go: %s could not be removed and is left as it is: %v\n", *dir, err)
}

// The retry the removal above makes, mirroring internal/testkit's own.
const (
	fakeGoRemovalAttempts = 3
	fakeGoRemovalDelay    = 100 * time.Millisecond
)

// installFakeGo puts an executable named `go` in dir and counts it.
func installFakeGo(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fakeGoName())
	if err := linkOrCopyExecutable(testkit.TestBinary(), path); err != nil {
		return "", err
	}
	fakeGoInstalls.Add(1)
	return path, nil
}

// Bin is the path of the scripted `go`.
//
// It is absolute, because every call site that takes a toolchain path runs it
// with a working directory of its own — a snapshot, a fixture copy — and a
// relative one would be resolved against that instead.
func (f *Fake) Bin() string { return f.bin }

// BinDir is the directory holding the scripted `go`, and holds nothing else.
//
// It is what goes in front of PATH for a call site that locates the toolchain
// itself; see [Fake.Export], which is the form to use when the process's own
// environment is the only way in.
func (f *Fake) BinDir() string { return filepath.Dir(f.bin) }

// RulesPath and CallsPath are the two files the protocol is carried in, for a
// test that wants to read the log as bytes or to point a second process at the
// same table.
func (f *Fake) RulesPath() string { return f.rules }

// CallsPath is the append-only log [Fake.Calls] reads.
func (f *Fake) CallsPath() string { return f.calls }

// Install puts a second reachable copy of the scripted `go` in dir and returns
// its path.
//
// It is for a test whose subject is *where* the toolchain is — a relative
// configured path, a directory placed on PATH by hand — and it is the exception
// rather than the way in: [Fake.Bin] is the shared install every other test
// uses, and this is the only thing in the package that adds to
// [FakeGoInstalls]. The behaviour travels in the environment rather than in the
// file, so every copy answers from the same table and records into the same
// log.
//
// The file is a hard link to this test binary where the filesystem allows one
// and a copy where it does not; see [sharedFakeGo] for why the difference is
// worth paying attention to.
func (f *Fake) Install(dir string) string {
	f.t.Helper()
	path, err := installFakeGo(dir)
	if err != nil {
		f.t.Fatalf("installing the fake `go` in %s: %v", dir, err)
		return ""
	}
	return path
}

// Env returns base with the two control variables applied, which is what a
// child needs to answer as `go`.
//
// The base is the test's own composed environment — [testkit.Compose] or
// [testkit.Environment.Vars] — rather than this process's, for the reason the
// harness composes one at all: a developer with GO_MUTANTS_ACTIVE exported would
// otherwise have a scripted compile running a mutant.
func (f *Fake) Env(base []string) []string {
	return append(slices.Clone(base), FakeGoRulesEnv+"="+f.rules, FakeGoCallsEnv+"="+f.calls)
}

// Export publishes the fake into this process: the two control variables, and
// its directory in front of PATH.
//
// It is for the call sites that locate the toolchain themselves — internal/cli's
// `doctor` and internal/engine's pipeline both call gocmd.Locate with no
// explicit path and a nil environment, so a child of theirs inherits this
// process's own. It calls t.Setenv, so a test that uses it may not call
// t.Parallel; a test with a call site that takes a path or an environment wants
// [Fake.Bin] and [Fake.Env] instead, and stays parallel.
//
// After it, *every* `go` this process starts is the fake, the harness's own
// included — PATH is a process-wide global and the fake answers to the name.
// That is the point for the code under test and a hazard for everything else,
// so the one harness probe that runs a `go` is forced first:
// [testkit.ResolveToolchainDirectories] pins GOENV, GOPATH and GOMODCACHE from
// the machine's real toolchain before the PATH is changed. Without it a later
// [testkit.Compose] in the same test would send `go env` to the fake, which
// records a call nobody asked for — aimed straight at the exact-count
// assertions this package exists to make possible — and falls back to
// build.Default for the directories. It is lazy and therefore order-dependent,
// which is why forcing it is not optional.
func (f *Fake) Export() {
	f.t.Helper()
	// While the machine's own `go` is still the one PATH resolves.
	testkit.ResolveToolchainDirectories()
	f.t.Setenv(FakeGoRulesEnv, f.rules)
	f.t.Setenv(FakeGoCallsEnv, f.calls)
	path := f.BinDir()
	if existing := os.Getenv("PATH"); existing != "" {
		path += string(filepath.ListSeparator) + existing
	}
	f.t.Setenv("PATH", path)
}

// On scripts the answer to every call whose argv begins with the given
// arguments, and returns the rule to fill in.
//
// The arguments are what follows the binary name, so `f.On("test", "-c")`
// matches `go test -c -o out ./pkg`. The longest matching prefix wins and, among
// prefixes of the same length, the one registered last — so a convenience rule
// can be laid down first and overridden for the one call that matters.
//
// At least one argument is required. A rule matching everything would be the
// silent success the fail-closed rule exists to refuse, and a test that really
// wants one writes the argv out.
func (f *Fake) On(argv ...string) *FakeRule {
	f.t.Helper()
	if len(argv) == 0 {
		f.t.Fatalf("a fake `go` rule needs at least one argument to match: a rule matching every " +
			"call is the silent success the fail-closed rule exists to refuse")
		return &FakeRule{f: f, rule: &fakeRule{}}
	}
	rule := &fakeRule{Argv: slices.Clone(argv)}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.table = append(f.table, rule)
	f.saveLocked()
	return &FakeRule{f: f, rule: rule}
}

// Version scripts `go version` with a release the caller names.
//
// The target is this process's own, because that is the only answer a caller can
// check against anything: internal/gocmd rejects a line that does not end in a
// well-formed `os/arch`, and a run reports the target it was measured on.
//
// A release no toolchain will ever carry — "1.99.0" — is the spelling to use, so
// that a result accidentally produced by the real `go` cannot be mistaken for a
// scripted one.
func (f *Fake) Version(release string) *FakeRule {
	f.t.Helper()
	return f.On("version").
		Stdout("go version go" + release + " " + runtime.GOOS + "/" + runtime.GOARCH + "\n")
}

// ListJSON scripts `go list -json` with the given packages.
//
// The encoding is the go command's own: a stream of pretty-printed objects with
// no enclosing array, which is what makes a streaming decoder the right reader
// for it and what internal/execute's decoder expects.
//
// It matches every `go list`, so a phase that issues a second one with a
// different shape — internal/engine's scope resolution runs `go list -e -f` —
// scripts that separately with a longer prefix.
func (f *Fake) ListJSON(packages ...ListPackage) *FakeRule {
	f.t.Helper()
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetIndent("", "\t")
	for _, pkg := range packages {
		if err := encoder.Encode(pkg); err != nil {
			f.t.Fatalf("encoding the scripted `go list` answer for %s: %v", pkg.ImportPath, err)
			return &FakeRule{f: f, rule: &fakeRule{}}
		}
	}
	return f.On("list").Stdout(out.String())
}

// Calls returns every invocation the fake answered, in the order they were
// recorded.
//
// It reads the log rather than remembering anything, because the calls happen in
// other processes: what a phase asked for is a fact on disk, and reading it is
// what lets a test assert on an argv a call site composed without that call site
// having to be injectable.
func (f *Fake) Calls() []Call {
	f.t.Helper()
	data, err := os.ReadFile(f.calls)
	if err != nil {
		f.t.Fatalf("reading the fake `go` call log %s: %v", f.calls, err)
		return nil
	}
	var calls []Call
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var call Call
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			f.t.Fatalf("reading a line of the fake `go` call log %s: %v\n%s", f.calls, err, line)
			return nil
		}
		calls = append(calls, call)
	}
	return calls
}

// Stdout scripts what the call prints on standard output.
func (r *FakeRule) Stdout(text string) *FakeRule {
	return r.set(func(rule *fakeRule) { rule.Stdout = text })
}

// Stderr scripts what the call prints on standard error.
//
// The two streams are separate here and merged by whatever runs the child, which
// is internal/runner's arrangement: a compiler writes its diagnostics to stderr
// and a listing writes its document to stdout, and a fake that could only write
// one of them could not script the pair a failed build really produces.
func (r *FakeRule) Stderr(text string) *FakeRule {
	return r.set(func(rule *fakeRule) { rule.Stderr = text })
}

// Exit scripts the status the call exits with. Zero is the default and needs no
// rule.
func (r *FakeRule) Exit(code int) *FakeRule {
	return r.set(func(rule *fakeRule) { rule.Exit = code })
}

// Sleep makes the call hang for the given duration before it exits.
//
// It is how a toolchain that does not answer is scripted, which is otherwise a
// `go` nobody can install. The output, if the rule has any, is written first and
// the sleep is last, so a test can tell "it never spoke" apart from "it spoke
// and then stopped" — and the caller's own deadline is what ends it, which is
// the thing being tested.
func (r *FakeRule) Sleep(d time.Duration) *FakeRule {
	return r.set(func(rule *fakeRule) { rule.SleepMS = d.Milliseconds() })
}

// CreateOutput makes the call create an executable file at its `-o` path.
//
// It is what lets `go test -c -o <path>` be scripted at all: the caller checks
// that a binary was left behind, and a fake that only printed would fail that
// check for a reason that has nothing to do with the test.
//
// The file is the fake itself, on every platform, which is what makes the
// produced binary something a test can go on to *run*. internal/execute's
// scheduler starts a compiled test binary with `-test.timeout=…` and the
// mutant's own arguments, and a scripted compile whose output was an inert file
// could only ever be stat'ed — so the whole half of that package below the build
// would still need a real toolchain. Because the output is the fake, the
// scheduler's child answers from the same rule table, records its argv and its
// activation in the same log, and a mutant can be scripted killed or survived
// by exit status.
//
// A shell script would have been simpler and is wrong twice over: Windows has
// no shebang, so the two platforms would produce different things, and neither
// could be asked to fail on demand.
//
// The hazard worth stating is the same one the fake has everywhere, with one
// less guard: the produced file is this test binary, named whatever `-o` said
// rather than `go`, so [Main]'s refusal cannot recognise it. Run with the
// control variables — which is what every environment internal/execute composes
// carries — it is the fake; run without them it would run the suite. A test
// that starts the produced binary should start it the way the code under test
// does, with the environment it composed.
//
// A rule that promises this and is matched by an argv with no `-o` in it is a
// misconfigured fake and exits [FakeGoNoRule] rather than pretending.
func (r *FakeRule) CreateOutput() *FakeRule {
	return r.set(func(rule *fakeRule) { rule.CreateOutput = true })
}

// set amends this rule and republishes the table, both under the table's lock.
//
// One lock over the pair rather than two: a rule amended while another
// goroutine was publishing would otherwise be written out half applied, and the
// child that read it at that moment would answer from a table nobody wrote.
func (r *FakeRule) set(apply func(*fakeRule)) *FakeRule {
	r.f.t.Helper()
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	apply(r.rule)
	r.f.saveLocked()
	return r
}

// saveLocked writes the whole table out, so that a rule amended after the
// binary's path was handed over still reaches the child. The caller holds
// [Fake.mu].
//
// The write is a rename over a complete file rather than a truncate-and-write,
// because the child may already be running: a reader that arrived halfway
// through would see a truncated document and refuse the call, which is a flake
// rather than a failure anybody can act on.
func (f *Fake) saveLocked() {
	f.t.Helper()
	data, err := json.MarshalIndent(f.table, "", "\t")
	if err != nil {
		f.t.Fatalf("encoding the fake `go` rule table: %v", err)
		return
	}
	temp := f.rules + ".writing"
	if err := os.WriteFile(temp, append(data, '\n'), 0o600); err != nil {
		f.t.Fatalf("writing the fake `go` rule table %s: %v", temp, err)
		return
	}
	if err := os.Rename(temp, f.rules); err != nil {
		f.t.Fatalf("publishing the fake `go` rule table %s: %v", f.rules, err)
	}
}

// linkFile is os.Link, behind a name so that the copy fallback below can be
// driven on a machine where linking works. See SetFakeGoLinker in
// export_test.go.
var linkFile = os.Link

// linkOrCopyExecutable makes to a second name for from, by link where the
// filesystem allows it and by copy where it does not.
func linkOrCopyExecutable(from, to string) error {
	if err := linkFile(from, to); err == nil {
		return nil
	}
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	destination, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(destination, source)
	return errors.Join(copyErr, destination.Close())
}

// Main is the TestMain of a package whose tests script the `go` command.
//
//	func TestMain(m *testing.M) {
//		os.Exit(mutantkit.Main(m))
//	}
//
// When [FakeGoRulesEnv] is set this process *is* the go command: it answers from
// the table the variable names, records the call, and its status is the
// process's. Otherwise the suite runs as usual.
//
// It goes through [testkit.Helper], which is the same shape and gives the fake
// the same thing every helper process in this repository needs: a private
// GOCOVERDIR. A fake is this very test binary re-executed, so under `go test
// -cover` it is coverage-instrumented and its exit hook writes a meta-data file
// named after the binary — identical for every fake — into the single directory
// `go test` exports. The concurrent renames collide, and on Windows the loser
// prints "coverage meta-data emit failed" onto the very stderr a test here
// asserts the exact bytes of.
//
// A package whose TestMain already does something of its own still runs the
// suite through here and asks [IsFakeGo] afterwards:
//
//	func TestMain(m *testing.M) {
//		code := mutantkit.Main(m)
//		if mutantkit.IsFakeGo() {
//			os.Exit(code)
//		}
//		releaseWhateverThisPackageShares(code != 0)
//		os.Exit(code)
//	}
//
// Calling m.Run directly and dispatching only on the fake path does not work,
// and the way it fails is worth writing down: the coverage root is published by
// the *suite* branch, so a suite that ran itself leaves every fake child with
// nowhere private to write and each one refuses the call it was started for.
// The root package's TestMain is the shape above for exactly that reason.
//
// A package that also ran a [testkit.Helper] program of its own — none scripts
// the toolchain today, and internal/runner is the one that would — would put
// the fake's dispatch in front of its own, because one binary can be two kinds
// of child and only one of the two variables is ever set:
//
//	func TestMain(m *testing.M) {
//		if mutantkit.IsFakeGo() {
//			os.Exit(mutantkit.Main(m))
//		}
//		os.Exit(testkit.Helper(m, myHelperEnv, runMyHelper))
//	}
func Main(m *testing.M) int {
	if status, stray := refuseAStrayFake(); stray {
		return status
	}
	code := testkit.Helper(m, FakeGoRulesEnv, runFakeGo)
	// After the suite and not in a cleanup, because a TestMain ends in os.Exit
	// and runs no deferred function. A fake process never installed anything —
	// it *is* the install — so this is a no-op there and the parent's directory
	// is safe from it.
	removeSharedFakeGo()
	return code
}

// IsFakeGo reports whether this process was started as the scripted `go`.
//
// It is the dispatch half of [Main] on its own, for a TestMain that has
// something else to do; see [Main] for the shape.
func IsFakeGo() bool { return testkit.HelperEnabled(FakeGoRulesEnv) }

// refuseAStrayFake stops a test binary that was started as `go` with the switch
// unset from running its own suite in place of the go command.
//
// It is a guard against the one mistake this design makes possible and cannot
// otherwise detect. The fake is a link to the test binary, so a call site that
// dropped [FakeGoRulesEnv] from the environment it composed — an environment
// policy that strips a prefix, an explicit []string that forgot it — would start
// the whole suite as a child of itself, once per `go` command, and the only
// symptom would be a test that took a very long time. Refusing by the name the
// binary was invoked under costs one string comparison per test binary start and
// turns that into a message.
func refuseAStrayFake() (int, bool) {
	if IsFakeGo() || len(os.Args) == 0 {
		return 0, false
	}
	if filepath.Base(os.Args[0]) != fakeGoName() {
		return 0, false
	}
	fmt.Fprintf(os.Stderr,
		"fake go: this test binary was started as %q with %s unset, so it would have run its whole "+
			"suite in place of the go command. The environment the caller composed has to carry %s "+
			"and %s.\n",
		os.Args[0], FakeGoRulesEnv, FakeGoRulesEnv, FakeGoCallsEnv)
	return FakeGoNoRule, true
}

// runFakeGo is the whole scripted toolchain: record the call, find the rule,
// answer it.
//
// The call is recorded before the rule is looked up, so that a refused call is
// in the log too. A test whose subject is "which command did this phase issue?"
// is very often a test whose answer is "one nobody scripted", and a log that
// dropped it would leave the reader with a status and no argv.
func runFakeGo(args []string) int {
	recordFakeCall(os.Getenv(FakeGoCallsEnv), args)

	table, err := readFakeRules(os.Getenv(FakeGoRulesEnv))
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake go: %v\n", err)
		return FakeGoNoRule
	}
	rule := matchFakeRule(table, args)
	if rule == nil {
		fmt.Fprintf(os.Stderr, "fake go: no rule for `go %s`\n", strings.Join(args, " "))
		return FakeGoNoRule
	}
	if rule.CreateOutput {
		if err := createFakeOutput(args); err != nil {
			fmt.Fprintf(os.Stderr, "fake go: %v\n", err)
			return FakeGoNoRule
		}
	}
	_, _ = io.WriteString(os.Stdout, rule.Stdout)
	_, _ = io.WriteString(os.Stderr, rule.Stderr)
	if rule.SleepMS > 0 {
		time.Sleep(time.Duration(rule.SleepMS) * time.Millisecond)
	}
	return rule.Exit
}

// readFakeRules reads the table the parent wrote.
func readFakeRules(path string) ([]fakeRule, error) {
	if path == "" {
		return nil, errors.New(FakeGoRulesEnv + " is empty, so there is no rule table to answer from")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the rule table: %w", err)
	}
	var table []fakeRule
	if err := json.Unmarshal(data, &table); err != nil {
		return nil, fmt.Errorf("reading the rule table %s: %w", path, err)
	}
	return table, nil
}

// matchFakeRule returns the rule whose argv is the longest prefix of args, or
// nil when none is.
//
// Among prefixes of the same length the last registered wins, so a convenience
// rule laid down first can be overridden for the one call that matters.
func matchFakeRule(table []fakeRule, args []string) *fakeRule {
	var best *fakeRule
	for index := range table {
		rule := &table[index]
		if len(rule.Argv) > len(args) {
			continue
		}
		if !slices.Equal(rule.Argv, args[:len(rule.Argv)]) {
			continue
		}
		if best == nil || len(rule.Argv) >= len(best.Argv) {
			best = rule
		}
	}
	return best
}

// recordFakeCall appends one line to the call log.
//
// The whole line is written in a single call to an O_APPEND descriptor, which is
// what makes the log safe for the workers of one phase to write at once: the
// append and the offset update are one operation, so two compiles running
// concurrently produce two whole lines rather than one interleaved one.
//
// A log that cannot be written is not a failure. The parent reads it and will
// say what is missing far better than a child can, and a fake that refused to
// answer because it could not take a note would turn a diagnostic into the
// outcome of the test.
func recordFakeCall(path string, args []string) {
	if path == "" {
		return
	}
	dir, err := os.Getwd()
	if err != nil {
		dir = ""
	}
	line, err := json.Marshal(Call{
		Argv:     args,
		Dir:      dir,
		EnvNames: fakeEnvNames(),
		Env:      fakeEnvValues(),
	})
	if err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = file.Write(append(line, '\n'))
	_ = file.Close()
}

// fakeEnvNames is every variable the child could see, sorted so that two runs of
// one test produce the same log.
func fakeEnvNames() []string {
	environ := os.Environ()
	names := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// fakeEnvValues is the value of each variable in [recordedEnvValues] that is
// set. A variable that is not set is absent rather than empty, because the two
// are different claims: internal/execute's control environment is defined by
// GO_MUTANTS_ACTIVE being *unset*.
func fakeEnvValues() map[string]string {
	values := make(map[string]string, len(recordedEnvValues))
	for _, name := range recordedEnvValues {
		if value, ok := os.LookupEnv(name); ok {
			values[name] = value
		}
	}
	return values
}

// createFakeOutput writes an executable file at the argv's `-o` path.
func createFakeOutput(args []string) error {
	path, ok := dashOPath(args)
	if !ok {
		return errors.New("the rule creates its `-o` output, and `go " + strings.Join(args, " ") +
			"` names no `-o` path")
	}
	// The fake itself, so the produced binary is something the caller can
	// start and script in turn: see [FakeRule.CreateOutput].
	self, err := os.Executable()
	if err != nil {
		return err
	}
	return linkOrCopyExecutable(self, path)
}

// dashOPath reads the output path out of an argv, in both spellings the go
// command accepts.
func dashOPath(args []string) (string, bool) {
	for index, arg := range args {
		if after, found := strings.CutPrefix(arg, "-o="); found {
			return after, true
		}
		if arg == "-o" && index+1 < len(args) {
			return args[index+1], true
		}
	}
	return "", false
}
