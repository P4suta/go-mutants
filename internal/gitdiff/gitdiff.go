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

const DefaultProgram = "git"

const DefaultTimeout = 60 * time.Second

const UpstreamRef = "@{upstream}"

type Options struct {
	Root    string
	Ref     string
	Program string
	Env     []string
	Timeout time.Duration
}

type Range struct {
	First, Last int
}

type Changed struct {
	Ref   string
	Base  string
	Files map[string][]Range
}

func (c Changed) Paths() []string {
	paths := make([]string, 0, len(c.Files))
	for path := range c.Files {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}

func (c Changed) Lines(path string) []Range { return c.Files[path] }

func (c Changed) Touches(path string, first, last int) bool {
	first, last = min(first, last), max(first, last)
	for _, r := range c.Files[path] {
		if r.First <= last && first <= r.Last {
			return true
		}
	}
	return false
}

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
	g.run = g.command
	return g.resolve(ctx, opts.Ref)
}

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

type git struct {
	program string
	dir     string
	env     []string
	timeout time.Duration
	run     func(ctx context.Context, args ...string) (string, error)
}

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
		if lines == 0 {
			continue
		}
		files[rel] = Merge(append(files[rel], Range{First: 1, Last: lines}))
	}
	return nil
}

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
	defer func() { _ = file.Close() }()

	return countLines(rel, file)
}

func countLines(rel string, r io.Reader) (int, error) {
	var counter lineCounter
	if _, err := io.Copy(&counter, r); err != nil {
		return 0, unreadable(rel, err)
	}
	return counter.total(), nil
}

type lineCounter struct {
	lines int
	last  byte
	read  bool
}

func (c *lineCounter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		c.lines += bytes.Count(p, []byte{'\n'})
		c.last, c.read = p[len(p)-1], true
	}
	return len(p), nil
}

func (c *lineCounter) total() int {
	if c.read && c.last != '\n' {
		return c.lines + 1
	}
	return c.lines
}

func unreadable(rel string, err error) error {
	return &Error{
		Code: CodeUntrackedUnreadable,
		Message: "the untracked file " + strconv.Quote(rel) + " could not be read, so --changed cannot tell " +
			"which of its lines are new",
		Err: err,
	}
}

func (g git) command(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	argv := append([]string{"-C", g.dir}, args...)
	cmd := exec.CommandContext(ctx, g.program, argv...)
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
	if ctx.Err() == nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", &Error{
				Code:    CodeDiffFailed,
				Message: "`git " + strings.Join(args, " ") + "` exited with status " + strconv.Itoa(exitErr.ExitCode()),
				Output:  trimOutput(stderr.String()),
				Err:     err,
			}
		}
	}
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

const outputLines = 20

func trimOutput(s string) string {
	text := strings.TrimRight(s, "\r\n \t")
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	lines = lines[max(0, len(lines)-outputLines):]
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return strings.Join(lines, "\n")
}

func shortHash(hash string) string {
	const width = 12
	return hash[:min(len(hash), width)]
}

func or(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
