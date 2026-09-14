// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gitdiff

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultProgram is the git executable, resolved through PATH.
const DefaultProgram = "git"

// DefaultTimeout bounds one git invocation.
//
// Every command this package runs is local and answers in milliseconds on any
// repository a person works in, so the budget is not a performance figure: it
// is the wait before concluding that git is stuck — on a lock, on a network
// filesystem, on a credential prompt that should never have appeared — rather
// than slow. A `--changed` run that hangs forever inside a selection decision
// would be the worst of both worlds, since nothing has been measured yet.
const DefaultTimeout = 60 * time.Second

// UpstreamRef is the revision a bare `--changed` resolves: the upstream branch
// of HEAD, in git's own notation.
//
// It is a request rather than a name. [Options.Ref] holding it means exactly
// what holding nothing means — resolve the upstream of HEAD, and record the
// branch it turns out to be — because it is the value internal/cli gives the
// bare flag, and a user who writes it out longhand asked the same question.
const UpstreamRef = "@{upstream}"

// Options is everything [Resolve] needs.
type Options struct {
	// Root is the workspace root, and the directory every git command runs in.
	// It is the user's own tree rather than a snapshot; see the package
	// documentation.
	Root string
	// Ref is the revision to compare against. Empty — or [UpstreamRef], which
	// is the same request written out — resolves the upstream branch of HEAD,
	// and fails with [CodeNoUpstream] when there is none.
	Ref string
	// Program is the git executable. Empty is [DefaultProgram].
	Program string
	// Env is the environment git runs with, in "KEY=VALUE" form. Nil inherits
	// this process's, which is what every real run wants: a user's git
	// configuration governs their own repository, and a `--changed` that read a
	// different configuration from the `git diff` they would have typed would be
	// answering a different question. The tests set it so that a scripted
	// repository is independent of the developer's own configuration.
	//
	// The invocation overrides the few settings that would break the parser
	// whatever this says; see [git.diff].
	Env []string
	// Timeout bounds each invocation. Zero is [DefaultTimeout].
	Timeout time.Duration
}

// A Range is an inclusive, 1-based run of lines in one file.
type Range struct {
	// First and Last are line numbers, and First is never above Last.
	First, Last int
}

// Changed is the changed-line set of one workspace against one ref.
//
// The zero value touches nothing, which is the right answer to "which lines
// changed" for a caller that never asked — and never the value [Resolve]
// returns on a failure, because a failure is an error there rather than an
// empty set.
type Changed struct {
	// Ref is the revision the base was resolved from, as it will be recorded in
	// the report: the ref the user named, or the upstream branch's own name
	// when they named none. It is the branch name rather than `@{upstream}`,
	// because a report saying `@{upstream}` documents a lookup instead of a
	// comparison.
	Ref string
	// Base is the full commit hash of the merge base that was diffed against.
	Base string
	// Files maps a workspace-relative, '/'-separated path onto the lines that
	// changed in it, as a sorted list of non-overlapping ranges. A file with no
	// changed lines is absent rather than present and empty.
	Files map[string][]Range
}

// Paths returns every changed file, sorted.
func (c Changed) Paths() []string {
	paths := make([]string, 0, len(c.Files))
	for path := range c.Files {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}

// Lines returns the changed ranges of one file, or nil.
func (c Changed) Lines(path string) []Range { return c.Files[path] }

// Touches reports whether any changed line of path falls inside [first, last].
//
// The comparison is by line and never by column, which is the same
// approximation coverage mapping makes and for the same reason: an edit is
// recorded as whole lines, so asking anything finer would be inventing
// precision the input does not have. A mutant spanning a line somebody touched
// is selected even if the touched part was a comment at the end of it —
// selecting one mutant too many costs time, and missing one costs a finding.
func (c Changed) Touches(path string, first, last int) bool {
	// Normalised rather than compared-and-swapped: a caller naming one line
	// passes the same number twice, and `>` and `>=` over a pair that is
	// already in order are the same swap of nothing at all.
	first, last = min(first, last), max(first, last)
	for _, r := range c.Files[path] {
		if r.First <= last && first <= r.Last {
			return true
		}
	}
	return false
}

// Resolve finds the changed lines of the workspace against a ref.
//
// The sequence is: prove the workspace is inside a working tree and learn where
// in it, resolve the ref, take the merge base with HEAD, diff the working tree
// against that base with no context lines, and add the files git has not been
// told about. Every step's failure has its own code, so a user never has to
// guess which one went wrong.
func Resolve(ctx context.Context, opts Options) (Changed, error) {
	g := git{
		program: or(opts.Program, DefaultProgram),
		dir:     opts.Root,
		env:     opts.Env,
		timeout: opts.Timeout,
	}
	if g.timeout <= 0 {
		g.timeout = DefaultTimeout
	}
	// The seam is closed here and nowhere else: a real run starts real git
	// commands, and the value is taken after every field it reads is set.
	g.run = g.command
	return g.resolve(ctx, opts.Ref)
}

// resolve is [Resolve] once the commands have a way to be run.
func (g git) resolve(ctx context.Context, wanted string) (Changed, error) {
	prefix, err := g.prefix(ctx)
	if err != nil {
		return Changed{}, err
	}
	ref, err := g.resolveRef(ctx, wanted)
	if err != nil {
		return Changed{}, err
	}
	base, err := g.mergeBase(ctx, ref)
	if err != nil {
		return Changed{}, err
	}
	out, err := g.diff(ctx, base)
	if err != nil {
		return Changed{}, err
	}
	files, err := parseDiff(out, prefix)
	if err != nil {
		return Changed{}, err
	}
	if err = g.addUntracked(ctx, files, prefix); err != nil {
		return Changed{}, err
	}
	return Changed{Ref: ref, Base: base, Files: files}, nil
}

// A git runs one repository's commands.
type git struct {
	program string
	dir     string
	env     []string
	timeout time.Duration
	// run is how a command is run, and it is a field so that the failures git
	// will not produce on demand can be produced anyway.
	//
	// The tests of this package drive a real git, because every interesting
	// thing about *reading* git is what git actually prints and a stand-in
	// would be a second implementation of the thing under test. That argument
	// does not reach the other half of this package: which diagnostic a user
	// sees when `git ls-files` exits non-zero, when the diff command fails
	// after the merge base succeeded, or when one sub-command is missing while
	// the rest answer, is a fact about go-mutants' own code, and there is no
	// repository state that produces some of those at all. Scripting them is
	// the same trade internal/gocmd made when it left the toolchain allowlist.
	//
	// [Resolve] sets it to [git.command] and nothing else in a real run does.
	run func(ctx context.Context, args ...string) (string, error)
}

// prefix returns the workspace's path within the repository, '/'-separated and
// either empty or ending in a slash. It is also the proof that there is a
// repository at all.
func (g git) prefix(ctx context.Context) (string, error) {
	out, err := g.run(ctx, "rev-parse", "--show-prefix")
	if err != nil {
		var failure *Error
		if errors.As(err, &failure) && failure.Code == CodeGitUnavailable {
			return "", err
		}
		return "", &Error{
			Code: CodeNotARepository,
			Message: "the workspace is not inside a git working tree, so there is no diff to select by; " +
				"drop --changed, or run go-mutants inside the repository",
			Output: OutputOf(err),
			Err:    err,
		}
	}
	return strings.TrimSpace(out), nil
}

// resolveRef turns the user's ref — or their silence — into a name to compare
// against.
//
// A named ref is passed through unchecked: whether it exists is what the merge
// base is about to find out, and asking twice would produce two different
// messages for one mistake. Silence resolves the upstream branch by its own
// name, so that the report records `origin/main` rather than a notation.
//
// [UpstreamRef] is silence rather than a name, and that is the whole of the
// condition below. It is the value the bare `--changed` flag carries, so
// passing it through would mean no invocation of the flag ever reached this
// lookup: a report would record `@{upstream}` — documenting how the base was
// found instead of what it was — a branch with no upstream would fail at the
// merge base talking about a ref nobody typed rather than with
// [CodeNoUpstream], and two shards diffing different upstreams would both write
// the same string and pass a congruence check they should fail. It also makes
// the two spellings one behaviour, which is what internal/cli's default
// promises.
//
// Exactly that one string is special, and git's other ways of naming an
// upstream — `@{u}`, `main@{upstream}`, `@{upstream}^` — are refs like any
// other: they resolve, and they are recorded as written. This is about the
// value the flag produces, not about revision syntax, and a resolver that
// started interpreting git's grammar would be a second and worse
// implementation of it.
func (g git) resolveRef(ctx context.Context, ref string) (string, error) {
	if named := strings.TrimSpace(ref); named != "" && named != UpstreamRef {
		return ref, nil
	}
	out, err := g.run(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", UpstreamRef)
	if err != nil {
		var failure *Error
		if errors.As(err, &failure) && failure.Code == CodeGitUnavailable {
			return "", err
		}
		return "", &Error{
			Code: CodeNoUpstream,
			Message: "--changed asked for the upstream of this branch and there is none to fall back on, " +
				"so there is nothing to compare against; name the ref, as in `--changed=origin/main`, " +
				"or set an upstream with `git branch --set-upstream-to=origin/main`",
			Output: OutputOf(err),
			Err:    err,
		}
	}
	name := strings.TrimSpace(out)
	if name == "" {
		return "", &Error{
			Code:    CodeNoUpstream,
			Message: "git named no upstream branch for HEAD; name the ref instead, as in `--changed=origin/main`",
		}
	}
	return name, nil
}

// mergeBase returns the commit the branch left: the best common ancestor of ref
// and HEAD.
//
// The failure path asks one extra question, because "there is no merge base"
// has two very different causes and only one of them is about the ref. A
// repository with no commits at all cannot answer any comparison, and telling
// somebody their ref is unknown when the truth is that they have not committed
// anything would send them looking in the wrong place.
func (g git) mergeBase(ctx context.Context, ref string) (string, error) {
	out, err := g.run(ctx, "merge-base", ref, "HEAD")
	if err == nil {
		base := strings.TrimSpace(out)
		if base != "" {
			return base, nil
		}
		return "", &Error{
			Code:    CodeUnknownRef,
			Message: "git named no merge base for " + strconv.Quote(ref) + " and HEAD",
		}
	}
	if code := CodeOf(err); code == CodeGitUnavailable {
		return "", err
	}
	if _, headErr := g.run(ctx, "rev-parse", "--verify", "--quiet", "HEAD"); headErr != nil {
		return "", &Error{
			Code: CodeUnknownRef,
			Message: "this repository has no commits yet, so there is nothing for --changed to compare against; " +
				"commit first, or drop --changed to run every mutant",
			Output: OutputOf(headErr),
			Err:    headErr,
		}
	}
	return "", &Error{
		Code: CodeUnknownRef,
		Message: "git cannot find a merge base for " + strconv.Quote(ref) + " and HEAD: " +
			"the ref may not exist here, or the two may share no history",
		Output: OutputOf(err),
		Err:    err,
	}
}

// diff renders the working tree against the base with no context lines.
//
// Every flag is a defence against a configuration this repository's owner is
// entitled to have and this parser cannot read:
//
//   - `--src-prefix`/`--dst-prefix` pin the `a/` and `b/` the header parser
//     depends on, which `diff.noprefix` and `diff.mnemonicPrefix` would
//     otherwise remove or rename.
//   - `core.quotePath=false` stops non-ASCII paths coming back C-escaped.
//   - `--no-renames` is required rather than defensive: `diff.renames` is on by
//     default, and a rename reported as a rename carries no hunks for the lines
//     of the new file.
//   - `--no-ext-diff` and `--no-textconv` keep a configured external differ or
//     a textconv filter from replacing the unified diff with something else
//     entirely.
//   - `-U0` is what makes a hunk header the changed lines exactly, with no
//     context to subtract.
//
// The pathspec is the working directory, so a module inside a larger repository
// is diffed against its own subtree; [parseDiff] maps what comes back onto
// module-relative paths.
func (g git) diff(ctx context.Context, base string) (string, error) {
	out, err := g.run(ctx,
		"-c", "core.quotePath=false",
		"diff",
		"--src-prefix=a/", "--dst-prefix=b/",
		"--no-renames", "--no-color", "--no-ext-diff", "--no-textconv",
		"-U0", base, "--", ".",
	)
	if err == nil {
		return out, nil
	}
	if code := CodeOf(err); code == CodeGitUnavailable {
		return "", err
	}
	return "", &Error{
		Code:    CodeDiffFailed,
		Message: "`git diff` against " + shortHash(base) + " failed",
		Output:  OutputOf(err),
		Err:     err,
	}
}

// addUntracked folds the files git has not been told about into the changed
// set, each as the whole of itself.
//
// `git diff` cannot see one at all: a file with no index entry has nothing to
// be diffed against, so it produces no hunks however new it is. Selecting from
// the diff alone would stop halfway across a boundary this feature has already
// crossed — an edited line of a tracked file selects its mutants, and a file
// written from scratch selects none of them — which is an afternoon's work
// reported as `score N/A (0 valid mutants)` and exit 0. That is the failure
// `--changed` fails closed everywhere else to avoid.
//
// Every line of a file git does not have is new, so this is not an
// approximation; it is the answer `git diff` gives the moment the file is
// added. Where it can be generous is the file somebody wrote last week and
// still has not staged, which is the same direction `--no-renames` is generous
// in: a mutant too many costs time, and a mutant missed costs a finding.
func (g git) addUntracked(ctx context.Context, files map[string][]Range, prefix string) error {
	paths, err := g.untracked(ctx)
	if err != nil {
		return err
	}
	for _, path := range paths {
		rel := relative(path, prefix)
		if rel == "" {
			continue
		}
		lines, countErr := lineCount(g.dir, rel)
		if countErr != nil {
			return countErr
		}
		// An empty file has no lines to mutate, and a file with no changed
		// lines is absent rather than present and empty; see [Changed.Files].
		if lines == 0 {
			continue
		}
		files[rel] = Merge(append(files[rel], Range{First: 1, Last: lines}))
	}
	return nil
}

// untracked lists the workspace's untracked, unignored files as
// repository-relative paths.
//
// `--exclude-standard` is what keeps the list to work rather than to output: a
// file the repository ignores is one its owner has said is not theirs to read,
// and selecting mutants inside a generated tree would be the opposite of
// narrowing. `--full-name` pins the paths to the repository root so that
// [relative] maps them exactly as it maps the diff's — without it they would
// arrive relative to the workspace and be mapped twice. `-z` takes quoting out
// of the question entirely: a NUL-separated list has nothing to escape, so a
// path with a quotation mark in it needs no decoding here.
func (g git) untracked(ctx context.Context) ([]string, error) {
	out, err := g.run(ctx, "ls-files", "--others", "--exclude-standard", "--full-name", "-z", "--", ".")
	if err != nil {
		if code := CodeOf(err); code == CodeGitUnavailable {
			return nil, err
		}
		return nil, &Error{
			Code: CodeUntrackedUnreadable,
			Message: "`git ls-files` could not list the untracked files, so --changed cannot tell " +
				"whether a new file is missing from the diff",
			Output: OutputOf(err),
			Err:    err,
		}
	}
	paths := strings.Split(out, "\x00")
	return slices.DeleteFunc(paths, func(path string) bool { return path == "" }), nil
}

// lineCount counts the lines of one file under root, which for an untracked
// file is how many of its lines are new.
//
// Counting rather than declaring the whole file changed with a sentinel range
// is what makes an untracked file and a committed one describe the same lines:
// `git add` must not change which mutants a run selects, and a range that no
// diff could ever produce would be a second answer to a question this package
// already answers one way. A last line with no newline after it counts, which
// is git's own rule for the same text.
//
// Only regular files are read. A named pipe would block this read until
// somebody wrote to it, and a symbolic link is something internal/snapshot
// refuses outright — so a link's line count could never reach a mutant. A file
// that vanished between the listing and the read is gone rather than
// unreadable: there is nothing left in it to mutate.
func lineCount(root, rel string) (int, error) {
	path := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, unreadable(rel, err)
	}
	if !info.Mode().IsRegular() {
		return 0, nil
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, unreadable(rel, err)
	}
	// Nothing was written, so there is nothing a close can fail to flush.
	defer func() { _ = file.Close() }()

	return countLines(rel, file)
}

// countLines counts the lines a reader carries, naming the file in the failure
// so that a user reads the path git gave rather than a descriptor.
//
// io.Copy owns the loop, and that is the point of writing it this way. A
// hand-rolled read loop has to decide for itself when to stop, which makes its
// stopping condition an edit away from a program that never returns -- and a
// mutant that never returns costs this repository's own gate a whole per-mutant
// timeout, twice, to say what the loop's shape already says. What is left is
// the counting, which is a pure function of the bytes and is tested as one.
// The buffer goes with the loop: its size was a syscall-size choice, and the
// standard library makes the same kind of choice.
func countLines(rel string, r io.Reader) (int, error) {
	var counter lineCounter
	if _, err := io.Copy(&counter, r); err != nil {
		return 0, unreadable(rel, err)
	}
	return counter.total(), nil
}

// A lineCounter counts newlines as bytes go past, and remembers whether the
// last byte it saw was one.
//
// It is a writer rather than a loop so that [io.Copy] can own the reading; see
// [lineCount]. Nothing is kept but the tally, so a file of any size costs the
// same.
type lineCounter struct {
	lines int
	last  byte
	read  bool
}

// Write counts one chunk. It never fails and never reports a short write,
// because there is nothing here for either to mean.
func (c *lineCounter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		c.lines += bytes.Count(p, []byte{'\n'})
		c.last, c.read = p[len(p)-1], true
	}
	return len(p), nil
}

// total is the line count, which is the newline count plus a last line that
// nobody terminated.
//
// A file with no bytes at all has no lines, which is the difference this
// distinguishes from a file holding one unterminated line: the first is a file
// with nothing in it to mutate and the second is a file with one line in it.
// git counts the same text the same way.
func (c *lineCounter) total() int {
	if c.read && c.last != '\n' {
		return c.lines + 1
	}
	return c.lines
}

// unreadable reports a file git named and this process could not read.
//
// It is an error rather than a skip for the reason every other failure in this
// package is one: a `--changed` run that quietly left a file out of the
// selection would report a score for work it never measured.
func unreadable(rel string, err error) error {
	return &Error{
		Code: CodeUntrackedUnreadable,
		Message: "the untracked file " + strconv.Quote(rel) + " could not be read, so --changed cannot tell " +
			"which of its lines are new",
		Err: err,
	}
}

// command executes one git command and returns its standard output.
//
// The streams are kept apart rather than combined, which is why this does not
// go through internal/runner: that package supervises a process tree and
// merges the two streams, which is exactly right for a test binary whose output
// is one story, and exactly wrong here — a `warning:` on standard error would
// end up inside the commit hash this parses.
//
// A failure to *start* git is [CodeGitUnavailable] and is the one code that
// travels unchanged through every caller. A git that ran and refused is
// reported by the caller instead, which is the only place that knows what the
// question was.
func (g git) command(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	argv := append([]string{"-C", g.dir}, args...)
	cmd := exec.CommandContext(ctx, g.program, argv...)
	// The caller's environment, plus the two variables that keep a local,
	// read-only command from waiting for a human: a pager holding the pipe open
	// and a credential prompt on a terminal nobody is watching.
	base := g.env
	if base == nil {
		base = os.Environ()
	}
	cmd.Env = append(slices.Clone(base), "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return stdout.String(), nil
	}
	// Whose answer this is comes before what the answer was, and the two
	// questions are nested rather than joined: a child the context killed
	// reports an exit status exactly as a child that refused does, so reading
	// the status first would call an interrupted run a broken repository. Asked
	// this way round there is one reading of each question -- a conjunction of
	// them has a second that no input can tell from the one that is meant.
	if ctx.Err() == nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// git ran and said no. What that means is the caller's to name.
			return "", &Error{
				Code:    CodeDiffFailed,
				Message: "`git " + strings.Join(args, " ") + "` exited with status " + strconv.Itoa(exitErr.ExitCode()),
				Output:  trimOutput(stderr.String()),
				Err:     err,
			}
		}
	}
	// A cancelled run is the caller's cancellation, not a broken git. Cause is
	// nil for a context that is not done, so this asks nothing of a run that
	// simply failed.
	if cause := context.Cause(ctx); errors.Is(cause, context.Canceled) {
		return "", &Error{
			Code:    CodeGitUnavailable,
			Message: "`git " + strings.Join(args, " ") + "` was interrupted",
			Output:  trimOutput(stderr.String()),
			Err:     cause,
		}
	}
	return "", &Error{
		Code: CodeGitUnavailable,
		Message: "`git " + strings.Join(args, " ") + "` could not be run; " +
			"--changed needs git on PATH and a repository to read",
		Output: trimOutput(stderr.String()),
		Err:    err,
	}
}

// outputLines is how many trailing lines of git's own output an error keeps.
// git is terse, so this is generous rather than a real limit.
const outputLines = 20

// trimOutput reduces git's standard error to what is worth printing.
func trimOutput(s string) string {
	text := strings.TrimRight(s, "\r\n \t")
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	// The last lines, and all of them when there are fewer: a command that
	// failed says why on its way out. Written as one slice rather than as a
	// guarded one because `>` and `>=` pick the same lines at exactly the
	// limit -- `lines[0:]` is the whole of it either way -- so the guard would
	// have a second reading no output could tell from the first.
	lines = lines[max(0, len(lines)-outputLines):]
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return strings.Join(lines, "\n")
}

// shortHash abbreviates a commit for a message, and leaves anything that is not
// one alone rather than slicing past its end.
//
// One slice rather than a guard and a slice, for [trimOutput]'s reason: a hash
// of exactly the width is the same string cut or uncut, so the comparison that
// chose between them had two readings and one answer.
func shortHash(hash string) string {
	const width = 12
	return hash[:min(len(hash), width)]
}

// or returns value, or fallback when value is empty.
func or(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
