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
	size := len(mutants)
	if size == 0 {
		size = 1
	}

	var b strings.Builder
	b.WriteString("// SPDX-FileCopyrightText: 2026 go-mutants contributors\n")
	b.WriteString("// SPDX-License-Identifier: MIT OR Apache-2.0\n\n")
	b.WriteString("// Code generated by go-mutants. DO NOT EDIT.\n\n")
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

	// gofmt the result rather than hand-aligning the template. It also proves
	// the generated source parses, which is the one property a code generator
	// cannot afford to get wrong quietly: an unparsable runtime would surface
	// as a compile failure in every instrumented package at once.
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
