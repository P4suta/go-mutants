// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit"
)

// syscallPackage and syscallPrefixes name the calls a Pointer may be converted
// to a uintptr inside.
//
// They are the functions the runtime marks `//go:uintptrkeepalive` and
// `//go:nosplit` — syscall.Syscall, Syscall6, SyscallN and the Raw* forms — and
// the two pragmas are why the conversion is safe *there* and nowhere else. The
// list is a constant rather than a heuristic so that admitting another callee
// is a deliberate edit with a reason beside it.
const syscallPackage = "syscall"

var syscallPrefixes = []string{"Syscall", "RawSyscall"}

// TestEveryPointerHandedToASyscallIsConvertedInsideTheCall is a rule of the Go
// compiler, written down where it can fail.
//
// `uintptr(unsafe.Pointer(&x))` is only meaningful in the argument list of the
// assembly syscall itself. The runtime says why, in syscall/dll_windows.go
// beside the pragmas that make it work: "//go:nosplit because stack copying
// does not account for uintptrkeepalive, so the stack must not grow. Stack
// copying cannot blindly assume that all uintptr arguments are pointers,
// because some values may look like pointers, but not really be pointers, and
// adjusting their value would break the call."
//
// Write the conversion one Go frame earlier — in a call to an ordinary wrapper
// that spells the parameter `uintptr`, which is how golang.org/x/sys/windows
// spells SetInformationJobObject and QueryInformationJobObject — and none of
// that holds. Escape analysis leaves the value on the stack, because a uintptr
// is not a pointer; the wrapper's own prologue is a stack-growth point; a
// goroutine that grows there has its frames copied and its old stack span
// returned to the pool with the uintptr still naming the old address. Another
// goroutine takes the span, writes its own frames into it, and the kernel reads
// whatever landed at that offset. runtime.KeepAlive does not help: it keeps a
// value from being collected, and a stack frame is not collected, it is moved.
//
// That is a defect only a concurrent run can show, and it showed once —
// windows-latest, 2026-09-06, `GOM7201: could not set kill-on-close on the
// Windows job object ...: The parameter is incorrect.` under eight concurrent
// Workspace.Exec calls, green on every other Windows run. `go vet` has no
// analyzer for this direction of the conversion, so the gate is a scan, which
// is this repository's precedent for a rule the linters do not carry.
func TestEveryPointerHandedToASyscallIsConvertedInsideTheCall(t *testing.T) {
	t.Parallel()

	root := testkit.Root(t)
	offenders, err := pointerConversionsOutsideASyscall(root)
	if err != nil {
		t.Fatalf("scanning %s for pointer-to-uintptr conversions: %v", root, err)
	}
	if len(offenders) != 0 {
		t.Errorf("%d conversion(s) hand a pointer to a uintptr parameter of an ordinary Go function, "+
			"where a stack copy would leave the kernel reading a freed frame:\n\t%s",
			len(offenders), strings.Join(offenders, "\n\t"))
	}
}

// TestThePointerConversionGateNamesTheOffendingLine proves the scan can fail
// and that it is precise about where.
//
// A gate that only ever passes is indistinguishable from one that cannot see
// anything, and the way this one would break is by matching too little — an
// expression shape it does not recognise, a directory it walks past. So it is
// pointed at a module built to offend, with the safe spelling of the very same
// call beside the unsafe one.
func TestThePointerConversionGateNamesTheOffendingLine(t *testing.T) {
	t.Parallel()

	// The conversion one frame early, the same conversion written where it is
	// safe, and a test file, which the scan does not read.
	const offending = `package bad

import (
	"syscall"
	"unsafe"
)

func Set(fn uintptr, info *[16]byte) {
	wrapper(fn, uintptr(unsafe.Pointer(info)), unsafe.Sizeof(*info))
}

func wrapper(fn, addr, size uintptr) {
	_, _, _ = syscall.SyscallN(fn, addr, size)
}
`
	m := testkit.NewModule(t).Module("fixture.example/ptr")
	m.Source("bad/bad.go", offending)
	m.Source("good/good.go", `package good

import (
	"syscall"
	"unsafe"
)

func Set(fn uintptr, info *[16]byte) {
	_, _, _ = syscall.SyscallN(fn, uintptr(unsafe.Pointer(info)), unsafe.Sizeof(*info))
}
`)
	m.Source("good/good_test.go", `package good

import "unsafe"

var _ = uintptr(unsafe.Pointer(&struct{}{}))
`)

	got, err := pointerConversionsOutsideASyscall(m.Root())
	if err != nil {
		t.Fatalf("scanning the synthesized module: %v", err)
	}
	// Derived rather than typed, because Source writes [testkit.SPDXHeader] in
	// front of the body and a line number counted off the literal above would
	// be wrong by however tall that header is today.
	want := []string{fmt.Sprintf("bad/bad.go:%d", lineOf(testkit.SPDXHeader+offending, "wrapper(fn,"))}
	if !slices.Equal(got, want) {
		t.Errorf("pointerConversionsOutsideASyscall = %q, want %q", got, want)
	}
}

// lineOf is the one-based number of the first line of source containing needle.
func lineOf(source, needle string) int {
	for i, line := range strings.Split(source, "\n") {
		if strings.Contains(line, needle) {
			return i + 1
		}
	}
	return 0
}

// pointerConversionsOutsideASyscall reports every `uintptr(unsafe.Pointer(...))`
// in root's production files that is not written directly in the argument list
// of a syscall call, as "<path>:<line>" and sorted.
//
// It is a scan of the source rather than of the type-checked program because
// the unit tier may not reach for a toolchain, and because the expression this
// is about is recognisable from its shape alone: nothing else spells
// `uintptr(unsafe.Pointer(x))`.
func pointerConversionsOutsideASyscall(root string) ([]string, error) {
	var offenders []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && slices.Contains(skippedTrees, entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		// Build tags hide a file from this platform's compiler and from
		// nothing else, which is the point: the code this rule is about is the
		// code that never compiles on the machine running the gate.
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, call := range unguardedPointerConversions(file) {
			offenders = append(offenders, fmt.Sprintf("%s:%d",
				filepath.ToSlash(rel), fset.Position(call.Pos()).Line))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(offenders)
	return offenders, nil
}

// skippedTrees are the directories holding Go files that are not this module's
// own code: the corpus is a set of modules of its own, testdata holds inputs,
// and vendor-assets holds somebody else's source.
var skippedTrees = []string{"testdata", "fixtures", "vendor-assets", ".git"}

// unguardedPointerConversions returns the pointer-to-uintptr conversions in one
// file that are not arguments of a syscall call.
//
// The permitted ones are collected first and whole, because "is this expression
// an argument of that call" is a question about a parent and ast.Inspect
// answers questions about children.
func unguardedPointerConversions(file *ast.File) []*ast.CallExpr {
	permitted := make(map[*ast.CallExpr]bool)
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || !isSyscallCall(call) {
			return true
		}
		for _, arg := range call.Args {
			if inner, ok := arg.(*ast.CallExpr); ok && isPointerToUintptr(inner) {
				permitted[inner] = true
			}
		}
		return true
	})

	var unguarded []*ast.CallExpr
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if ok && isPointerToUintptr(call) && !permitted[call] {
			unguarded = append(unguarded, call)
		}
		return true
	})
	return unguarded
}

// isPointerToUintptr reports whether call is `uintptr(unsafe.Pointer(x))`.
func isPointerToUintptr(call *ast.CallExpr) bool {
	converted, ok := call.Fun.(*ast.Ident)
	if !ok || converted.Name != "uintptr" || len(call.Args) != 1 {
		return false
	}
	inner, ok := call.Args[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := inner.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Pointer" {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == "unsafe"
}

// isSyscallCall reports whether call is one of the syscall package's
// //go:uintptrkeepalive entry points.
func isSyscallCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok || pkg.Name != syscallPackage {
		return false
	}
	return slices.ContainsFunc(syscallPrefixes, func(prefix string) bool {
		return strings.HasPrefix(selector.Sel.Name, prefix)
	})
}
