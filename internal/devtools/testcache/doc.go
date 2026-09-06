// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Command testcache owns the build cache this repository's test suites fill,
// so that the developer's own cache is not where the suites leave their rubble.
//
// # The two failures this exists between
//
// The bloat is not go-mutants compiling itself. The suites drive thousands of
// child `go build`, `go test -c` and `go list` commands against fixtures,
// synthesized modules and instrumented snapshots, and every one of those trees
// lives at an absolute path that exists for a single run — so every entry they
// produce is keyed on a path that will never be seen again. That is what took
// ~/.cache/go-build to 14 GB on the machine this was written on, twice, and
// filled a disk from the direction nobody was watching.
//
// The obvious fix is the other failure. A cache per test — a GOCACHE under
// t.TempDir() — is correct, hermetic, and recompiles the standard library once
// per test binary, which is minutes per package and hours per suite.
//
// So there is exactly one cache, it is shared, it is persistent, it is outside
// every temporary directory a test owns, and it has a budget. This program is
// what names it, measures it, empties it, and exports it into the one child
// process that is going to fill it:
//
//	testcache path [--kept] [--marker]            print a directory, or a marker's name
//	testcache status                              path, existence, size, file count
//	testcache clean                               empty it and the kept scratch root
//	testcache trim --budget 4GiB                  empty it only if it is over budget
//	testcache exec --budget 4GiB -- go test ...   run a command against it
//
// The directory is GO_MUTANTS_TEST_GOCACHE when that names an absolute path,
// and <os.UserCacheDir()>/go-mutants-test/go-build otherwise. CI points the
// variable at the runner's own temporary area, which dies with the runner and
// is never restored from an actions cache, so a job cannot inherit yesterday's
// 14 GB.
//
// # Why this cannot import internal/testkit
//
// internal/testkit resolves the same directory, from the same rule, for the
// suites. This program may not import it: testkit takes a testing.TB and fails
// tests, so importing it from anything that ships would link the testing
// package — and its flag registrations — into go-mutants. The import gate
// (TestProductionCodeDoesNotImportTestkit) parses every non-test file in the
// tree to enforce that, and this is a production main.
//
// The rule therefore exists twice, and the agreement between the copies is a
// test rather than a compiler check: internal/testkit's TestPathAgreesWithTestkit
// runs `go run ./internal/devtools/testcache path` with and without the
// variable and compares what it prints with what testkit.BuildCache resolved.
// If the two ever drift, the suites fill a directory this tool never empties,
// and the only symptom is a disk that fills a month later.
//
// # Nothing is removed without a marker
//
// This tool empties directories, and it is pointed at them by an environment
// variable. `GO_MUTANTS_TEST_GOCACHE=$HOME` is an absolute path like any other,
// and that was once enough to have `clean` run `go clean -cache` and
// os.RemoveAll against a home directory: nothing in a path says who made it.
//
// So ownership is written down. The harness stamps the cache with
// `.go-mutants-testcache` when a test resolves it, `exec` stamps it before the
// run it wraps, and nothing here removes a directory that does not carry that
// file (`.go-mutants-kept` for the kept scratch root). Two things make the
// scheme hold rather than merely exist:
//
//   - A stamp is never written into a directory that already holds files that
//     are not ours. Otherwise `exec` would issue itself the permission slip on
//     the way in, and `--budget 0` would delete the directory two lines later.
//     Absent and empty both qualify; anything else is reported and left alone.
//   - Three directories are refused whatever they contain, before anything is
//     measured, created or removed: a filesystem root, the user's home, and the
//     go command's own `<cache>/go-build` — the last because it is the exact
//     directory this tool exists to keep the suites out of.
//
// The failure mode of the whole arrangement is a cache that grows, which is the
// problem this tool was written to notice, and the opposite of the one it could
// otherwise cause. `clean` treats a refusal as a failure, because it is a person
// asking for a removal; `trim` and `exec` report it and carry on, because they
// are housekeeping around somebody else's run.
//
// # What else it refuses to do
//
// Two more rules are worth stating because both are the opposite of what a tidy
// implementation would do.
//
// A removal that cannot be finished is reported and the tool exits 0. This runs
// as the last step of a suite and as an `if: always()` step in CI; a file in the
// cache can be held open by an antivirus scanner or a test binary Windows has
// not finished unmapping, and a collector that turns a green run red because a
// directory it wanted to delete is still there has done more damage than the
// directory ever would. It retries once after a pause, then says so.
//
// A trim never touches the kept scratch root. That root holds the evidence of
// runs that failed — the directories the keep-on-failure policy preserves — and
// its size has nothing to do with the build cache's. Deleting a failing run's
// diagnostics because a cache grew is the one thing this tool must not do;
// `clean` removes it, because `clean` is a person asking for exactly that.
package main
