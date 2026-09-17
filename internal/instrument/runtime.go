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

const runtimeDirBase = "gomutants_rt"

const runtimeDirLimit = 1000

const ActiveEnv = "GO_MUTANTS_ACTIVE"

const UnknownMutantExit = 97

const DivergedExit = 96

const LoopCensusEnv = "GO_MUTANTS_LOOP_CENSUS"

const LoopLimitsEnv = "GO_MUTANTS_LOOP_LIMITS"

const (
	runtimeLimit = "Limit"
	runtimeOver  = "Over"
)

const ProbeEnv = "GO_MUTANTS_PROBE"

const ProbeUnavailableExit = 98

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

func writeRuntime(root, dir, modulePath string, catalog *mutation.Catalog, loops []loopSite) error {
	source, err := renderRuntime(dir, modulePath, catalog, loops)
	if err != nil {
		return err
	}
	return writeGeneratedPackage(root, dir, source)
}

func writeProbeRuntime(root, dir string, catalog *mutation.Catalog) error {
	source, err := renderProbeRuntime(dir, catalog)
	if err != nil {
		return err
	}
	return writeGeneratedPackage(root, dir, source)
}

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

func renderRuntime(pkgName, modulePath string, catalog *mutation.Catalog, loops []loopSite) ([]byte, error) {
	mutants := catalog.Mutants()
	size := arraySize(catalog.Len())
	sites := arraySize(len(loops))

	var b strings.Builder
	generatedPreamble(&b)
	fmt.Fprintf(&b, "// Package %s carries the mutant activation state of one go-mutants run.\n", pkgName)
	b.WriteString("//\n")
	b.WriteString("// It is generated into the disposable snapshot, never into the tree it was\n")
	b.WriteString("// copied from, and it is a first-party package of the module under test so that\n")
	b.WriteString("// no go.mod edit and no vendor entry is needed to import it.\n")
	fmt.Fprintf(&b, "package %s\n\n", pkgName)
	b.WriteString("import (\n\t\"bufio\"\n\t\"fmt\"\n\t\"os\"\n\t\"strconv\"\n\t\"strings\"\n\t\"sync\"\n\t\"sync/atomic\"\n)\n\n")

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
	b.WriteString("}\n\n")

	renderLoopCounting(&b, sites, loops, LoopFileSuffix(modulePath))

	return formatGenerated(&b)
}

func renderLoopCounting(b *strings.Builder, sites int, loops []loopSite, suffix string) {
	fmt.Fprintf(b, "// loopCensusEnv names the file this process appends its loop census to.\n")
	fmt.Fprintf(b, "// Empty or unset records nothing.\nconst loopCensusEnv = %q\n\n", LoopCensusEnv)
	fmt.Fprintf(b, "// loopLimitsEnv names the file this process reads its ceilings from. Empty or\n")
	fmt.Fprintf(b, "// unset leaves every ceiling at noLimit, which no loop can reach.\nconst loopLimitsEnv = %q\n\n", LoopLimitsEnv)
	fmt.Fprintf(b, "// loopFileSuffix is what this module adds to either of those paths. A\n")
	fmt.Fprintf(b, "// workspace run has one of these packages per module, each numbering its own\n")
	fmt.Fprintf(b, "// loops, and the two variables name one path.\nconst loopFileSuffix = %q\n\n", suffix)
	fmt.Fprintf(b, "// divergedExit is the status this process exits with when a loop passes its\n")
	fmt.Fprintf(b, "// ceiling.\nconst divergedExit = %d\n\n", DivergedExit)
	fmt.Fprintf(b, "// censusHeader opens the census this process writes.\nconst censusHeader = %q\n\n", censusHeader(len(loops)))
	fmt.Fprintf(b, "// limitsHeader opens the table this process is willing to read.\nconst limitsHeader = %q\n\n", limitsHeader(len(loops)))

	b.WriteString("// noLimit is the ceiling of a run that was given no table: no loop reaches it,\n")
	b.WriteString("// so the counters cost an increment and a compare and decide nothing.\n")
	b.WriteString("const noLimit = ^uint64(0)\n\n")

	b.WriteString("// Limit is one ceiling per counted loop, indexed by the site the rewrite\n")
	b.WriteString("// spells. Counted loops read it and nothing writes it after init.\n")
	fmt.Fprintf(b, "var Limit [%d]uint64\n\n", sites)

	b.WriteString("// loopSites names each counted loop, so that a divergence can say which one it\n")
	b.WriteString("// was rather than leaving a reader to count `for` statements.\n")
	fmt.Fprintf(b, "var loopSites = [%d]string{\n", sites)
	for i, loop := range loops {
		fmt.Fprintf(b, "\t%d: %q,\n", i, loop.Position)
	}
	b.WriteString("}\n\n")

	b.WriteString("// censusFile is the census this process appends to, and nil when it was not\n")
	b.WriteString("// asked for one. censusSeen is the largest count already written for each\n")
	b.WriteString("// site, which keeps the file to about one line per doubling however many\n")
	b.WriteString("// times a loop is entered. censusMu serialises the writes themselves.\n")
	b.WriteString("var (\n\tcensusFile *os.File\n")
	fmt.Fprintf(b, "\tcensusSeen [%d]atomic.Uint64\n", sites)
	b.WriteString("\tcensusMu   sync.Mutex\n)\n\n")

	b.WriteString("// enforcing reports whether this process was given a table of ceilings to\n")
	b.WriteString("// hold its loops to. It is written in init and read everywhere else.\n")
	b.WriteString("var enforcing bool\n\n")

	b.WriteString("// Over is what a counted loop calls when its own counter passes its own\n")
	b.WriteString("// ceiling, and it returns the ceiling that loop should carry on with.\n")
	b.WriteString("//\n")
	b.WriteString("// A process holding a table never returns from it: n iterations of this loop\n")
	b.WriteString("// is more work than the original program did anywhere in this suite, which is\n")
	b.WriteString("// what a mutant that does not return looks like when it is counted rather\n")
	b.WriteString("// than waited for.\n")
	b.WriteString("func Over(i uint32, n uint64) uint64 {\n")
	b.WriteString("	if enforcing {\n")
	b.WriteString("		diverged(i, n)\n")
	b.WriteString("	}\n")
	b.WriteString("	if censusFile != nil {\n")
	b.WriteString("		record(i, n)\n")
	b.WriteString("	}\n")
	b.WriteString("	return n * 2\n")
	b.WriteString("}\n\n")

	b.WriteString("// diverged ends the process, naming the loop and the two counts that decided\n")
	b.WriteString("// it. It never returns.\n")
	b.WriteString("func diverged(i uint32, n uint64) {\n")
	b.WriteString("	fmt.Fprintln(os.Stderr, \"go-mutants: the loop at \"+loopSites[i]+\" ran \"+strconv.FormatUint(n, 10)+\n")
	b.WriteString("		\" times, past the \"+strconv.FormatUint(Limit[i], 10)+\" this run derived for it from what the\"+\n")
	b.WriteString("		\" original program did under the same tests; this mutant does not return\")\n")
	b.WriteString("	os.Exit(divergedExit)\n")
	b.WriteString("}\n\n")

	b.WriteString("// record appends one count to the census, and only when it beats every count\n")
	b.WriteString("// already written for that site.\n")
	b.WriteString("func record(i uint32, n uint64) {\n")
	b.WriteString("	for {\n")
	b.WriteString("		seen := censusSeen[i].Load()\n")
	b.WriteString("		if n <= seen {\n\t\t\treturn\n\t\t}\n")
	b.WriteString("		if censusSeen[i].CompareAndSwap(seen, n) {\n\t\t\tbreak\n\t\t}\n")
	b.WriteString("	}\n")
	b.WriteString("	censusMu.Lock()\n")
	b.WriteString("	defer censusMu.Unlock()\n")
	b.WriteString("	fmt.Fprintln(censusFile, i, n)\n")
	b.WriteString("}\n\n")

	b.WriteString("// init settles the ceilings: every loop unlimited, then the table the\n")
	b.WriteString("// environment names if it names one, and the census file if it asks for one.\n")
	b.WriteString("//\n")
	b.WriteString("// A table that cannot be opened, cannot be read, or was written for another\n")
	b.WriteString("// tree leaves every ceiling at noLimit and says so once: a run that measured\n")
	b.WriteString("// nothing about its loops is a run bounded in time alone, which is what every\n")
	b.WriteString("// run was before it could count.\n")
	b.WriteString("func init() {\n")
	b.WriteString("	for i := range Limit {\n\t\tLimit[i] = noLimit\n\t}\n")
	b.WriteString("	if named := os.Getenv(loopCensusEnv); named != \"\" {\n")
	b.WriteString("		path := named + loopFileSuffix\n")
	b.WriteString("		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)\n")
	b.WriteString("		if err == nil {\n")
	b.WriteString("			censusFile = file\n")
	b.WriteString("			fmt.Fprintln(file, censusHeader)\n")
	b.WriteString("			for i := range Limit {\n\t\t\t\tLimit[i] = 1\n\t\t\t}\n")
	b.WriteString("		} else {\n")
	b.WriteString("			fmt.Fprintln(os.Stderr, \"go-mutants: cannot open the loop census \"+path+\": \"+err.Error())\n")
	b.WriteString("		}\n")
	b.WriteString("	}\n")
	b.WriteString("	named := os.Getenv(loopLimitsEnv)\n")
	b.WriteString("	if named == \"\" {\n\t\treturn\n\t}\n")
	b.WriteString("	path := named + loopFileSuffix\n")
	b.WriteString("	if err := readLimits(path); err != nil {\n")
	b.WriteString("		for i := range Limit {\n\t\t\tLimit[i] = noLimit\n\t\t}\n")
	b.WriteString("		fmt.Fprintln(os.Stderr, \"go-mutants: cannot read the loop limits \"+path+\": \"+err.Error()+\n")
	b.WriteString("			\"; this mutant is bounded in time alone\")\n")
	b.WriteString("		return\n")
	b.WriteString("	}\n")
	b.WriteString("	enforcing = true\n")
	b.WriteString("}\n\n")

	b.WriteString("// readLimits fills Limit from the table at path, or reports why it did not.\n")
	b.WriteString("func readLimits(path string) error {\n")
	b.WriteString("	file, err := os.Open(path)\n")
	b.WriteString("	if err != nil {\n\t\treturn err\n\t}\n")
	b.WriteString("	defer file.Close()\n")
	b.WriteString("	scanner := bufio.NewScanner(file)\n")
	b.WriteString("	if !scanner.Scan() || scanner.Text() != limitsHeader {\n")
	b.WriteString("		return fmt.Errorf(\"the table was not written for this tree\")\n")
	b.WriteString("	}\n")
	b.WriteString("	for scanner.Scan() {\n")
	b.WriteString("		left, right, ok := strings.Cut(scanner.Text(), \" \")\n")
	b.WriteString("		if !ok {\n\t\t\treturn fmt.Errorf(\"%q is not a site and a ceiling\", scanner.Text())\n\t\t}\n")
	b.WriteString("		site, siteErr := strconv.Atoi(left)\n")
	b.WriteString("		if siteErr != nil || site < 0 || site >= len(Limit) {\n")
	b.WriteString("			return fmt.Errorf(\"%q names no loop of this tree\", left)\n\t\t}\n")
	b.WriteString("		ceiling, ceilErr := strconv.ParseUint(right, 10, 64)\n")
	b.WriteString("		if ceilErr != nil {\n\t\t\treturn ceilErr\n\t\t}\n")
	b.WriteString("		Limit[site] = ceiling\n")
	b.WriteString("	}\n")
	b.WriteString("	return scanner.Err()\n")
	b.WriteString("}\n")
}

func renderProbeRuntime(pkgName string, catalog *mutation.Catalog) ([]byte, error) {
	size := arraySize(catalog.Len())

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

	b.WriteString("// Differs yields v, having recorded through Infect that mutant i's site would\n")
	b.WriteString("// have read as m instead wherever the two disagree.\n")
	b.WriteString("//\n")
	b.WriteString("// It is what the boolean probe form is written as. Both readings are\n")
	b.WriteString("// evaluated before the call, by the compiler, in the site's own context; what\n")
	b.WriteString("// this adds is one comparison and, the first time it fails, one line in the\n")
	b.WriteString("// log. The value returned is the original's, so the program this is spliced\n")
	b.WriteString("// into is the program without it.\n")
	b.WriteString("func Differs(i uint32, v, m bool) bool {\n")
	b.WriteString("\tif v != m {\n\t\tInfect(i)\n\t}\n")
	b.WriteString("\treturn v\n")
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
	b.WriteString("\tprobeFile = file\n")
	b.WriteString("}\n")

	return formatGenerated(&b)
}

func probeDiagnostic(b *strings.Builder, indent, what, cause string) {
	b.WriteString(indent + "fmt.Fprintln(os.Stderr, " + what + "+\": \"+" + cause + ".Error()+\n")
	b.WriteString(indent + "\t\"; a probe process that cannot record what it saw would be read as having seen nothing\")\n")
	b.WriteString(indent + "os.Exit(probeUnavailableExit)\n")
}

func arraySize(mutants int) int {
	if mutants > 0 {
		return mutants
	}
	return 1
}

func generatedPreamble(b *strings.Builder) {
	b.WriteString("// SPDX-FileCopyrightText: 2026 go-mutants contributors\n")
	b.WriteString("// SPDX-License-Identifier: MIT OR Apache-2.0\n\n")
	b.WriteString("// Code generated by go-mutants. DO NOT EDIT.\n\n")
}

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
