// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/tempowner"
)

func swapSeam[T any](t *testing.T, seam *T, with T) {
	t.Helper()
	was := *seam
	*seam = with
	t.Cleanup(func() { *seam = was })
}

var errRefused = errors.New("the filesystem refused")

func unreadableDir(t *testing.T, dir string) {
	t.Helper()
	chmodOrSkip(t, dir, 0o000, 0o700, func() error {
		_, err := os.ReadDir(dir)
		return err
	})
}

func unwritableDir(t *testing.T, dir string) {
	t.Helper()
	chmodOrSkip(t, dir, 0o500, 0o700, func() error {
		probe := filepath.Join(dir, "probe")
		err := os.WriteFile(probe, []byte("x"), 0o600)
		if err == nil {
			_ = os.Remove(probe)
		}
		return err
	})
}

func unsearchableDir(t *testing.T, dir string) {
	t.Helper()
	chmodOrSkip(t, dir, 0o600, 0o700, func() error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Skipf("this filesystem does not list an unsearchable directory: %v", err)
		}
		if len(entries) == 0 {
			t.Skip("an unsearchable directory needs something in it to errRefused to stat")
		}
		_, err = entries[0].Info()
		return err
	})
}

func unreadableFile(t *testing.T, path string) {
	t.Helper()
	chmodOrSkip(t, path, 0o200, 0o600, func() error {
		f, err := os.Open(path)
		if err == nil {
			_ = f.Close()
		}
		return err
	})
}

func chmodOrSkip(t *testing.T, path string, mode, restore fs.FileMode, probe func() error) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("a Windows file mode does not errRefused this the way the test needs")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permissions this test uses to make an operation fail")
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("setting the mode of %s: %v", path, err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, restore) })
	if probe() == nil {
		t.Skip("this filesystem does not enforce the mode this test needs")
	}
}

func TestCreateRefusesWhatItCannotResolveOrRead(t *testing.T) {
	t.Parallel()

	t.Run("a source root that is not a directory", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		file := filepath.Join(root, "a.go")
		if err := os.WriteFile(file, []byte("package a\n"), 0o600); err != nil {
			t.Fatalf("writing a file: %v", err)
		}
		_, err := Create(file, Options{DestParent: t.TempDir()})
		assertCode(t, err, CodeSourceRoot)
	})

	t.Run("a directory inside the tree that cannot be listed", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeTree(t, root, map[string]string{"keep/a.go": "package a\n"})
		unreadableDir(t, filepath.Join(root, "keep"))

		_, err := Create(root, Options{DestParent: t.TempDir()})
		assertCode(t, err, CodeWalk)
		if !errors.Is(err, fs.ErrPermission) {
			t.Errorf("the failure does not carry the refusal the listing reported: %v", err)
		}
		if got := pathOfError(t, err); got != "keep" {
			t.Errorf("the failure's path is %q, want the directory it could not list", got)
		}
	})

	t.Run("an entry inside the tree that cannot be stat-ed", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeTree(t, root, map[string]string{"keep/a.go": "package a\n"})
		unsearchableDir(t, filepath.Join(root, "keep"))

		_, err := Create(root, Options{DestParent: t.TempDir()})
		assertCode(t, err, CodeWalk)
		if got := pathOfError(t, err); got != "keep/a.go" {
			t.Errorf("the failure's path is %q, want the entry it could not stat", got)
		}
	})

	t.Run("a source file that cannot be read", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeTree(t, root, map[string]string{"a.go": "package a\n"})
		unreadableFile(t, filepath.Join(root, "a.go"))

		dest := t.TempDir()
		_, err := Create(root, Options{DestParent: dest})
		assertCode(t, err, CodeCopy)
		if !strings.Contains(err.Error(), "a.go") {
			t.Errorf("the failure does not name the file: %v", err)
		}
		assertEmptyDir(t, dest)
	})
}

func TestCreateCleansUpAfterASyscallItCannotBeMadeToFail(t *testing.T) {
	for _, test := range []struct {
		name string
		seam func(t *testing.T)
		code Code
	}{{
		name: "the snapshot tree cannot be created",
		seam: func(t *testing.T) {
			swapSeam(t, &makeTreeDir, func(string, fs.FileMode) error { return errRefused })
		},
		code: CodeDestination,
	}, {
		name: "a directory of the copy cannot be created",
		seam: func(t *testing.T) {
			swapSeam(t, &makeDirTree, func(string, fs.FileMode) error { return errRefused })
		},
		code: CodeCopy,
	}, {
		name: "a directory's permissions cannot be set",
		seam: func(t *testing.T) {
			swapSeam(t, &setDirPerm, func(string, fs.FileMode) error { return errRefused })
		},
		code: CodeCopy,
	}, {
		name: "a directory's times cannot be set",
		seam: func(t *testing.T) {
			swapSeam(t, &setFileTimes, func(path string, a, b time.Time) error {
				if info, err := os.Stat(path); err == nil && info.IsDir() {
					return errRefused
				}
				return os.Chtimes(path, a, b)
			})
		},
		code: CodeCopy,
	}} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, map[string]string{"pkg/a.go": "package pkg\n"})
			dest := t.TempDir()
			test.seam(t)

			_, err := Create(root, Options{DestParent: dest})
			assertCode(t, err, test.code)
			if !errors.Is(err, errRefused) {
				t.Errorf("the failure does not carry the one that was staged: %v", err)
			}
			assertEmptyDir(t, dest)
		})
	}
}

func TestCreateLeavesADirectoryItCouldNotClaim(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "package a\n"})
	dest := t.TempDir()

	var claimed string
	swapSeam(t, &claimDir, func(dir string, _ time.Time) (*tempowner.Owner, error) {
		claimed = dir
		return nil, &Error{Code: CodeDestination, Path: dir, Message: "cannot claim the snapshot directory", Err: errRefused}
	})

	_, err := Create(root, Options{DestParent: dest})
	assertCode(t, err, CodeDestination)
	if !errors.Is(err, errRefused) {
		t.Errorf("the failure does not carry the one that was staged: %v", err)
	}
	if claimed == "" {
		t.Fatal("Create never reached the claim")
	}
	if _, statErr := os.Stat(claimed); statErr != nil {
		t.Errorf("the directory the claim lost was removed anyway: %v", statErr)
	}
}

func TestAPathThatCannotBeResolvedIsRefusedAtBothEnds(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "package a\n"})

	t.Run("the source root", func(t *testing.T) {
		swapSeam(t, &absPath, func(string) (string, error) { return "", errRefused })
		_, err := Create(root, Options{DestParent: t.TempDir()})
		assertCode(t, err, CodeInvalidOptions)
		if !errors.Is(err, errRefused) {
			t.Errorf("the failure does not carry the one that was staged: %v", err)
		}
	})

	t.Run("the destination parent", func(t *testing.T) {
		calls := 0
		swapSeam(t, &absPath, func(path string) (string, error) {
			calls++
			if calls == 1 {
				return filepath.Abs(path)
			}
			return "", errRefused
		})
		_, err := Create(root, Options{DestParent: t.TempDir()})
		assertCode(t, err, CodeDestination)
		if !errors.Is(err, errRefused) {
			t.Errorf("the failure does not carry the one that was staged: %v", err)
		}
	})
}

func TestTheDestinationParentIsWhereCreateFailsFirst(t *testing.T) {
	t.Parallel()

	t.Run("a parent that refuses new directories", func(t *testing.T) {
		t.Parallel()

		parent := t.TempDir()
		unwritableDir(t, parent)
		_, stable, err := destination(parent, t.TempDir())
		assertCode(t, err, CodeDestination)
		if stable {
			t.Error("a failed destination reported the stable name")
		}
	})

	t.Run("a parent that refuses the fallback too", func(t *testing.T) {
		t.Parallel()

		parent := t.TempDir()
		src := t.TempDir()
		taken := filepath.Join(parent, StableName(absolutePath(t, src)))
		if err := os.Mkdir(taken, 0o700); err != nil {
			t.Fatalf("taking the stable name: %v", err)
		}
		unwritableDir(t, parent)

		_, stable, err := destination(parent, src)
		assertCode(t, err, CodeDestination)
		if stable {
			t.Error("a failed destination reported the stable name")
		}
	})
}

func TestClaimDestinationDecidesWhatHappensToTheDirectory(t *testing.T) {
	t.Parallel()

	t.Run("a claim that could not be written removes the directory", func(t *testing.T) {
		t.Parallel()

		dir := filepath.Join(t.TempDir(), "go-mutants-snap-x")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("making the directory: %v", err)
		}
		unwritableDir(t, dir)

		owner, err := claimDestination(dir, time.Now())
		assertCode(t, err, CodeDestination)
		if owner != nil {
			t.Error("a failed claim returned an owner")
		}
		if _, statErr := os.Stat(dir); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("the directory nobody claimed was left behind: %v", statErr)
		}
	})
}

func TestCopyFileReportsEveryWayOneFileCanFail(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.go")
	const content = "package a\n"
	if err := os.WriteFile(src, []byte(content), 0o600); err != nil {
		t.Fatalf("writing the source: %v", err)
	}
	when := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

	t.Run("a source that cannot be opened", func(t *testing.T) {
		secret := filepath.Join(root, "secret.go")
		if err := os.WriteFile(secret, []byte(content), 0o600); err != nil {
			t.Fatalf("writing the source: %v", err)
		}
		unreadableFile(t, secret)
		_, _, err := copyFile(secret, filepath.Join(t.TempDir(), "out.go"), 0o600, when)
		if !errors.Is(err, fs.ErrPermission) {
			t.Errorf("copyFile = %v, want the refusal the open reported", err)
		}
	})

	t.Run("a destination that already exists", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "out.go")
		if _, _, err := copyFile(src, dst, 0o600, when); err != nil {
			t.Fatalf("the first copy: %v", err)
		}
		if _, _, err := copyFile(src, dst, 0o600, when); !errors.Is(err, fs.ErrExist) {
			t.Errorf("the second copy = %v, want a refusal that the destination exists", err)
		}
	})

	t.Run("a source whose bytes cannot be read", func(t *testing.T) {
		if _, _, err := copyFile(root, filepath.Join(t.TempDir(), "out.go"), 0o600, when); err == nil {
			t.Error("copyFile read bytes out of a directory")
		}
	})

	for _, test := range []struct {
		name string
		seam func(t *testing.T)
	}{{
		name: "permissions that cannot be set",
		seam: func(t *testing.T) {
			swapSeam(t, &finalizeCopyPerm, func(*os.File, fs.FileMode) error { return errRefused })
		},
	}, {
		name: "a write handle that cannot be closed",
		seam: func(t *testing.T) {
			swapSeam(t, &closeCopy, func(f *os.File) error { _ = f.Close(); return errRefused })
		},
	}, {
		name: "times that cannot be set",
		seam: func(t *testing.T) {
			swapSeam(t, &setFileTimes, func(string, time.Time, time.Time) error { return errRefused })
		},
	}} {
		t.Run(test.name, func(t *testing.T) {
			test.seam(t)
			_, _, err := copyFile(src, filepath.Join(t.TempDir(), "out.go"), 0o600, when)
			if !errors.Is(err, errRefused) {
				t.Errorf("copyFile = %v, want the staged failure", err)
			}
		})
	}

	t.Run("and what a copy that works leaves behind", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "out.go")
		size, digest, err := copyFile(src, dst, 0o640, when)
		if err != nil {
			t.Fatalf("copyFile: %v", err)
		}
		if size != int64(len(content)) {
			t.Errorf("size = %d, want %d", size, len(content))
		}
		if got := readFile(t, dst); got != content {
			t.Errorf("the copy holds %q, want %q", got, content)
		}
		if want := digestOf(content); digest != want {
			t.Errorf("digest = %q, want %q", digest, want)
		}
		info, statErr := os.Stat(dst)
		if statErr != nil {
			t.Fatalf("stat: %v", statErr)
		}
		if !info.ModTime().Equal(when) {
			t.Errorf("the copy is stamped %v, want the time it was given", info.ModTime())
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
			t.Errorf("the copy's mode is %v, want the source's exactly", info.Mode().Perm())
		}
	})
}

func TestHashFileIsTheReadOnlyHalfOfACopy(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "a.go")
	const content = "package a\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	size, digest, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	if size != int64(len(content)) || digest != digestOf(content) {
		t.Errorf("hashFile = %d, %q, want %d and the content's digest", size, digest, len(content))
	}

	if _, _, err := hashFile(root); err == nil {
		t.Error("hashFile read bytes out of a directory")
	}

	secret := filepath.Join(root, "secret.go")
	if err := os.WriteFile(secret, []byte(content), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}
	unreadableFile(t, secret)
	if _, _, err := hashFile(secret); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("hashFile = %v, want the refusal the open reported", err)
	}
}

func TestEveryDirectoryIsStampedDeepestFirst(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"aaa/x.go":      "package aaa\n",
		"zzz/deep/y.go": "package deep\n",
	})
	when := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, rel := range []string{"aaa", "zzz", "zzz/deep", "."} {
		if err := os.Chtimes(filepath.Join(root, rel), when, when); err != nil {
			t.Fatalf("stamping the source: %v", err)
		}
	}

	snap := create(t, root, Options{DestParent: t.TempDir()})
	for _, rel := range []string{".", "aaa", "zzz", "zzz/deep"} {
		info, err := os.Stat(filepath.Join(snap.Root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if !info.ModTime().Equal(when) {
			t.Errorf("%s is stamped %v, want the source's %v", rel, info.ModTime(), when)
		}
	}
}

func TestStampingSaysWhichDirectoryItCouldNotStamp(t *testing.T) {
	root := t.TempDir()
	dirs := []record{{rel: "aaa", modTime: time.Now()}, {rel: "zzz", modTime: time.Now()}}

	t.Run("a walked directory is named by its relative path", func(t *testing.T) {
		swapSeam(t, &setFileTimes, func(string, time.Time, time.Time) error { return errRefused })
		failed, err := stampDirectoryTimes(dirs, time.Now(), root)
		if !errors.Is(err, errRefused) {
			t.Fatalf("stampDirectoryTimes = %v, want the staged failure", err)
		}
		if failed != "zzz" {
			t.Errorf("the failure names %q, want the directory it was stamping", failed)
		}
	})

	t.Run("the root is named by a dot", func(t *testing.T) {
		swapSeam(t, &setFileTimes, func(path string, _, _ time.Time) error {
			if path == root {
				return errRefused
			}
			return nil
		})
		failed, err := stampDirectoryTimes(dirs, time.Now(), root)
		if !errors.Is(err, errRefused) {
			t.Fatalf("stampDirectoryTimes = %v, want the staged failure", err)
		}
		if failed != "." {
			t.Errorf("the failure names %q, want the one spelling the root has", failed)
		}
	})

	t.Run("and nothing at all when every stamp lands", func(t *testing.T) {
		swapSeam(t, &setFileTimes, func(string, time.Time, time.Time) error { return nil })
		failed, err := stampDirectoryTimes(dirs, time.Now(), root)
		if err != nil || failed != "" {
			t.Errorf("stampDirectoryTimes = %q, %v, want nothing and no failure", failed, err)
		}
	})
}

func TestTheCopyAlwaysHasAWorkerAndNeverMoreThanItNeeds(t *testing.T) {
	t.Parallel()

	for _, files := range []int{0, 1, 2, 1000} {
		if got := snapshotCopyJobs(files); got < 1 {
			t.Errorf("snapshotCopyJobs(%d) = %d, want at least one worker", files, got)
		}
	}
	if got := snapshotCopyJobs(1); got != 1 {
		t.Errorf("snapshotCopyJobs(1) = %d, want one worker for one file", got)
	}
	if got := snapshotCopyJobs(0); got != 1 {
		t.Errorf("snapshotCopyJobs(0) = %d, want one", got)
	}
}

func TestCleanupReportsALockItCouldNotRelease(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), DirPrefix+"x")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("making the directory: %v", err)
	}
	removed := false
	s := &Snapshot{
		dir:        dir,
		destParent: filepath.Dir(dir),
		release:    func() error { return errRefused },
		remove:     func(string) error { removed = true; return nil },
		sleep:      func(time.Duration) {},
	}
	err := s.Cleanup()
	assertCode(t, err, CodeCleanupFailed)
	if !errors.Is(err, errRefused) {
		t.Errorf("the failure does not carry the release's own: %v", err)
	}
	if removed {
		t.Error("a directory whose lock would not come back was removed anyway")
	}
}

func TestAnErrorRendersWhatItHasAndNothingItDoesNot(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		err  *Error
		want string
	}{{
		name: "a condition this package detected itself",
		err:  &Error{Code: CodeSourceRoot, Message: "source root is not a directory"},
		want: "GOM7002: snapshot: source root is not a directory",
	}, {
		name: "with a path",
		err:  &Error{Code: CodeSourceRoot, Path: "/tmp/x", Message: "cannot read the source root"},
		want: `GOM7002: snapshot: cannot read the source root: "/tmp/x"`,
	}, {
		name: "with a path and a cause",
		err:  &Error{Code: CodeWalk, Path: "a/b.go", Message: "cannot stat the entry", Err: fs.ErrPermission},
		want: `GOM7003: snapshot: cannot stat the entry: "a/b.go": permission denied`,
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := test.err.Error(); got != test.want {
				t.Errorf("Error() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPathsEqualRefusesAnEmptySpellingOnEitherSide(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		a, b string
		want bool
	}{
		{"", "", false},
		{"", ".", false},
		{".", "", false},
		{".", ".", true},
		{"/tmp/x", "/tmp/x/", true},
		{"/tmp/x", "/tmp/y", false},
	} {
		if got := pathsEqual(test.a, test.b); got != test.want {
			t.Errorf("pathsEqual(%q, %q) = %v, want %v", test.a, test.b, got, test.want)
		}
	}
}

func TestRedigestReportsAFileItCannotRead(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "package a\n"})
	snap := create(t, root, Options{DestParent: t.TempDir()})
	unreadableFile(t, filepath.Join(snap.Root, "a.go"))

	_, err := snap.Redigest()
	assertCode(t, err, CodeWalk)
	if !strings.Contains(err.Error(), "a.go") {
		t.Errorf("the failure does not name the file: %v", err)
	}

	_, err = snap.Restore()
	assertCode(t, err, CodeWalk)
}

func TestDriftsAreReportedInPathOrderWhateverOrderTheyWereFoundIn(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTree(t, root, map[string]string{"aaa.go": "package a\n", "mmm.go": "package m\n"})
	snap := create(t, root, Options{DestParent: t.TempDir()})

	if err := os.Remove(filepath.Join(snap.Root, "aaa.go")); err != nil {
		t.Fatalf("removing a file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(snap.Root, "zzz.go"), []byte("package z\n"), 0o600); err != nil {
		t.Fatalf("adding a file: %v", err)
	}

	got := redigest(t, snap)
	paths := make([]string, 0, len(got))
	for _, d := range got {
		paths = append(paths, d.RelPath)
	}
	if want := []string{"aaa.go", "zzz.go"}; !slices.Equal(paths, want) {
		t.Errorf("drifts = %v, want %v in path order", paths, want)
	}
}

func TestRestoreOneSaysWhichStepOfThePutBackFailed(t *testing.T) {
	t.Parallel()

	stage := func(t *testing.T) *Snapshot {
		t.Helper()
		base := t.TempDir()
		source, tree := filepath.Join(base, "src"), filepath.Join(base, "tree")
		for _, dir := range []string{source, tree} {
			if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o700); err != nil {
				t.Fatalf("staging %s: %v", dir, err)
			}
		}
		if err := os.WriteFile(filepath.Join(source, "pkg", "a.go"), []byte("package pkg\n"), 0o600); err != nil {
			t.Fatalf("writing the source: %v", err)
		}
		return &Snapshot{SourceRoot: source, Root: tree}
	}
	change := Drift{Kind: DriftChanged, RelPath: "pkg/a.go", WantSHA256: digestOf("package pkg\n")}

	t.Run("the drifted file cannot be removed", func(t *testing.T) {
		t.Parallel()

		s := stage(t)
		dest := filepath.Join(s.Root, "pkg", "a.go")
		if err := os.WriteFile(dest, []byte("drifted\n"), 0o600); err != nil {
			t.Fatalf("writing the drifted file: %v", err)
		}
		unwritableDir(t, filepath.Dir(dest))

		err := s.restoreOne(change)
		assertCode(t, err, CodeRestoreFailed)
		if !strings.Contains(err.Error(), "removed before being restored") {
			t.Errorf("the failure names the wrong step: %v", err)
		}
	})

	t.Run("the directory holding it cannot be created", func(t *testing.T) {
		t.Parallel()

		s := stage(t)
		if err := os.WriteFile(filepath.Join(s.SourceRoot, "pkg", "sub", "a.go"), nil, 0o600); err == nil {
			t.Fatal("the source subdirectory was not supposed to exist yet")
		}
		if err := os.MkdirAll(filepath.Join(s.SourceRoot, "pkg", "sub"), 0o700); err != nil {
			t.Fatalf("staging the source: %v", err)
		}
		if err := os.WriteFile(filepath.Join(s.SourceRoot, "pkg", "sub", "a.go"), []byte("package sub\n"), 0o600); err != nil {
			t.Fatalf("writing the source: %v", err)
		}
		unwritableDir(t, filepath.Join(s.Root, "pkg"))

		err := s.restoreOne(Drift{Kind: DriftRemoved, RelPath: "pkg/sub/a.go", WantSHA256: digestOf("package sub\n")})
		assertCode(t, err, CodeRestoreFailed)
		if !strings.Contains(err.Error(), "directory holding the restored file") {
			t.Errorf("the failure names the wrong step: %v", err)
		}
	})

	t.Run("the file cannot be copied back", func(t *testing.T) {
		t.Parallel()

		s := stage(t)
		unwritableDir(t, filepath.Join(s.Root, "pkg"))

		err := s.restoreOne(change)
		assertCode(t, err, CodeRestoreFailed)
		if !strings.Contains(err.Error(), "could not be copied back") {
			t.Errorf("the failure names the wrong step: %v", err)
		}
	})

	t.Run("and the tree it was made of no longer holds it", func(t *testing.T) {
		t.Parallel()

		s := stage(t)
		if err := os.Remove(filepath.Join(s.SourceRoot, "pkg", "a.go")); err != nil {
			t.Fatalf("removing the source: %v", err)
		}
		err := s.restoreOne(change)
		assertCode(t, err, CodeRestoreFailed)
		if !strings.Contains(err.Error(), "no longer holds the file") {
			t.Errorf("the failure names the wrong step: %v", err)
		}
	})
}

func TestTheRejectionReportedIsTheFirstInPathOrder(t *testing.T) {
	t.Parallel()

	w := &walker{root: "/tmp/x"}
	w.reject(CodeSymlink, "zzz/link.go", "refuses to follow a symbolic link")
	w.reject(CodeSymlink, "aaa/link.go", "refuses to follow a symbolic link")
	w.reject(CodeSymlink, "mmm/link.go", "refuses to follow a symbolic link")

	err := w.rejection()
	var first *Error
	if !errors.As(err, &first) {
		t.Fatalf("rejection() = %v, want one of the refusals", err)
	}
	if first.Path != "aaa/link.go" {
		t.Errorf("rejection() names %q, want the first in path order", first.Path)
	}

	if got := (&walker{}).rejection(); got != nil {
		t.Errorf("rejection() of a clean walk = %v, want nil", got)
	}
}

func pathOfError(t *testing.T, err error) string {
	t.Helper()
	var coded *Error
	if !errors.As(err, &coded) {
		t.Fatalf("the failure is not this package's: %v", err)
	}
	return coded.Path
}

func TestAReportDirectoryThatIsNotOneIsRefusedBeforeAnythingIsCopied(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		reportDir string
		want      error
	}{
		{reportDir: "../outside", want: mutation.ErrEscapingPath},
		{reportDir: "..", want: mutation.ErrEscapingPath},
		{reportDir: "/absolute/out", want: mutation.ErrAbsolutePath},
		{reportDir: "C:/volume/out", want: mutation.ErrAbsolutePath},
	} {
		t.Run(test.reportDir, func(t *testing.T) {
			t.Parallel()

			_, err := exclusions(Options{ReportDir: test.reportDir})
			assertCode(t, err, CodeInvalidOptions)
			if got := pathOfError(t, err); got != test.reportDir {
				t.Errorf("the failure's path is %q, want the value the caller gave", got)
			}
			if !errors.Is(err, test.want) {
				t.Errorf("the failure = %v, want %v underneath it", err, test.want)
			}
		})
	}

	base, err := exclusions(Options{})
	if err != nil {
		t.Fatalf("exclusions of no options: %v", err)
	}
	same, err := exclusions(Options{ReportDir: DefaultReportDir})
	if err != nil {
		t.Fatalf("exclusions of the default report directory: %v", err)
	}
	if len(same) != len(base) {
		t.Errorf("the default report directory added %d patterns, want none", len(same)-len(base))
	}
	other, err := exclusions(Options{ReportDir: "build/out"})
	if err != nil {
		t.Fatalf("exclusions of a configured report directory: %v", err)
	}
	if len(other) != len(base)+1 {
		t.Errorf("a configured report directory added %d patterns, want one", len(other)-len(base))
	}
}

func TestDestinationReportsNoStableNameWhenItFails(t *testing.T) {
	swapSeam(t, &absPath, func(string) (string, error) { return "", errRefused })

	dir, stable, err := destination(t.TempDir(), t.TempDir())
	assertCode(t, err, CodeDestination)
	if stable {
		t.Error("a destination that was never created reported the stable name")
	}
	if dir != "" {
		t.Errorf("a failed destination answered %q as well", dir)
	}
}
