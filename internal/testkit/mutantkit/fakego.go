// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

const (
	FakeGoRulesEnv = "TESTKIT_FAKE_GO_RULES"
	FakeGoCallsEnv = "TESTKIT_FAKE_GO_CALLS"
)

const FakeGoNoRule = testkit.HelperMisuse

func fakeGoName() string {
	if runtime.GOOS == "windows" {
		return "go.exe"
	}
	return "go"
}

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

type Call struct {
	Argv     []string          `json:"argv"`
	Dir      string            `json:"dir"`
	EnvNames []string          `json:"env_names"`
	Env      map[string]string `json:"env"`
}

func (c Call) String() string {
	if c.Dir == "" {
		return "`go " + strings.Join(c.Argv, " ") + "`"
	}
	return "`go " + strings.Join(c.Argv, " ") + "` (in " + c.Dir + ")"
}

type ListPackage struct {
	ImportPath   string   `json:"ImportPath"`
	Dir          string   `json:"Dir"`
	TestGoFiles  []string `json:"TestGoFiles,omitempty"`
	XTestGoFiles []string `json:"XTestGoFiles,omitempty"`
}

type fakeRule struct {
	Argv         []string `json:"argv"`
	Stdout       string   `json:"stdout,omitempty"`
	Stderr       string   `json:"stderr,omitempty"`
	Exit         int      `json:"exit,omitempty"`
	SleepMS      int64    `json:"sleep_ms,omitempty"`
	CreateOutput bool     `json:"create_output,omitempty"`
}

type Fake struct {
	t     testing.TB
	bin   string
	rules string
	calls string

	mu    sync.Mutex
	table []*fakeRule
}

type FakeRule struct {
	f    *Fake
	rule *fakeRule
}

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
	if err := os.WriteFile(f.calls, nil, 0o600); err != nil {
		t.Fatalf("creating the fake `go` call log %s: %v", f.calls, err)
	}
	logInputs(t, "fake-go="+f.bin, "rules="+f.rules, "calls="+f.calls)
	return f
}

var fakeGoInstalls atomic.Int64

var fakeGoTempRoot = os.TempDir()

var sharedFakeGoDir atomic.Pointer[string]

var sharedFakeGo = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp(fakeGoTempRoot, "go-mutants-fake-go-")
	if err != nil {
		return "", err
	}
	sharedFakeGoDir.Store(&dir)
	return installFakeGo(dir)
})

func removeSharedFakeGo() {
	dir := sharedFakeGoDir.Swap(nil)
	if dir == nil {
		return
	}
	var err error
	attempts, delay := fakeGoRemovalPolicy(runtime.GOOS)
	for attempt := range attempts {
		if err = os.RemoveAll(*dir); err == nil {
			return
		}
		if attempt < attempts-1 {
			time.Sleep(delay)
		}
	}
	fmt.Fprintf(os.Stderr, "fake go: %s could not be removed and is left as it is: %v\n", *dir, err)
}

func fakeGoRemovalPolicy(goos string) (attempts int, delay time.Duration) {
	if goos == "windows" {
		return 5, 200 * time.Millisecond
	}
	return 3, 100 * time.Millisecond
}

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

func (f *Fake) Bin() string { return f.bin }

func (f *Fake) BinDir() string { return filepath.Dir(f.bin) }

func (f *Fake) RulesPath() string { return f.rules }

func (f *Fake) CallsPath() string { return f.calls }

func (f *Fake) Install(dir string) string {
	f.t.Helper()
	path, err := installFakeGo(dir)
	if err != nil {
		f.t.Fatalf("installing the fake `go` in %s: %v", dir, err)
		return ""
	}
	return path
}

func (f *Fake) Env(base []string) []string {
	return append(slices.Clone(base), FakeGoRulesEnv+"="+f.rules, FakeGoCallsEnv+"="+f.calls)
}

func (f *Fake) Export() {
	f.t.Helper()
	testkit.ResolveToolchainDirectories()
	f.t.Setenv(FakeGoRulesEnv, f.rules)
	f.t.Setenv(FakeGoCallsEnv, f.calls)
	path := f.BinDir()
	if existing := os.Getenv("PATH"); existing != "" {
		path += string(filepath.ListSeparator) + existing
	}
	f.t.Setenv("PATH", path)
}

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

func (f *Fake) Version(release string) *FakeRule {
	f.t.Helper()
	return f.On("version").
		Stdout("go version go" + release + " " + runtime.GOOS + "/" + runtime.GOARCH + "\n")
}

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

func (r *FakeRule) Stdout(text string) *FakeRule {
	return r.set(func(rule *fakeRule) { rule.Stdout = text })
}

func (r *FakeRule) Stderr(text string) *FakeRule {
	return r.set(func(rule *fakeRule) { rule.Stderr = text })
}

func (r *FakeRule) Exit(code int) *FakeRule {
	return r.set(func(rule *fakeRule) { rule.Exit = code })
}

func (r *FakeRule) Sleep(d time.Duration) *FakeRule {
	return r.set(func(rule *fakeRule) { rule.SleepMS = d.Milliseconds() })
}

func (r *FakeRule) CreateOutput() *FakeRule {
	return r.set(func(rule *fakeRule) { rule.CreateOutput = true })
}

func (r *FakeRule) set(apply func(*fakeRule)) *FakeRule {
	r.f.t.Helper()
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	apply(r.rule)
	r.f.saveLocked()
	return r
}

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

var linkFile = platformLinker(runtime.GOOS)

func platformLinker(goos string) func(from, to string) error {
	if !HardLinksAreRemovable(goos) {
		return refuseToLink
	}
	return os.Link
}

func HardLinksAreRemovable(goos string) bool { return goos != "windows" }

func refuseToLink(_, _ string) error { return errNoRemovableHardLink }

var errNoRemovableHardLink = errors.New(
	"a hard link to a running executable cannot be removed on this platform")

func linkOrCopyExecutable(from, to string) error {
	if err := os.Remove(to); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := linkFile(from, to); err == nil {
		return nil
	}
	return copyExecutable(from, to)
}

func copyExecutable(from, to string) error {
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()

	staging, err := os.CreateTemp(filepath.Dir(to), filepath.Base(to)+".tmp-")
	if err != nil {
		return err
	}
	name := staging.Name()
	_, copyErr := io.Copy(staging, source)
	chmodErr := staging.Chmod(0o755)
	if err := errors.Join(copyErr, chmodErr, staging.Close()); err != nil {
		return errors.Join(err, os.Remove(name))
	}
	if err := os.Rename(name, to); err != nil {
		return errors.Join(err, os.Remove(name))
	}
	return nil
}

func Main(m *testing.M) int {
	if status, stray := refuseAStrayFake(); stray {
		return status
	}
	code := testkit.Helper(m, FakeGoRulesEnv, runFakeGo)
	removeSharedFakeGo()
	return code
}

func IsFakeGo() bool { return testkit.HelperEnabled(FakeGoRulesEnv) }

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

func fakeEnvValues() map[string]string {
	values := make(map[string]string, len(recordedEnvValues))
	for _, name := range recordedEnvValues {
		if value, ok := os.LookupEnv(name); ok {
			values[name] = value
		}
	}
	return values
}

func createFakeOutput(args []string) error {
	path, ok := dashOPath(args)
	if !ok {
		return errors.New("the rule creates its `-o` output, and `go " + strings.Join(args, " ") +
			"` names no `-o` path")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	return linkOrCopyExecutable(self, path)
}

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
