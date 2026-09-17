// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourcegate_test

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/P4suta/go-mutants/internal/testkit"
)

var safeCallees = []string{
	"syscall.Syscall",
	"syscall.Syscall6",
	"syscall.Syscall9",
	"syscall.Syscall12",
	"syscall.Syscall15",
	"syscall.Syscall18",
	"syscall.SyscallN",
	"syscall.RawSyscall",
	"syscall.RawSyscall6",
	"syscall.Proc.Call",
	"syscall.LazyProc.Call",
	"golang.org/x/sys/windows.Proc.Call",
	"golang.org/x/sys/windows.LazyProc.Call",
}

func TestEveryPointerHandedToASyscallIsConvertedInsideTheCall(t *testing.T) {
	t.Parallel()

	root := testkit.Root(t)
	for _, goos := range targetsToTypeCheck() {
		t.Run(goos, func(t *testing.T) {
			t.Parallel()

			offenders := unguardedConversions(t, root, goos)
			if len(offenders) != 0 {
				t.Errorf("%d conversion(s) hand a pointer to a uintptr parameter of an ordinary Go "+
					"function, where a stack copy would leave the kernel reading a freed frame:\n\t%s",
					len(offenders), strings.Join(offenders, "\n\t"))
			}
		})
	}
}

func TestThePointerConversionGateSeesThroughAliasesAndShadowing(t *testing.T) {
	t.Parallel()

	const aliasedUnsafe = `package aliasedunsafe

import u "unsafe"

func Set(fn uintptr, info *[16]byte) {
	wrapper(fn, uintptr(u.Pointer(info)))
}

func wrapper(fn, addr uintptr) { _, _ = fn, addr }
`
	const shadowedSyscall = `package shadowedsyscall

import "unsafe"

type stand struct{}

func (stand) SyscallN(trap uintptr, args ...uintptr) (uintptr, uintptr, error) {
	_, _ = trap, args
	return 0, 0, nil
}

func Set(fn uintptr, info *[16]byte) {
	syscall := stand{}
	_, _, _ = syscall.SyscallN(fn, uintptr(unsafe.Pointer(info)), unsafe.Sizeof(*info))
}
`
	const aliasedSyscall = `package aliasedsyscall

import (
	sc "syscall"
	"unsafe"
)

func Set(fn uintptr, info *[16]byte) {
	_, _, _ = sc.SyscallN(fn, uintptr(unsafe.Pointer(info)), unsafe.Sizeof(*info))
}
`
	const plainlyUnsafe = `package plainlyunsafe

import "unsafe"

func Set(info *[16]byte) uintptr { return uintptr(unsafe.Pointer(info)) }
`
	const plainlySafeTest = `package plainlysafe

import (
	"syscall"
	"unsafe"
)

func Set(fn uintptr, info *[16]byte) {
	_, _, _ = syscall.SyscallN(fn, uintptr(unsafe.Pointer(info)), unsafe.Sizeof(*info))
}
`

	const storedPointer = `package storedpointer

import "unsafe"

func Set(fn uintptr, info *[16]byte) {
	p := unsafe.Pointer(info)
	wrapper(fn, uintptr(p))
}

func wrapper(fn, addr uintptr) { _, _ = fn, addr }
`
	const definedTypes = `package definedtypes

import "unsafe"

type address uintptr

type raw unsafe.Pointer

func Set(fn uintptr, info *[16]byte) {
	wrapper(fn, address(raw(unsafe.Pointer(info))))
}

func wrapper(fn uintptr, addr address) { _, _ = fn, addr }
`

	m := testkit.NewModule(t).Module("fixture.example/ptr")
	m.Source("aliasedunsafe/a.go", aliasedUnsafe)
	m.Source("shadowedsyscall/s.go", shadowedSyscall)
	m.Source("aliasedsyscall/a.go", aliasedSyscall)
	m.Source("plainlyunsafe/p.go", plainlyUnsafe)
	m.Source("plainlysafe/p_test.go", plainlySafeTest)
	m.Source("storedpointer/s.go", storedPointer)
	m.Source("definedtypes/d.go", definedTypes)

	got := unguardedConversions(t, m.Root(), "windows")

	want := []string{
		fmt.Sprintf("aliasedunsafe/a.go:%d", lineOf(testkit.SPDXHeader+aliasedUnsafe, "wrapper(fn,")),
		fmt.Sprintf("definedtypes/d.go:%d", lineOf(testkit.SPDXHeader+definedTypes, "wrapper(fn,")),
		fmt.Sprintf("plainlyunsafe/p.go:%d", lineOf(testkit.SPDXHeader+plainlyUnsafe, "func Set(")),
		fmt.Sprintf("shadowedsyscall/s.go:%d", lineOf(testkit.SPDXHeader+shadowedSyscall, "syscall.SyscallN(fn,")),
		fmt.Sprintf("storedpointer/s.go:%d", lineOf(testkit.SPDXHeader+storedPointer, "wrapper(fn,")),
	}
	if !slices.Equal(got, want) {
		t.Errorf("the gate reported\n\t%v\nwant\n\t%v\nthe aliased `unsafe` import, the shadowed "+
			"`syscall` local, a pointer parked in a variable and a pair of defined types are offenders "+
			"a syntactic match would miss, and the aliased real `syscall` import is one it would invent",
			got, want)
	}
}

func lineOf(source, needle string) int {
	for i, line := range strings.Split(source, "\n") {
		if strings.Contains(line, needle) {
			return i + 1
		}
	}
	return 0
}

func targetsToTypeCheck() []string {
	if runtime.GOOS == "windows" {
		return []string{"windows"}
	}
	return []string{"windows", runtime.GOOS}
}

func unguardedConversions(t *testing.T, root, goos string) []string {
	t.Helper()

	toolchain := testkit.GoBinary(t)
	env := append(os.Environ(), "GOOS="+goos,
		"PATH="+filepath.Dir(toolchain)+string(filepath.ListSeparator)+os.Getenv("PATH"))

	fset := token.NewFileSet()
	loaded, err := packages.Load(&packages.Config{
		Context: t.Context(),
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo,
		Dir:   root,
		Env:   env,
		Fset:  fset,
		Tests: true,
	}, "./...")
	if err != nil {
		t.Fatalf("loading %s for %s: %v", root, goos, err)
	}
	if len(loaded) == 0 {
		t.Fatalf("no packages loaded from %s for %s, so this proves nothing", root, goos)
	}
	var failures []string
	for _, pkg := range loaded {
		for _, loadErr := range pkg.Errors {
			failures = append(failures, pkg.PkgPath+": "+loadErr.Error())
		}
	}
	if len(failures) != 0 {
		t.Fatalf("%d package error(s) for %s, so the gate saw less than the module:\n\t%s",
			len(failures), goos, strings.Join(failures, "\n\t"))
	}

	var offenders []string
	for _, pkg := range loaded {
		for _, file := range pkg.Syntax {
			for _, call := range unguardedInFile(pkg.TypesInfo, file) {
				position := fset.Position(call.Pos())
				rel, relErr := filepath.Rel(root, position.Filename)
				if relErr != nil {
					rel = position.Filename
				}
				offenders = append(offenders, fmt.Sprintf("%s:%d", filepath.ToSlash(rel), position.Line))
			}
		}
	}
	slices.Sort(offenders)
	return slices.Compact(offenders)
}

func unguardedInFile(info *types.Info, file *ast.File) []*ast.CallExpr {
	permitted := make(map[*ast.CallExpr]bool)
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || !isSafeCallee(info, call) {
			return true
		}
		for _, arg := range call.Args {
			if inner, ok := arg.(*ast.CallExpr); ok && isPointerToUintptr(info, inner) {
				permitted[inner] = true
			}
		}
		return true
	})

	var unguarded []*ast.CallExpr
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if ok && isPointerToUintptr(info, call) && !permitted[call] {
			unguarded = append(unguarded, call)
		}
		return true
	})
	return unguarded
}

func isPointerToUintptr(info *types.Info, call *ast.CallExpr) bool {
	if !isConversionTo(info, call, types.Typ[types.Uintptr]) {
		return false
	}
	tv, ok := info.Types[call.Args[0]]
	return ok && tv.IsValue() && tv.Type != nil &&
		types.Identical(tv.Type.Underlying(), types.Typ[types.UnsafePointer])
}

func isConversionTo(info *types.Info, call *ast.CallExpr, want types.Type) bool {
	if len(call.Args) != 1 {
		return false
	}
	tv, ok := info.Types[call.Fun]
	return ok && tv.IsType() && tv.Type != nil && types.Identical(tv.Type.Underlying(), want)
}

func isSafeCallee(info *types.Info, call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	fn, ok := info.Uses[selector.Sel].(*types.Func)
	if !ok {
		return false
	}
	key := calleeKey(fn)
	return key != "" && slices.Contains(safeCallees, key)
}

func calleeKey(fn *types.Func) string {
	pkg := fn.Pkg()
	if pkg == nil {
		return ""
	}
	recv := fn.Signature().Recv()
	if recv == nil {
		return pkg.Path() + "." + fn.Name()
	}
	receiver := recv.Type()
	if pointer, ok := receiver.(*types.Pointer); ok {
		receiver = pointer.Elem()
	}
	named, ok := receiver.(*types.Named)
	if !ok {
		return ""
	}
	return pkg.Path() + "." + named.Obj().Name() + "." + fn.Name()
}
