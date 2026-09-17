// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/mutation"
)

type Mode int

const (
	ModeMutant Mode = iota
	ModeProbe
)

type Options struct {
	SnapshotRoot string

	ModulePath string

	Catalog *mutation.Catalog

	Hints Hints

	Module string

	Mode Mode
}

type Result struct {
	RuntimeDir        string
	RuntimeImport     string
	ModulePath        string
	FilesInstrumented []string
	GuardsByFile      map[string]int
	LoopBase          map[string]uint32
	Loops             int
}

func Instrument(opts Options) (Result, error) {
	if err := opts.validate(); err != nil {
		return Result{}, err
	}

	dir, err := chooseRuntimeDir(opts.SnapshotRoot)
	if err != nil {
		return Result{}, err
	}
	importPath := opts.ModulePath + "/" + dir

	result := Result{
		RuntimeDir:    dir,
		RuntimeImport: importPath,
		ModulePath:    opts.ModulePath,
		GuardsByFile:  make(map[string]int),
		LoopBase:      make(map[string]uint32),
	}

	names := newPackageNames()
	var loops []loopSite
	for _, group := range groupByPath(opts.Catalog, opts.Module) {
		guards, counted, err := instrumentFile(
			opts.SnapshotRoot, group.path, group.mutants, opts.Hints, importPath, names, opts.Mode,
			uint32(len(loops)))
		if err != nil {
			return Result{}, err
		}
		result.LoopBase[group.path] = uint32(len(loops))
		loops = append(loops, counted...)
		if guards == 0 {
			continue
		}
		result.FilesInstrumented = append(result.FilesInstrumented, group.path)
		result.GuardsByFile[group.path] = guards
	}
	result.Loops = len(loops)

	if err := writeTreeRuntime(
		opts.SnapshotRoot, dir, opts.ModulePath, opts.Catalog, opts.Mode, loops); err != nil {
		return Result{}, err
	}
	return result, nil
}

func writeTreeRuntime(
	root, dir, modulePath string, catalog *mutation.Catalog, mode Mode, loops []loopSite,
) error {
	if mode == ModeProbe {
		return writeProbeRuntime(root, dir, catalog)
	}
	return writeRuntime(root, dir, modulePath, catalog, loops)
}

func (o Options) validate() error {
	if strings.TrimSpace(o.SnapshotRoot) == "" {
		return &Error{Code: CodeOptions, Message: "no snapshot root was given"}
	}
	info, err := os.Stat(o.SnapshotRoot)
	if err != nil {
		return &Error{
			Code:    CodeOptions,
			Message: "cannot read the snapshot root " + strconv.Quote(o.SnapshotRoot),
			Err:     err,
		}
	}
	if !info.IsDir() {
		return &Error{
			Code:    CodeOptions,
			Message: "the snapshot root " + strconv.Quote(o.SnapshotRoot) + " is not a directory",
		}
	}
	if strings.TrimSpace(o.ModulePath) == "" {
		return &Error{Code: CodeOptions, Message: "no module path was given"}
	}
	if o.Catalog == nil {
		return &Error{Code: CodeOptions, Message: "no catalogue was given"}
	}
	if first, ok := o.Catalog.At(0); ok {
		switch {
		case first.ModulePath != "" && o.Module == "":
			return &Error{
				Code: CodeOptions,
				Message: "the catalogue names the module " + strconv.Quote(first.ModulePath) +
					" and no module was given to instrument",
			}
		case first.ModulePath == "" && o.Module != "":
			return &Error{
				Code: CodeOptions,
				Message: "the module " + strconv.Quote(o.Module) +
					" was given and the catalogue names no module",
			}
		}
	}
	if o.Mode != ModeMutant && o.Mode != ModeProbe {
		return &Error{
			Code:    CodeOptions,
			Message: "the instrumentation mode " + strconv.Itoa(int(o.Mode)) + " is not one this package knows",
		}
	}
	for _, m := range o.Catalog.Mutants() {
		if !insideSnapshot(m.Path) {
			return &Error{
				Code: CodeOptions,
				Message: "the catalogue names " + strconv.Quote(m.Path) +
					", which is not a module-relative path inside the snapshot",
			}
		}
	}
	return nil
}

func insideSnapshot(p string) bool {
	if p == "" || strings.ContainsRune(p, '\\') || strings.ContainsRune(p, 0) {
		return false
	}
	if path.IsAbs(p) || filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
		return false
	}
	clean := path.Clean(p)
	return clean != ".." && !strings.HasPrefix(clean, "../") && clean != "."
}

type fileGroup struct {
	path    string
	mutants []mutation.Mutant
}

func groupByPath(catalog *mutation.Catalog, module string) []fileGroup {
	mutants := catalog.Mutants()
	byPath := make(map[string][]mutation.Mutant, len(mutants))
	paths := make([]string, 0, len(mutants))
	for _, m := range mutants {
		if m.ModulePath != module {
			continue
		}
		if _, seen := byPath[m.Path]; !seen {
			paths = append(paths, m.Path)
		}
		byPath[m.Path] = append(byPath[m.Path], m)
	}
	slices.Sort(paths)

	groups := make([]fileGroup, 0, len(paths))
	for _, p := range paths {
		groups = append(groups, fileGroup{path: p, mutants: byPath[p]})
	}
	return groups
}

func instrumentFile(
	root, srcPath string,
	mutants []mutation.Mutant,
	hints Hints,
	importPath string,
	names *packageNames,
	mode Mode,
	base uint32,
) (int, []loopSite, error) {
	file := filepath.Join(root, filepath.FromSlash(srcPath))
	info, err := os.Stat(file)
	if err != nil {
		return 0, nil, &Error{
			Code:    CodeSourceUnreadable,
			Message: "cannot read " + strconv.Quote(srcPath) + " in the snapshot",
			Err:     err,
		}
	}
	src, err := os.ReadFile(file)
	if err != nil {
		return 0, nil, &Error{
			Code:    CodeSourceUnreadable,
			Message: "cannot read " + strconv.Quote(srcPath) + " in the snapshot",
			Err:     err,
		}
	}

	dir := filepath.Dir(file)
	reserved := func(pkg string) (map[string]bool, error) { return names.namesIn(dir, pkg) }

	out, guards, loops, err := instrumentSource(srcPath, src, mutants, hints, importPath, reserved, mode, base)
	if err != nil {
		return 0, nil, err
	}
	if guards == 0 {
		return 0, nil, nil
	}
	if err := replaceFile(file, out, info.Mode().Perm()); err != nil {
		return 0, nil, &Error{
			Code:    CodeWriteFailed,
			Message: "cannot write the instrumented " + strconv.Quote(srcPath),
			Err:     err,
		}
	}
	return guards, loops, nil
}

func replaceFile(file string, out []byte, perm fs.FileMode) error {
	dir := filepath.Dir(file)
	tmp, err := os.CreateTemp(dir, ".gomutants-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()

	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Rename(name, file); err != nil {
		if chmodErr := os.Chmod(file, 0o600); chmodErr != nil {
			return err
		}
		if retry := os.Rename(name, file); retry != nil {
			return err
		}
	}

	return os.Chmod(file, perm)
}

type reservedNames func(pkg string) (map[string]bool, error)

func instrumentSource(
	srcPath string,
	src []byte,
	mutants []mutation.Mutant,
	hints Hints,
	importPath string,
	reserved reservedNames,
	mode Mode,
	base uint32,
) ([]byte, int, []loopSite, error) {
	if len(mutants) == 0 {
		return src, 0, nil, nil
	}
	file, tok, err := parseSnapshotFile(srcPath, src)
	if err != nil {
		return nil, 0, nil, err
	}

	var bound map[string]bool
	if reserved != nil && file.Name != nil {
		if bound, err = reserved(file.Name.Name); err != nil {
			return nil, 0, nil, err
		}
	}
	taken := takenNames(file, bound)
	alias := aliasIn(taken)

	var loops []loopSite
	if mode == ModeMutant {
		loops = loopSites(file, tok, srcPath)
	}

	loopEdits := loopSplices(loops, alias, base)
	splices, guards, completions, err := composeSites(
		newSiteIndex(tok, file, src), srcPath, src, mutants, hints, alias, taken, mode, loopEdits)
	if err != nil {
		return nil, 0, nil, err
	}
	if guards == 0 {
		return src, 0, loops, nil
	}
	splices = append(splices, loopsOutsideSites(loopEdits, splices)...)
	imports, err := importSplices(file, tok, srcPath, alias, importPath, completions)
	if err != nil {
		return nil, 0, nil, err
	}
	splices = append(splices, imports...)

	if !LinePreserving(splices) {
		return nil, 0, nil, &Error{
			Code: CodeLineDrift,
			Message: "internal error: instrumenting " + strconv.Quote(srcPath) +
				" would move a line: a guard or the injected import does not replace as many line breaks as it writes",
		}
	}
	out, _, err := Apply(src, splices)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("instrumenting %s: %w", strconv.Quote(srcPath), err)
	}

	if err := checkLineCount(srcPath, src, out); err != nil {
		return nil, 0, nil, err
	}
	if err := checkParses(srcPath, out); err != nil {
		return nil, 0, nil, err
	}
	return out, guards, loops, nil
}

func composeSites(
	index *siteIndex,
	srcPath string,
	src []byte,
	mutants []mutation.Mutant,
	hints Hints,
	alias string,
	taken map[string]bool,
	mode Mode,
	loops []Splice,
) ([]Splice, int, []discover.Completion, error) {
	if mode == ModeProbe {
		forest, sites, edits, widest, completions, err := buildProbeSites(index, srcPath, mutants, hints)
		if err != nil {
			return nil, 0, nil, err
		}
		renderer := &probeRenderer{
			path:  srcPath,
			src:   src,
			alias: alias,
			temps: newProbeTemps(alias, taken, widest),
			sites: sites,
			edits: edits,
		}
		splices, guards, err := renderer.render(forest)
		return splices, guards, completions, err
	}

	forest, sites, completions, err := buildSites(index, srcPath, mutants, hints)
	if err != nil {
		return nil, 0, nil, err
	}
	renderer := &guardRenderer{path: srcPath, src: src, alias: alias, sites: sites, loops: loops}
	splices, guards, err := renderer.render(forest)
	return splices, guards, completions, err
}

func checkLineCount(srcPath string, src, out []byte) error {
	got, want := CountLines(out), CountLines(src)
	if got == want {
		return nil
	}
	return &Error{
		Code: CodeLineDrift,
		Message: "internal error: the instrumented " + strconv.Quote(srcPath) + " holds " +
			strconv.Itoa(got) + " line breaks, the original holds " + strconv.Itoa(want),
	}
}

func loopsOutsideSites(loops, sites []Splice) []Splice {
	if len(loops) == 0 {
		return nil
	}
	out := make([]Splice, 0, len(loops))
	for _, loop := range loops {
		at := loop.Span.StartByte
		claimed := false
		for _, site := range sites {
			if at > site.Span.StartByte && at < site.Span.EndByte {
				claimed = true
				break
			}
		}
		if !claimed {
			out = append(out, loop)
		}
	}
	return out
}
