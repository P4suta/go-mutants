// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"errors"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// runtimeDirBase is the directory the generated activation package is written
// to, relative to the snapshot root.
//
// It may not begin with "_" or ".": the go tool ignores such directories
// outright, so a package hidden in one would never be built and every
// instrumented file would fail to resolve its import. The name is otherwise
// chosen to be recognisable in a stack trace and unlikely to exist already —
// and when it does exist, [chooseRuntimeDir] bumps it rather than writing into
// somebody else's directory.
const runtimeDirBase = "gomutants_rt"

// runtimeDirLimit bounds how many bumped names are tried before giving up. A
// tree holding a thousand directories named after this tool is not a collision,
// it is a sign that something is generating them in a loop.
const runtimeDirLimit = 1000

// ActiveEnv is the environment variable the generated runtime reads to decide
// which mutant is live in a process.
//
// Empty or unset means every mutant is dormant, which is the instrumented
// baseline: the tree carries all its guards and behaves exactly like the
// pristine one. The runner sets it to one full mutant ID per test process.
const ActiveEnv = "GO_MUTANTS_ACTIVE"

// UnknownMutantExit is the status the generated runtime exits with when
// [ActiveEnv] names a mutant the instrumented tree does not contain.
//
// It is deliberately not a test failure. A stale catalogue activating nothing
// would let a run report "survived" for mutants that were never live — the one
// failure mode that silently inflates a mutation score — so the process refuses
// to start instead, and the runner treats the status as an infrastructure
// error rather than as a killed mutant.
const UnknownMutantExit = 97

// ProbeEnv is the environment variable the generated probe runtime reads to
// decide where to append its infection log.
//
// Empty or unset is an ordinary run: the runtime is linked in and records
// nothing, which is what every process that is not being probed wants. It
// shares the GO_MUTANTS_ prefix internal/execute strips from a test process's
// environment and refuses in a user overlay, deliberately — like [ActiveEnv]
// this is go-mutants' variable to set and nobody else's, and a value left over
// in a developer's shell must not turn their ordinary test run into a probe.
const ProbeEnv = "GO_MUTANTS_PROBE"

// ProbeUnavailableExit is the status the generated probe runtime exits with
// when the log [ProbeEnv] names cannot be opened or written.
//
// It is not a test failure, for the same reason [UnknownMutantExit] is not. An
// empty log reads exactly like a run in which no site was ever infected, and
// that reading is what licenses a consumer to skip executions — so a probe
// process that cannot record what it saw refuses to run at all, with a status
// the runner can tell apart from a red suite. Silence is the one lie a probe
// must never tell.
const ProbeUnavailableExit = 98

// chooseRuntimeDir returns the snapshot-relative directory name the runtime
// package will be generated into.
//
// A name already present in the snapshot is bumped rather than merged into or
// overwritten: the snapshot is a copy of somebody's repository, and a directory
// called gomutants_rt in it belongs to them until proven otherwise. The bumped
// name is a pure function of what is on disk, so two runs over the same tree
// agree.
func chooseRuntimeDir(root string) (string, error) {
	for n := 0; n < runtimeDirLimit; n++ {
		name := runtimeDirBase
		if n > 0 {
			name += strconv.Itoa(n)
		}
		_, err := os.Lstat(filepath.Join(root, name))
		if errors.Is(err, fs.ErrNotExist) {
			return name, nil
		}
		if err != nil {
			return "", &Error{
				Code:    CodeWriteFailed,
				Message: "cannot check whether " + strconv.Quote(name) + " is free in the snapshot",
				Err:     err,
			}
		}
	}
	return "", &Error{
		Code: CodeWriteFailed,
		Message: "the snapshot already holds " + strconv.Itoa(runtimeDirLimit) +
			" directories named " + strconv.Quote(runtimeDirBase) + " and its bumped variants",
	}
}

// writeRuntime generates the activation package into the snapshot.
func writeRuntime(root, dir string, catalog *mutation.Catalog) error {
	source, err := renderRuntime(dir, catalog)
	if err != nil {
		return err
	}
	return writeGeneratedPackage(root, dir, source)
}

// writeProbeRuntime generates the probe package into the snapshot. It is the
// mutant tree's [writeRuntime] with the other generator, because which package
// goes into the snapshot is the only thing the two trees disagree about here:
// the directory was chosen the same way, and it is written to disk the same
// way, into a snapshot of its own.
func writeProbeRuntime(root, dir string, catalog *mutation.Catalog) error {
	source, err := renderProbeRuntime(dir, catalog)
	if err != nil {
		return err
	}
	return writeGeneratedPackage(root, dir, source)
}

// writeGeneratedPackage writes one rendered runtime package into the snapshot,
// as the single file its directory holds.
func writeGeneratedPackage(root, dir string, source []byte) error {
	target := filepath.Join(root, dir)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return &Error{
			Code:    CodeWriteFailed,
			Message: "cannot create the runtime package directory " + strconv.Quote(target),
			Err:     err,
		}
	}
	file := filepath.Join(target, dir+".go")
	if err := os.WriteFile(file, source, 0o644); err != nil {
		return &Error{
			Code:    CodeWriteFailed,
			Message: "cannot write the runtime package " + strconv.Quote(file),
			Err:     err,
		}
	}
	return nil
}

// renderRuntime generates the source of the activation package.
//
// The package holds exactly one exported name. M is the activation array,
// indexed by the catalogue's own dense index, which is what a guard spells;
// everything else — the ID table, the environment variable, the diagnostic —
// is unexported, because the instrumented code has no business reaching for it
// and a second export would be a second thing to keep compatible.
//
// Writes to M happen in init and nowhere else. A package's init runs before any
// test code that imports it, so every later read is an ordinary array load with
// no synchronisation, no allocation, and nothing for the race detector to find.
//
// The array is never zero-length even for an empty catalogue: `var M [0]bool`
// is legal Go but leaves the package's only export unusable, and a length of
// one costs a byte and keeps the generated source one shape rather than two.
func renderRuntime(pkgName string, catalog *mutation.Catalog) ([]byte, error) {
	mutants := catalog.Mutants()
	size := runtimeArraySize(catalog)

	var b strings.Builder
	generatedPreamble(&b)
	fmt.Fprintf(&b, "// Package %s carries the mutant activation state of one go-mutants run.\n", pkgName)
	b.WriteString("//\n")
	b.WriteString("// It is generated into the disposable snapshot, never into the tree it was\n")
	b.WriteString("// copied from, and it is a first-party package of the module under test so that\n")
	b.WriteString("// no go.mod edit and no vendor entry is needed to import it.\n")
	fmt.Fprintf(&b, "package %s\n\n", pkgName)
	b.WriteString("import (\n\t\"fmt\"\n\t\"os\"\n)\n\n")

	b.WriteString("// activeEnv names the mutant that is live in this process. Empty or unset is\n")
	b.WriteString("// the instrumented baseline: every mutant dormant, every guard taking the\n")
	b.WriteString("// branch that holds the original source.\n")
	fmt.Fprintf(&b, "const activeEnv = %q\n\n", ActiveEnv)

	b.WriteString("// unknownMutantExit is the status this process exits with when activeEnv names\n")
	b.WriteString("// a mutant that is not in this tree. Running the tests anyway would report a\n")
	b.WriteString("// survivor for a mutant that was never live.\n")
	fmt.Fprintf(&b, "const unknownMutantExit = %d\n\n", UnknownMutantExit)

	b.WriteString("// M is one activation flag per catalogued mutant, indexed by the catalogue's\n")
	b.WriteString("// dense index. Instrumented guards read it and nothing writes it after init.\n")
	fmt.Fprintf(&b, "var M [%d]bool\n\n", size)

	b.WriteString("// ids maps every catalogued mutant ID to its index in M.\n")
	b.WriteString("var ids = map[string]uint32{\n")
	for _, m := range mutants {
		fmt.Fprintf(&b, "\t%q: %d,\n", m.ID, m.Index)
	}
	b.WriteString("}\n\n")

	b.WriteString("// init activates the mutant the environment names, if any.\n")
	b.WriteString("func init() {\n")
	b.WriteString("\tid := os.Getenv(activeEnv)\n")
	b.WriteString("\tif id == \"\" {\n\t\treturn\n\t}\n")
	b.WriteString("\tindex, ok := ids[id]\n")
	b.WriteString("\tif !ok {\n")
	b.WriteString("\t\tfmt.Fprintln(os.Stderr, \"go-mutants: \"+activeEnv+\"=\"+id+\" names a mutant this instrumented tree does not contain;\"+\n")
	b.WriteString("\t\t\t\" the catalogue and the snapshot have drifted apart, most likely a stale catalogue reused against a fresh tree;\"+\n")
	b.WriteString("\t\t\t\" re-run go-mutants so that both are built from the same source\")\n")
	b.WriteString("\t\tos.Exit(unknownMutantExit)\n")
	b.WriteString("\t}\n")
	b.WriteString("\tM[index] = true\n")
	b.WriteString("}\n")

	return formatGenerated(&b)
}

// renderProbeRuntime generates the source of the probe package.
//
// It is a second generator rather than a flag on the first, and the two share
// only what is genuinely the same file: the header lines, the array length rule,
// the gofmt pass. Everything else differs — the exported name, the imports, the
// environment variable, what init does, whether there is an ID table at all —
// and threading that through one template would produce a function whose every
// line asks which tree it is generating, in exchange for saving a preamble.
//
// The package holds exactly one exported name, [ProbeEnv]'s reader excepted
// because it is init. Infect is what a probe form calls when the mutated value
// at its site would have differed from the original's; the file it appends to,
// the guard array, and the header are unexported, because a probe tree has no
// business reaching for any of them and a second export would be a second thing
// to keep compatible.
//
// There is no table from mutant ID to index here, and that is the difference
// that matters: a probe tree activates nothing, so it never resolves an ID.
// What it writes is the dense index a guard already spells, and the header
// carries the catalogue digest so that a reader can refuse indices minted
// against a catalogue it was not given.
//
// The array is never zero-length, for the reason [renderRuntime]'s is not: an
// empty catalogue is a real case, `var probeSeen [0]uint32` is legal Go that no
// index can address, and a header claiming zero mutants would describe a log in
// which no line could ever be valid.
func renderProbeRuntime(pkgName string, catalog *mutation.Catalog) ([]byte, error) {
	size := runtimeArraySize(catalog)

	var b strings.Builder
	generatedPreamble(&b)
	fmt.Fprintf(&b, "// Package %s records which of one go-mutants run's mutants a probe\n", pkgName)
	b.WriteString("// pass could have observed.\n")
	b.WriteString("//\n")
	b.WriteString("// It is generated into the disposable snapshot, never into the tree it was\n")
	b.WriteString("// copied from, and it is a first-party package of the module under test so that\n")
	b.WriteString("// no go.mod edit and no vendor entry is needed to import it.\n")
	fmt.Fprintf(&b, "package %s\n\n", pkgName)
	b.WriteString("import (\n\t\"fmt\"\n\t\"os\"\n\t\"sync/atomic\"\n)\n\n")

	b.WriteString("// probeEnv names the file this process appends its infection log to. Empty or\n")
	b.WriteString("// unset is an ordinary run: Infect costs one nil check and returns.\n")
	fmt.Fprintf(&b, "const probeEnv = %q\n\n", ProbeEnv)

	b.WriteString("// probeUnavailableExit is the status this process exits with when the log it\n")
	b.WriteString("// was told to write cannot be opened or written. Silence would be read as \"no\n")
	b.WriteString("// site was infected\", which is the one lie a probe must never tell.\n")
	fmt.Fprintf(&b, "const probeUnavailableExit = %d\n\n", ProbeUnavailableExit)

	b.WriteString("// probeHeader is the line this process writes before any index: the format,\n")
	b.WriteString("// the catalogue these indices are dense in, and how many of them there are.\n")
	b.WriteString("// Several processes append to one log, so it is written once per process\n")
	b.WriteString("// rather than once per file: a process that held its header back until it had\n")
	b.WriteString("// an index to write would say nothing at all if it died first.\n")
	fmt.Fprintf(&b, "const probeHeader = %q\n\n", infectionHeader(catalog.Digest(), size))

	b.WriteString("// probeFile is the log this process appends to, and nil when probing is off.\n")
	b.WriteString("// It is written in init and read everywhere else, so nothing synchronises on\n")
	b.WriteString("// it: a package's init runs before any test code that imports it.\n")
	b.WriteString("var probeFile *os.File\n\n")

	b.WriteString("// probeSeen is one guard per catalogued mutant, indexed by the catalogue's\n")
	b.WriteString("// dense index: zero until that site is first seen to differ, one afterwards.\n")
	b.WriteString("// It is what keeps the log to one line per mutant however many times a site is\n")
	b.WriteString("// evaluated, and it is atomic because a test suite is concurrent.\n")
	fmt.Fprintf(&b, "var probeSeen [%d]uint32\n\n", size)

	b.WriteString("// Infect records that mutant i's site evaluated to a value the original would\n")
	b.WriteString("// not have produced, at least once in this process.\n")
	b.WriteString("//\n")
	b.WriteString("// An index is written the first time it is seen and never again, straight to\n")
	b.WriteString("// an O_APPEND file: there is no exit hook and no flush window, so whatever a\n")
	b.WriteString("// process wrote before it died is exactly what it proved. An index outside the\n")
	b.WriteString("// catalogue panics on the bounds check, which is the right answer to what can\n")
	b.WriteString("// only be a generator bug: a probe process that fails yields no facts, and no\n")
	b.WriteString("// facts is the safe answer.\n")
	b.WriteString("func Infect(i uint32) {\n")
	b.WriteString("\tif probeFile == nil {\n\t\treturn\n\t}\n")
	b.WriteString("\tif !atomic.CompareAndSwapUint32(&probeSeen[i], 0, 1) {\n\t\treturn\n\t}\n")
	b.WriteString("\tif _, err := fmt.Fprintln(probeFile, i); err != nil {\n")
	probeDiagnostic(&b, "\t\t", `"go-mutants: cannot append to the infection log "+probeFile.Name()`, "err")
	b.WriteString("\t}\n")
	b.WriteString("}\n\n")

	b.WriteString("// init opens the log the environment names and writes this process's header,\n")
	b.WriteString("// or leaves probing off when it names none.\n")
	b.WriteString("func init() {\n")
	b.WriteString("\tpath := os.Getenv(probeEnv)\n")
	b.WriteString("\tif path == \"\" {\n\t\treturn\n\t}\n")
	b.WriteString("\tfile, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)\n")
	b.WriteString("\tif err != nil {\n")
	probeDiagnostic(&b, "\t\t", `"go-mutants: cannot open the infection log "+path`, "err")
	b.WriteString("\t}\n")
	b.WriteString("\tif _, headerErr := fmt.Fprintln(file, probeHeader); headerErr != nil {\n")
	probeDiagnostic(&b, "\t\t", `"go-mutants: cannot write the header of the infection log "+path`, "headerErr")
	b.WriteString("\t}\n")
	// Last, so that a process which never got a usable log leaves Infect
	// looking at a nil file rather than at one it could not write a header to.
	b.WriteString("\tprobeFile = file\n")
	b.WriteString("}\n")

	return formatGenerated(&b)
}

// probeDiagnostic writes the generated runtime's one failure path: say which
// file could not be written and why it matters, then exit.
//
// The second half of the message is not decoration. Somebody meeting this in a
// test log has to know that the run did not merely lose a file, it lost the
// right to conclude anything from the run, which is why the process stopped
// rather than carried on.
func probeDiagnostic(b *strings.Builder, indent, what, cause string) {
	b.WriteString(indent + "fmt.Fprintln(os.Stderr, " + what + "+\": \"+" + cause + ".Error()+\n")
	b.WriteString(indent + "\t\"; a probe process that cannot record what it saw would be read as having seen nothing\")\n")
	b.WriteString(indent + "os.Exit(probeUnavailableExit)\n")
}

// runtimeArraySize is the length of the per-mutant array both generated
// runtimes carry: the catalogue's size, or one when the catalogue is empty.
//
// An empty catalogue is a real case — a run whose filters selected nothing —
// and a zero-length array is legal Go that no index can address. One element
// costs a byte and keeps each generated source one shape rather than two.
func runtimeArraySize(catalog *mutation.Catalog) int {
	if size := catalog.Len(); size > 0 {
		return size
	}
	return 1
}

// generatedPreamble writes the lines both generated packages open with: the
// SPDX header every file in this repository carries, and the marker every Go
// generator follows (https://go.dev/s/generatedcode), which is the exact pattern
// internal/discover refuses to mutate on — a later run over a tree that somehow
// kept one of these files has to skip it rather than mutate the machinery it is
// mutating with.
func generatedPreamble(b *strings.Builder) {
	b.WriteString("// SPDX-FileCopyrightText: 2026 go-mutants contributors\n")
	b.WriteString("// SPDX-License-Identifier: MIT OR Apache-2.0\n\n")
	b.WriteString("// Code generated by go-mutants. DO NOT EDIT.\n\n")
}

// formatGenerated gofmts a rendered package rather than leaving the templates
// to hand-align themselves. It also proves the generated source parses, which is
// the one property a code generator cannot afford to get wrong quietly: an
// unparsable runtime would surface as a compile failure in every instrumented
// package at once.
func formatGenerated(b *strings.Builder) ([]byte, error) {
	source, err := format.Source([]byte(b.String()))
	if err != nil {
		return nil, &Error{
			Code:    CodeUnparsable,
			Message: "internal error: the generated runtime package does not parse",
			Err:     err,
		}
	}
	return source, nil
}
