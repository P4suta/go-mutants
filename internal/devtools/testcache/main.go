// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	buildCacheEnv = "GO_MUTANTS_TEST_GOCACHE"
	keepDirEnv    = "GO_MUTANTS_TEST_KEEP_DIR"
)

const (
	harnessDir   = "go-mutants-test"
	buildCacheIn = "go-build"
	keptIn       = "kept"
)

const (
	buildCacheMarker = ".go-mutants-testcache"
	keptMarker       = ".go-mutants-kept"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, environment(), deps{}))
}

func environment() map[string]string {
	env := make(map[string]string, len(os.Environ()))
	for _, entry := range os.Environ() {
		if name, value, ok := strings.Cut(entry, "="); ok {
			env[name] = value
		}
	}
	return env
}

type deps struct {
	cleaner   func(dir string) error
	userCache func() (string, error)
	userHome  func() (string, error)
	sleep     func(d time.Duration)
}

func (d deps) withDefaults() deps {
	if d.cleaner == nil {
		d.cleaner = goCleanCache
	}
	if d.userCache == nil {
		d.userCache = os.UserCacheDir
	}
	if d.userHome == nil {
		d.userHome = os.UserHomeDir
	}
	if d.sleep == nil {
		d.sleep = time.Sleep
	}
	return d
}

func run(args []string, stdout, stderr io.Writer, env map[string]string, d deps) int {
	d = d.withDefaults()
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	switch name := args[0]; name {
	case "path":
		return runPath(args[1:], stdout, stderr, env, d)
	case "status":
		return runStatus(args[1:], stdout, stderr, env, d)
	case "clean":
		return runClean(args[1:], stdout, stderr, env, d)
	case "trim":
		return runTrim(args[1:], stdout, stderr, env, d)
	case "exec":
		return runExec(args[1:], stderr, env, d)
	case "help", "-h", "-help", "--help":
		usage(stdout)
		return exitOK
	default:
		printf(stderr, "testcache: unknown subcommand %q\n", name)
		usage(stderr)
		return exitUsage
	}
}

func usage(w io.Writer) {
	printf(w, "%s", `testcache keeps the test suites' `+"`go`"+` commands out of the developer's own build cache.

usage:
  testcache path [--kept] [--marker]   print the resolved directory, the kept
                                       scratch root, or a marker file's name
  testcache status                     print the cache and kept-scratch sizes
  testcache clean                      empty the cache and the kept scratch root
  testcache trim --budget <size>       empty the cache only if it is over budget
  testcache exec [--budget <size>] -- <cmd...>
                                       run <cmd...> with GOCACHE pointed at the
                                       cache, report the growth, then trim

The directory is `+buildCacheEnv+` when it names an absolute path,
and <os.UserCacheDir()>/`+harnessDir+`/`+buildCacheIn+` otherwise; the kept
scratch root is `+keepDirEnv+` or <os.UserCacheDir()>/`+harnessDir+`/`+keptIn+`.
Sizes are binary: B, KiB, MiB, GiB, TiB.

Nothing here removes a directory that does not hold its marker file (`+buildCacheMarker+`
for the cache, `+keptMarker+` for the kept root). The harness writes it when a
test resolves the cache, and `+"`exec`"+` writes it before the run it wraps -- but
never into a directory that already holds files that are not the harness's.
`)
}

func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func runPath(args []string, stdout, stderr io.Writer, env map[string]string, d deps) int {
	flags := flag.NewFlagSet("path", flag.ContinueOnError)
	flags.SetOutput(stderr)
	kept := flags.Bool("kept", false, "print the kept scratch root rather than the build cache")
	marker := flags.Bool("marker", false, "print the name of the ownership marker file rather than a directory")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if code := requireNoArguments("path", flags.Args(), stderr); code != exitOK {
		return code
	}

	if *marker {
		printf(stdout, "%s\n", markerFor(*kept))
		return exitOK
	}

	where, err := resolveTarget(env, d, *kept)
	if err != nil {
		printf(stderr, "testcache: %v\n", err)
		return exitFailure
	}
	printf(stdout, "%s\n", where.dir)
	return exitOK
}

func markerFor(kept bool) string {
	if kept {
		return keptMarker
	}
	return buildCacheMarker
}

func resolveTarget(env map[string]string, d deps, kept bool) (target, error) {
	if kept {
		return keptTarget(env, d)
	}
	return buildCacheTarget(env, d)
}

func runStatus(args []string, stdout, stderr io.Writer, env map[string]string, d deps) int {
	if code := requireNoArguments("status", args, stderr); code != exitOK {
		return code
	}
	cache, err := buildCacheTarget(env, d)
	if err != nil {
		printf(stderr, "testcache: %v\n", err)
		return exitFailure
	}
	kept, err := keptTarget(env, d)
	if err != nil {
		printf(stderr, "testcache: %v\n", err)
		return exitFailure
	}
	reportUsage(stdout, stderr, cache)
	reportUsage(stdout, stderr, kept)
	return exitOK
}

func runClean(args []string, stdout, stderr io.Writer, env map[string]string, d deps) int {
	if code := requireNoArguments("clean", args, stderr); code != exitOK {
		return code
	}
	cache, err := buildCacheTarget(env, d)
	if err != nil {
		printf(stderr, "testcache: %v\n", err)
		return exitFailure
	}
	kept, err := keptTarget(env, d)
	if err != nil {
		printf(stderr, "testcache: %v\n", err)
		return exitFailure
	}

	removed := wipe(stdout, stderr, cache, d)
	keptUsed, err := measure(kept.dir)
	if err != nil {
		printf(stderr, "testcache: %s could not be measured completely: %v\n", kept, err)
	}
	if !removeTree(stdout, stderr, kept, keptUsed, d) {
		removed = false
	}
	if !removed {
		return exitFailure
	}
	return exitOK
}

func runTrim(args []string, stdout, stderr io.Writer, env map[string]string, d deps) int {
	flags := flag.NewFlagSet("trim", flag.ContinueOnError)
	flags.SetOutput(stderr)
	budget := flags.String("budget", "", "empty the cache when it holds more than this (B, KiB, MiB, GiB, TiB)")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if code := requireNoArguments("trim", flags.Args(), stderr); code != exitOK {
		return code
	}
	limit, err := parseSize(*budget)
	if err != nil {
		printf(stderr, "testcache: --budget: %v\n", err)
		return exitUsage
	}
	cache, err := buildCacheTarget(env, d)
	if err != nil {
		printf(stderr, "testcache: %v\n", err)
		return exitFailure
	}
	trimToBudget(stdout, stderr, cache, limit, d)
	return exitOK
}

func runExec(args []string, stderr io.Writer, env map[string]string, d deps) int {
	flags := flag.NewFlagSet("exec", flag.ContinueOnError)
	flags.SetOutput(stderr)
	budget := flags.String("budget", "", "empty the cache afterwards if it holds more than this (B, KiB, MiB, GiB, TiB)")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	argv := flags.Args()
	if len(argv) == 0 {
		printf(stderr, "testcache: `exec` needs a command to run: testcache exec [--budget <size>] -- <cmd...>\n")
		return exitUsage
	}

	limit := int64(-1)
	if flagWasGiven(flags, "budget") {
		parsed, err := parseSize(*budget)
		if err != nil {
			printf(stderr, "testcache: --budget: %v\n", err)
			return exitUsage
		}
		limit = parsed
	}

	cache, err := buildCacheTarget(env, d)
	if err != nil {
		printf(stderr, "testcache: %v\n", err)
		return exitFailure
	}

	stamped, stampErr := stampDirectory(cache)
	switch {
	case stampErr != nil:
		printf(stderr, "testcache: %v\n", stampErr)
		return exitFailure
	case !stamped:
		printf(stderr, "testcache: %s already holds files that are not the harness's, so it was not "+
			"stamped: the run below will use it and nothing here will ever empty it. Point %s at a "+
			"directory of its own if that is not what you meant.\n", cache, cache.variable)
	}

	before, err := measure(cache.dir)
	if err != nil {
		printf(stderr, "testcache: %s could not be measured completely before the run: %v\n", cache, err)
	}
	code := spawn(argv, cache.dir, env, stderr)
	after, err := measure(cache.dir)
	if err != nil {
		printf(stderr, "testcache: %s could not be measured completely: %v\n", cache, err)
	}
	printf(stderr, "testcache: %s %d bytes (%+d)\n", cache.dir, after.bytes, after.bytes-before.bytes)

	if limit >= 0 {
		trimToBudget(stderr, stderr, cache, limit, d)
	}
	return code
}

func flagWasGiven(flags *flag.FlagSet, name string) bool {
	given := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == name {
			given = true
		}
	})
	return given
}

func spawn(argv []string, cache string, env map[string]string, stderr io.Writer) int {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = childEnvironment(env, cache)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	err := cmd.Run()
	var exited *exec.ExitError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &exited):
		return childStatus(exited.ProcessState)
	default:
		printf(stderr, "testcache: %s could not be run: %v\n", argv[0], err)
		return 127
	}
}

func childStatus(state *os.ProcessState) int {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	if code := state.ExitCode(); code >= 0 {
		return code
	}
	return exitFailure
}

func childEnvironment(env map[string]string, cache string) []string {
	child := make([]string, 0, len(env)+2)
	for name, value := range env {
		if sameEnvName(name, "GOCACHE") || sameEnvName(name, buildCacheEnv) {
			continue
		}
		child = append(child, name+"="+value)
	}
	slices.Sort(child)
	return append(child, "GOCACHE="+cache, buildCacheEnv+"="+cache)
}

func sameEnvName(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func trimToBudget(report, stderr io.Writer, t target, limit int64, d deps) {
	used, err := measure(t.dir)
	partial := ""
	if err != nil {
		printf(stderr, "testcache: %s could not be measured completely: %v\n", t, err)
		partial = ", counting only what could be read"
	}
	if used.bytes <= limit {
		printf(report, "testcache: %s holds %s (%d bytes)%s, under the budget of %s; kept\n",
			t.dir, humanSize(used.bytes), used.bytes, partial, humanSize(limit))
		return
	}
	printf(report, "testcache: %s holds %s (%d bytes)%s, over the budget of %s\n",
		t.dir, humanSize(used.bytes), used.bytes, partial, humanSize(limit))
	wipe(report, stderr, t, d)
}

func wipe(stdout, stderr io.Writer, t target, d deps) bool {
	if !licensed(stderr, t) {
		return false
	}
	used, err := measure(t.dir)
	if err != nil {
		printf(stderr, "testcache: %s could not be measured completely: %v\n", t, err)
	}
	if err := d.cleaner(t.dir); err != nil {
		printf(stderr, "testcache: %v\n", err)
	}
	return removeTree(stdout, stderr, t, used, d)
}

const removalRetryDelay = 2 * time.Second

func removeTree(stdout, stderr io.Writer, t target, used diskUsage, d deps) bool {
	if !licensed(stderr, t) {
		return false
	}
	err := os.RemoveAll(t.dir)
	if err != nil {
		d.sleep(removalRetryDelay)
		err = os.RemoveAll(t.dir)
	}
	if err != nil {
		printf(stderr, "testcache: the %s %s could not be removed, and is left as it is "+
			"(a file in it is probably still open): %v\n", t.label, t, err)
		return true
	}
	if !used.exists {
		printf(stdout, "testcache: the %s %s was already empty\n", t.label, t)
		return true
	}
	printf(stdout, "testcache: removed the %s %s (%s in %s)\n",
		t.label, t, humanSize(used.bytes), plural(used.files, "file"))
	return true
}

const labelWidth = len("kept scratch:")

func reportUsage(stdout, stderr io.Writer, t target) {
	used, err := measure(t.dir)
	if err != nil {
		printf(stderr, "testcache: %s could not be measured completely: %v\n", t, err)
	}
	printf(stdout, "%-*s %s\n", labelWidth, t.label+":", t)
	indent := strings.Repeat(" ", labelWidth+1)
	if !used.exists {
		printf(stdout, "%sdoes not exist (0 bytes in 0 files)\n", indent)
		return
	}
	note := ""
	if own, err := inspectOwnership(t); err != nil || own == ownershipForeign {
		note = " — not stamped by the harness, so `clean` will refuse it"
	}
	printf(stdout, "%s%s (%d bytes) in %s%s\n",
		indent, humanSize(used.bytes), used.bytes, plural(used.files, "file"), note)
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

type diskUsage struct {
	exists bool
	files  int
	bytes  int64
}

func measure(dir string) (diskUsage, error) {
	var used diskUsage
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return used, nil
	case err != nil:
		return used, err
	case !info.IsDir():
		return diskUsage{exists: true, files: 1, bytes: info.Size()}, nil
	}

	used.exists = true
	var firstFailure error
	others := 0
	note := func(err error) error {
		if firstFailure == nil {
			firstFailure = err
		} else {
			others++
		}
		return nil
	}

	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil
		case err != nil:
			return note(err)
		case entry.IsDir():
			return nil
		}
		info, err := entry.Info()
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil
		case err != nil:
			return note(fmt.Errorf("%s: %w", path, err))
		}
		used.files++
		used.bytes += info.Size()
		return nil
	})

	switch {
	case firstFailure == nil:
		return used, nil
	case others > 0:
		return used, fmt.Errorf("%w (and %d more like it)", firstFailure, others)
	default:
		return used, firstFailure
	}
}

func requireNoArguments(name string, args []string, stderr io.Writer) int {
	if len(args) == 0 {
		return exitOK
	}
	printf(stderr, "testcache: `%s` takes no arguments, and was given %q\n", name, args)
	return exitUsage
}

type target struct {
	label    string
	dir      string
	named    string
	variable string
	marker   string
}

func (t target) String() string {
	if t.dir == t.named {
		return t.dir
	}
	return t.dir + " (named as " + t.named + ")"
}

func buildCacheTarget(env map[string]string, d deps) (target, error) {
	dir, named, err := harnessDirectory(env, d, buildCacheEnv, buildCacheIn,
		"and the go command refuses a relative GOCACHE")
	return target{
		label: "build cache", dir: dir, named: named,
		variable: buildCacheEnv, marker: buildCacheMarker,
	}, err
}

func keptTarget(env map[string]string, d deps) (target, error) {
	dir, named, err := harnessDirectory(env, d, keepDirEnv, keptIn,
		"and a child that starts elsewhere would resolve it against its own working directory")
	return target{
		label: "kept scratch", dir: dir, named: named,
		variable: keepDirEnv, marker: keptMarker,
	}, err
}

func harnessDirectory(env map[string]string, d deps, variable, leaf, because string) (dir, named string, err error) {
	if value := env[variable]; value != "" {
		if !filepath.IsAbs(value) {
			return "", value, fmt.Errorf("%s=%s is not an absolute path, %s", variable, value, because)
		}
		named = filepath.Clean(value)
	} else {
		root, err := d.userCache()
		switch {
		case err != nil:
			return "", "", fmt.Errorf("this platform has no user cache directory, so %s has to name one: %w", variable, err)
		case root == "":
			return "", "", fmt.Errorf("this platform reports no user cache directory, so %s has to name one", variable)
		}
		named = filepath.Join(root, harnessDir, leaf)
	}
	dir = resolveSymlinks(named)
	if err := refuseDangerousDirectory(dir, named, variable, d); err != nil {
		return "", named, err
	}
	return dir, named, nil
}

func resolveSymlinks(dir string) string {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return dir
	}
	return resolved
}

func refuseDangerousDirectory(dir, named, variable string, d deps) error {
	spelling := dir
	if dir != named {
		spelling = dir + " (named as " + named + ")"
	}
	if dir == filepath.Dir(dir) {
		return fmt.Errorf("%s resolves to %s, which is a filesystem root: nothing this tool does to a "+
			"directory is a thing to do to one of those", variable, spelling)
	}
	if home, err := d.userHome(); err == nil && home != "" && samePath(dir, home) {
		return fmt.Errorf("%s resolves to %s, which is your home directory: this tool empties what it is "+
			"pointed at, so it refuses to be pointed there", variable, spelling)
	}
	if root, err := d.userCache(); err == nil && root != "" && samePath(dir, filepath.Join(root, buildCacheIn)) {
		return fmt.Errorf("%s resolves to %s, which is the go command's own build cache: empty that with "+
			"`go clean -cache` yourself if you mean to. This tool exists to keep the suites out of it",
			variable, spelling)
	}
	return nil
}

func samePath(a, b string) bool {
	return resolveSymlinks(filepath.Clean(a)) == resolveSymlinks(filepath.Clean(b))
}

func stampDirectory(t target) (bool, error) {
	own, inspectErr := inspectOwnership(t)
	switch {
	case inspectErr != nil:
		return false, fmt.Errorf("inspecting the %s %s: %w", t.label, t, inspectErr)
	case own == ownershipOurs:
		return true, nil
	case own == ownershipForeign:
		empty, readErr := isEmptyDirectory(t.dir)
		if readErr != nil {
			return false, fmt.Errorf("reading the %s %s: %w", t.label, t, readErr)
		}
		if !empty {
			return false, nil
		}
	}

	if mkErr := os.MkdirAll(t.dir, 0o700); mkErr != nil {
		return false, fmt.Errorf("creating the %s %s: %w", t.label, t, mkErr)
	}
	file, err := os.OpenFile(filepath.Join(t.dir, t.marker), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("stamping the %s %s: %w", t.label, t, err)
	}
	_, writeErr := file.Write(markerBody(t))
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return false, fmt.Errorf("stamping the %s %s: %w", t.label, t, err)
	}
	return true, nil
}

func isEmptyDirectory(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func markerBody(t target) []byte {
	return []byte("This directory is the go-mutants test harness's " + t.label + ".\n" +
		"It exists so that the test suites' `go` commands do not fill your own build cache.\n" +
		"`mise run test-cache-status` reports it; `mise run test-clean` empties it.\n" +
		"Nothing in it is precious: deleting it costs one recompile.\n" +
		"This file is also what tells the collector the directory is safe to remove,\n" +
		"which is why nothing removes a directory that does not have one.\n")
}

type ownership int

const (
	ownershipAbsent ownership = iota
	ownershipOurs
	ownershipForeign
)

func inspectOwnership(t target) (ownership, error) {
	info, err := os.Stat(t.dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ownershipAbsent, nil
	case err != nil:
		return ownershipForeign, err
	case !info.IsDir():
		return ownershipForeign, nil
	}
	switch _, err := os.Lstat(filepath.Join(t.dir, t.marker)); {
	case err == nil:
		return ownershipOurs, nil
	case errors.Is(err, fs.ErrNotExist):
		return ownershipForeign, nil
	default:
		return ownershipForeign, err
	}
}

func licensed(stderr io.Writer, t target) bool {
	own, err := inspectOwnership(t)
	if err != nil {
		printf(stderr, "testcache: the %s %s cannot be inspected, so nothing here will remove it: %v\n",
			t.label, t, err)
		return false
	}
	if own == ownershipForeign {
		printf(stderr, "testcache: %s is not the harness's %s: it holds no %s, so nothing here will remove "+
			"it. If it is meant to be the %s, either let a suite or `testcache exec` create it — both "+
			"write that file — or, if it predates this check, delete it once by hand and the next run "+
			"will make it again. If it is not, point %s somewhere else.\n",
			t, t.label, t.marker, t.label, t.variable)
		return false
	}
	return true
}

func goCleanCache(dir string) error {
	gobin, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("locating the go command that owns %s: %w", dir, err)
	}
	cmd := exec.Command(gobin, "clean", "-cache")
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), "GOCACHE="+dir, "GOTOOLCHAIN=local", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("`go clean -cache` with GOCACHE=%s: %w\n%s", dir, err, out)
	}
	return nil
}

var binaryUnits = []struct {
	suffix string
	scale  float64
}{
	{"KIB", 1 << 10},
	{"MIB", 1 << 20},
	{"GIB", 1 << 30},
	{"TIB", 1 << 40},
	{"B", 1},
}

func parseSize(text string) (int64, error) {
	trimmed := strings.ToUpper(strings.TrimSpace(text))
	if trimmed == "" {
		return 0, fmt.Errorf("a budget may not be empty: write it as a number of bytes or with a unit (B, KiB, MiB, GiB, TiB)")
	}

	number, scale := trimmed, float64(1)
	for _, unit := range binaryUnits {
		if rest, found := strings.CutSuffix(trimmed, unit.suffix); found {
			number, scale = strings.TrimSpace(rest), unit.scale
			break
		}
	}

	value, err := strconv.ParseFloat(number, 64)
	switch {
	case err != nil:
		return 0, fmt.Errorf("%q is not a size: write it as a number of bytes or with a unit (B, KiB, MiB, GiB, TiB); "+
			"the decimal spellings (kB, MB, GB) are refused so that a budget is never quietly smaller than it reads", text)
	case math.IsNaN(value):
		return 0, fmt.Errorf("%q is not a number at all: a budget is a number of bytes, optionally with a unit (B, KiB, MiB, GiB, TiB)", text)
	case math.IsInf(value, 0):
		return 0, fmt.Errorf("%q has no size: a budget is a number of bytes, optionally with a unit (B, KiB, MiB, GiB, TiB)", text)
	case value < 0:
		return 0, fmt.Errorf("%q is negative: a budget is a number of bytes, optionally with a unit (B, KiB, MiB, GiB, TiB)", text)
	case value >= float64(math.MaxInt64)/scale:
		return 0, fmt.Errorf("%q is larger than any directory can be (the limit is just under %d bytes, and units are B, KiB, MiB, GiB, TiB)",
			text, int64(math.MaxInt64))
	}
	return int64(value * scale), nil
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for rest := n / unit; rest >= unit; rest /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
